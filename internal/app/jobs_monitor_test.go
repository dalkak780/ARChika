package app

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type jobMonitorLog struct {
	slog.Handler
	records chan slog.Record
}

func (h jobMonitorLog) Handle(ctx context.Context, r slog.Record) error {
	select {
	case h.records <- r.Clone():
	default:
	}
	return h.Handler.Handle(ctx, r)
}

func startWaitingIndexJob(t *testing.T, log *slog.Logger) (*App, string, *indexFlight, context.Context) {
	t.Helper()
	a, roots := testApp(t)
	a.Log = log
	root := roots[1]
	writeZIP(t, filepath.Join(root.Path, "waiting.zip"))
	work := registerWork(t, a, root, "waiting.zip")
	al, e := a.AuthorizedAlias(context.Background(), User{Admin: true}, work)
	if e != nil {
		t.Fatal(e)
	}
	tx, e := a.DB.Begin()
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if _, e = tx.Exec("DELETE FROM jobs"); e != nil {
		t.Fatal(e)
	}
	jid, e := enqueueTx(tx, "incremental", root.ID, work, "", "manual", "", nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(a.ctx)
	t.Cleanup(cancel)
	f := &indexFlight{done: make(chan struct{}), cancel: cancel}
	a.indexFlights[fmt.Sprintf("%s:%d", work, al.Version)] = f
	a.wg.Add(1)
	go func() { defer a.wg.Done(); a.jobLoop() }()
	a.signal()
	deadline := time.Now().Add(3 * time.Second)
	for {
		a.indexMu.Lock()
		waiting := f.waiters
		a.indexMu.Unlock()
		if waiting == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("job did not join the pending index operation")
		}
		time.Sleep(time.Millisecond)
	}
	return a, jid, f, ctx
}

func waitForJobState(t *testing.T, a *App, jid, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var state string
		if e := a.DB.QueryRow("SELECT state FROM jobs WHERE id=?", jid).Scan(&state); e != nil {
			t.Fatal(e)
		}
		if state == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("job state=%q, want %q", state, want)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestJobMonitorKeepsRunningWhenDatabaseBusy(t *testing.T) {
	records := make(chan slog.Record, 8)
	log := slog.New(jobMonitorLog{Handler: slog.NewTextHandler(io.Discard, nil), records: records})
	a, jid, f, ctx := startWaitingIndexJob(t, log)
	if _, e := a.DB.Exec("PRAGMA journal_mode=DELETE; PRAGMA busy_timeout=1"); e != nil {
		t.Fatal(e)
	}
	locked, e := sql.Open("sqlite", filepath.Join(a.Config.DataDir, "archika.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	defer locked.Close()
	locked.SetMaxOpenConns(1)
	if _, e = locked.Exec("BEGIN EXCLUSIVE"); e != nil {
		t.Fatal(e)
	}
	defer locked.Exec("ROLLBACK")
	if e = a.jobAlive(context.Background(), jid); e == nil || !strings.Contains(e.Error(), "SQLITE_BUSY") {
		t.Fatalf("expected database contention, got %v", e)
	}
	select {
	case r := <-records:
		if r.Message != "background job monitor failed" {
			t.Fatalf("unexpected log: %s", r.Message)
		}
	case <-ctx.Done():
		t.Fatal("monitor cancelled the job on a transient database error")
	case <-time.After(3 * time.Second):
		t.Fatal("monitor did not report the database error")
	}
	if _, e = locked.Exec("ROLLBACK"); e != nil {
		t.Fatal(e)
	}
	close(f.done)
	waitForJobState(t, a, jid, "completed")
}

func TestJobLoopCancelledResultCanBeRetried(t *testing.T) {
	a, jid, f, _ := startWaitingIndexJob(t, slog.Default())
	f.err = fmt.Errorf("index operation: %w", context.Canceled)
	close(f.done)
	waitForJobState(t, a, jid, "failed")
	request := httptest.NewRequest("POST", "/api/admin/jobs/"+jid, strings.NewReader(`{"action":"retry"}`))
	request.Header.Set("Content-Type", "application/json")
	request.SetPathValue("id", jid)
	response := httptest.NewRecorder()
	a.jobAction(response, request, User{Admin: true})
	if response.Code != 200 {
		t.Fatal("retry action", response.Body.String())
	}
}

func TestJobMonitorStopsForUserActionOrDeletion(t *testing.T) {
	for _, action := range []string{"pause", "cancel", "delete"} {
		t.Run(action, func(t *testing.T) {
			a, jid, _, ctx := startWaitingIndexJob(t, slog.Default())
			if action == "delete" {
				if _, e := a.DB.Exec("DELETE FROM jobs WHERE id=?", jid); e != nil {
					t.Fatal(e)
				}
			} else {
				request := httptest.NewRequest("POST", "/api/admin/jobs/"+jid, strings.NewReader(`{"action":"`+action+`"}`))
				request.Header.Set("Content-Type", "application/json")
				request.SetPathValue("id", jid)
				response := httptest.NewRecorder()
				a.jobAction(response, request, User{Admin: true})
				if response.Code != 200 {
					t.Fatal("job action", response.Body.String())
				}
			}
			select {
			case <-ctx.Done():
			case <-time.After(3 * time.Second):
				t.Fatal("monitor did not stop the job")
			}
			tx, e := a.DB.Begin()
			if e != nil {
				t.Fatal(e)
			}
			defer tx.Rollback()
			next, e := enqueueTx(tx, "unsupported", "", "", "", "manual", "", nil)
			if e != nil {
				t.Fatal(e)
			}
			if e = tx.Commit(); e != nil {
				t.Fatal(e)
			}
			a.signal()
			waitForJobState(t, a, next, "failed")
			if action == "delete" {
				var count int
				if e = a.DB.QueryRow("SELECT count(*) FROM jobs WHERE id=?", jid).Scan(&count); e != nil || count != 0 {
					t.Fatalf("deleted job count=%d, error=%v", count, e)
				}
			} else {
				state := "paused"
				if action == "cancel" {
					state = "cancelled"
				}
				waitForJobState(t, a, jid, state)
			}
		})
	}
}
