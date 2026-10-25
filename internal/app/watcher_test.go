package app

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

func TestWatchEventsContinueAfterObservationFailure(t *testing.T) {
	a, roots := testApp(t)
	var logs bytes.Buffer
	a.Log = slog.New(slog.NewTextHandler(&logs, nil))
	if _, e := a.DB.Exec(`CREATE TRIGGER fail_watch_observation BEFORE INSERT ON aliases
WHEN new.path LIKE 'bad-%' BEGIN SELECT RAISE(ABORT, 'permanent observation failure'); END`); e != nil {
		t.Fatal(e)
	}
	for i := range 32 {
		rel := fmt.Sprintf("bad-%02d.zip", i)
		writeZIP(t, filepath.Join(roots[0].Path, rel))
		if _, e := a.DB.Exec("INSERT INTO watch_events(root_id,path,observed) VALUES(?,?,?)", roots[0].ID, rel, i*2); e != nil {
			t.Fatal(e)
		}
	}
	for i, root := range roots {
		writeZIP(t, filepath.Join(root.Path, "good.zip"))
		if _, e := a.DB.Exec("INSERT INTO watch_events(root_id,path,observed) VALUES(?,?,?)", root.ID, "good.zip", 1+i*64); e != nil {
			t.Fatal(e)
		}
	}
	for _, root := range roots {
		if e := a.processWatchEvents(context.Background()); e != nil {
			t.Fatal(e)
		}
		var count int
		if e := a.DB.QueryRow("SELECT count(*) FROM aliases WHERE root_id=? AND path='good.zip'", root.ID).Scan(&count); e != nil {
			t.Fatal(e)
		}
		if count != 1 {
			t.Fatalf("later event for root %s was not processed", root.ID)
		}
	}
	var pending int
	if e := a.DB.QueryRow("SELECT count(*) FROM watch_events").Scan(&pending); e != nil {
		t.Fatal(e)
	}
	if pending != 0 {
		t.Fatalf("watch events remaining: %d", pending)
	}
	if got := strings.Count(logs.String(), "permanent observation failure"); got != 32 {
		t.Fatalf("logged observation failures: got %d, want 32", got)
	}
}

type recordingDirectoryWatcher struct {
	added, removed []string
	failAdd        bool
}

func (w *recordingDirectoryWatcher) Add(p string) error {
	w.added = append(w.added, p)
	if w.failAdd {
		return fmt.Errorf("watch unavailable")
	}
	return nil
}

func (w *recordingDirectoryWatcher) Remove(p string) error {
	w.removed = append(w.removed, p)
	return nil
}

func TestRegisterWatchesIncremental(t *testing.T) {
	a, roots := testApp(t)
	root := roots[0]
	exec := func(query string, args ...any) {
		t.Helper()
		if _, e := a.DB.Exec(query, args...); e != nil {
			t.Fatal(e)
		}
	}
	exec("UPDATE roots SET watch=0")
	exec("UPDATE roots SET watch=1 WHERE id=?", root.ID)
	// Cross a page boundary so unchanged watches on later pages are retained.
	for i := range 130 {
		exec("INSERT INTO watch_dirs(root_id,path) VALUES(?,?)", root.ID, fmt.Sprintf("dir-%03d", i))
	}
	w := &recordingDirectoryWatcher{}
	watched := map[string]Root{}
	a.registerWatches(w, watched)
	if len(w.added) != 131 || len(watched) != 131 {
		t.Fatalf("initial watches: added=%d tracked=%d", len(w.added), len(watched))
	}
	w.added = nil
	a.registerWatches(w, watched)
	if len(w.added) != 0 || len(w.removed) != 0 {
		t.Fatalf("unchanged reload: added=%v removed=%v", w.added, w.removed)
	}
	exec("INSERT INTO watch_dirs(root_id,path) VALUES(?,?)", root.ID, "new")
	exec("DELETE FROM watch_dirs WHERE root_id=? AND path=?", root.ID, "dir-000")
	a.registerWatches(w, watched)
	if len(w.added) != 1 || w.added[0] != filepath.Join(root.Path, "new") {
		t.Fatalf("added=%v", w.added)
	}
	if len(w.removed) != 1 || w.removed[0] != filepath.Join(root.Path, "dir-000") {
		t.Fatalf("removed=%v", w.removed)
	}
	w.added, w.removed = nil, nil
	exec("INSERT INTO watch_dirs(root_id,path) VALUES(?,?)", root.ID, "retry")
	w.failAdd = true
	a.registerWatches(w, watched)
	if _, ok := watched[filepath.Join(root.Path, "retry")]; ok {
		t.Fatal("failed watch recorded as installed")
	}
	w.failAdd = false
	w.added = nil
	a.registerWatches(w, watched)
	if len(w.added) != 1 || w.added[0] != filepath.Join(root.Path, "retry") {
		t.Fatalf("retry added=%v", w.added)
	}
	w.removed = nil
	exec("UPDATE roots SET watch=0")
	a.registerWatches(w, watched)
	if len(watched) != 0 || len(w.removed) != 132 {
		t.Fatalf("disabled root: tracked=%d removed=%d", len(watched), len(w.removed))
	}
}
