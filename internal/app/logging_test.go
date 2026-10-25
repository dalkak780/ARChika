package app

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type logBuffer struct {
	sync.Mutex
	bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	return b.Buffer.Write(p)
}
func (b *logBuffer) text() string { b.Lock(); defer b.Unlock(); return b.Buffer.String() }
func captureLogs(a *App) *logBuffer {
	b := &logBuffer{}
	a.Log = slog.New(slog.NewJSONHandler(b, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return b
}
func logRecords(t *testing.T, b *logBuffer, message string) []map[string]any {
	t.Helper()
	out := []map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(b.text()), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record["msg"] == message {
			out = append(out, record)
		}
	}
	return out
}
func oneLog(t *testing.T, b *logBuffer, message string) map[string]any {
	t.Helper()
	records := logRecords(t, b, message)
	if len(records) != 1 {
		t.Fatalf("want one %q, got %d: %s", message, len(records), b.text())
	}
	return records[0]
}

func TestLoginLogs(t *testing.T) {
	a, _ := testApp(t)
	a.Config.Password = PasswordPolicy{MemoryKiB: 19 * 1024, Iterations: 2, Parallelism: 1, MaxMemoryKiB: 65536}
	const password = "private-password-fixture"
	if err := BootstrapAdmin(a.DB, "known", password, a.Config.Password); err != nil {
		t.Fatal(err)
	}
	failureBody := ""
	for _, test := range []struct {
		name, password, reason string
		disabled               bool
	}{
		{"known", password, "", false}, {"known", "incorrect-secret", "wrong_password", false},
		{"unknown-secret-name", password, "unknown_account", false}, {"known", password, "disabled", true},
	} {
		t.Run(test.reason, func(t *testing.T) {
			if _, err := a.DB.Exec("UPDATE users SET disabled=?", test.disabled); err != nil {
				t.Fatal(err)
			}
			b := captureLogs(a)
			body, _ := json.Marshal(map[string]string{"name": test.name, "password": test.password})
			req := httptest.NewRequest("POST", "/api/auth/login", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.RemoteAddr = "192.0.2.7:1234"
			res := httptest.NewRecorder()
			a.login(res, req)
			message := "sign-in failed"
			if test.reason == "" {
				message = "sign-in succeeded"
				if res.Code != 200 {
					t.Fatal(res.Body.String())
				}
			} else {
				if res.Code != 401 {
					t.Fatal(res.Code)
				}
				if failureBody == "" {
					failureBody = res.Body.String()
				} else if failureBody != res.Body.String() {
					t.Fatal("failure responses differ")
				}
			}
			record := oneLog(t, b, message)
			if record["remote"] != "192.0.2.7" {
				t.Fatal(record)
			}
			if test.reason != "" && record["reason"] != test.reason {
				t.Fatal(record)
			}
			if test.reason == "unknown_account" {
				if _, ok := record["user"]; ok {
					t.Fatal(record)
				}
			} else if record["user"] != "known" {
				t.Fatal(record)
			}
			for _, secret := range []string{password, "incorrect-secret", "unknown-secret-name"} {
				if strings.Contains(b.text(), secret) {
					t.Fatal("secret appeared in logs")
				}
			}
		})
	}
	b := captureLogs(a)
	req := httptest.NewRequest("POST", "/api/auth/login", nil)
	req.RemoteAddr = "192.0.2.8:42"
	for range 11 {
		a.allowPasswordAttempt(httptest.NewRecorder(), req)
	}
	if oneLog(t, b, "sign-in rate limited")["remote"] != "192.0.2.8" {
		t.Fatal(b.text())
	}
}

func TestRequestLogs(t *testing.T) {
	a := &App{}
	b := captureLogs(a)
	req := httptest.NewRequest("GET", "/failure?secret=hidden", nil)
	res := httptest.NewRecorder()
	a.requestLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { panic("fixture panic") })).ServeHTTP(res, req)
	if res.Code != 500 || !strings.Contains(res.Body.String(), "internal_error") {
		t.Fatal(res.Code, res.Body.String())
	}
	record := oneLog(t, b, "request panic")
	if record["panic"] != "fixture panic" || !strings.Contains(record["stack"].(string), "TestRequestLogs") {
		t.Fatal(record)
	}
	record = oneLog(t, b, "request failed")
	if record["status"] != float64(500) || record["path"] != "/failure" {
		t.Fatal(record)
	}
	oneLog(t, b, "request finished")
	if strings.Contains(b.text(), "hidden") {
		t.Fatal("query leaked")
	}
	b = captureLogs(a)
	a.logRequest(req, 503, 6*time.Second)
	if oneLog(t, b, "slow request")["duration_ms"] != float64(6000) {
		t.Fatal(b.text())
	}
	oneLog(t, b, "request failed")
	b = captureLogs(a)
	a.logRequest(req, 200, 5*time.Second)
	if len(logRecords(t, b, "slow request")) != 0 || len(logRecords(t, b, "request failed")) != 0 {
		t.Fatal(b.text())
	}
}

func TestRequestFlushAndCommittedPanic(t *testing.T) {
	a := &App{}
	b := captureLogs(a)
	res := httptest.NewRecorder()
	a.requestLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(202)
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Fatal(err)
		}
		if _, ok := w.(http.Flusher); !ok {
			t.Fatal("Flusher missing")
		}
		panic("after headers")
	})).ServeHTTP(res, httptest.NewRequest("GET", "/stream", nil))
	if res.Code != 202 || !res.Flushed || res.Body.Len() != 0 {
		t.Fatal(res)
	}
	oneLog(t, b, "request panic")
	if oneLog(t, b, "request finished")["status"] != float64(202) {
		t.Fatal(b.text())
	}
}

func TestJobFailureLog(t *testing.T) {
	a, _ := testApp(t)
	b := captureLogs(a)
	tx, err := a.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	jid, err := enqueueTx(tx, "unsupported-fixture", "", "", "", "automatic", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	a.wg.Add(1)
	go func() { defer a.wg.Done(); a.jobLoop() }()
	a.signal()
	waitForJobState(t, a, jid, "failed")
	record := oneLog(t, b, "job failed")
	if record["error"] != "unsupported job type" || record["job"] != jid || record["duration_ms"] == nil {
		t.Fatal(record)
	}
	oneLog(t, b, "job started")
}

func TestIndexFailureLog(t *testing.T) {
	a, roots := testApp(t)
	if err := os.WriteFile(filepath.Join(roots[0].Path, "bad.zip"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	work := registerWork(t, a, roots[0], "bad.zip")
	if _, err := a.DB.Exec("UPDATE works SET index_status='pending' WHERE id=?", work); err != nil {
		t.Fatal(err)
	}
	al, err := a.AuthorizedAlias(context.Background(), User{Admin: true}, work)
	if err != nil {
		t.Fatal(err)
	}
	b := captureLogs(a)
	err = a.buildIndex(context.Background(), al)
	if err == nil {
		t.Fatal("broken ZIP indexed")
	}
	record := oneLog(t, b, "index failed")
	if record["work"] != work || record["root"] != roots[0].ID || record["path"] != "bad.zip" || record["code"] != decodeErrorCode(err) || record["message"] != indexError(err) {
		t.Fatal(record)
	}
	if strings.Contains(b.text(), roots[0].Path) {
		t.Fatal("host path leaked")
	}
}

func TestMigrationLogs(t *testing.T) {
	a, _ := testApp(t)
	for _, code := range []string{"corrupt", "unsupported_format"} {
		if _, err := a.DB.Exec("INSERT INTO works(id,kind,size,mtime,registered_at,index_status,error_code) VALUES(?,'zip',0,0,0,'error',?)", code, code); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.DB.Exec("PRAGMA user_version=3"); err != nil {
		t.Fatal(err)
	}
	b := &logBuffer{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(b, nil)))
	defer slog.SetDefault(old)
	db, err := OpenDB(a.Config)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	records := logRecords(t, b, "database migrated")
	if len(records) != 2 {
		t.Fatal(b.text())
	}
	for i, record := range records {
		if record["from"] != float64(i+3) || record["to"] != float64(i+4) || record["stale"] != float64(1) {
			t.Fatal(record)
		}
	}
}

func TestAdminLogOnlyAfterCommit(t *testing.T) {
	a, _ := testApp(t)
	b := captureLogs(a)
	tx, err := a.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = audit(tx, "actor", "fixture", "root", nil); err != nil {
		t.Fatal(err)
	}
	if b.text() != "" {
		t.Fatal("uncommitted action logged")
	}
	tx.Rollback()
	if err = a.commitAdmin(tx, "actor", "fixture", "root", nil); err == nil {
		t.Fatal("commit succeeded after rollback")
	}
	if b.text() != "" {
		t.Fatal("rolled back action logged")
	}
	tx, err = a.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = audit(tx, "actor", "fixture", "root", nil); err != nil {
		t.Fatal(err)
	}
	if err = a.commitAdmin(tx, "actor", "fixture", "root", nil); err != nil {
		t.Fatal(err)
	}
	if oneLog(t, b, "admin action")["actor"] != "actor" {
		t.Fatal(b.text())
	}
}

func TestRequestStatusLog(t *testing.T) {
	a := &App{}
	b := captureLogs(a)
	res := httptest.NewRecorder()
	a.requestLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		w.WriteHeader(200)
		_, _ = w.Write([]byte("unavailable"))
	})).ServeHTTP(res, httptest.NewRequest("POST", "/api/test?token=secret", nil))
	record := oneLog(t, b, "request failed")
	if record["method"] != "POST" || record["path"] != "/api/test" || record["status"] != float64(503) || res.Code != 503 {
		t.Fatal(record)
	}
	if strings.Contains(b.text(), "secret") {
		t.Fatal("query leaked")
	}
}

func TestScanSummaryLog(t *testing.T) {
	a, roots := testApp(t)
	root := roots[0]
	writeZIP(t, filepath.Join(root.Path, "old.zip"))
	registerWork(t, a, root, "old.zip")
	writeZIP(t, filepath.Join(root.Path, "kept.zip"))
	registerWork(t, a, root, "kept.zip")
	if err := os.Remove(filepath.Join(root.Path, "old.zip")); err != nil {
		t.Fatal(err)
	}
	writeZIP(t, filepath.Join(root.Path, "new.zip"))
	b := captureLogs(a)
	j := Job{ID: id(), Type: "scan", RootID: root.ID, Origin: "automatic", Created: now()}
	if _, err := a.DB.Exec("INSERT INTO jobs(id,type,root_id,state,created,updated) VALUES(?,?,?,'running',?,?)", j.ID, j.Type, root.ID, j.Created, j.Created); err != nil {
		t.Fatal(err)
	}
	if err := a.scanJob(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	record := oneLog(t, b, "scan finished")
	if record["root"] != root.ID || record["name"] != root.Name || record["new"] != float64(1) || record["missing"] != float64(1) || record["dirs"] != float64(1) {
		t.Fatal(record)
	}
	if strings.Contains(b.text(), root.Path) {
		t.Fatal("host path leaked")
	}
}
