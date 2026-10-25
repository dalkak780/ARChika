package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestScanResumeRetainsAliasesObservedWhilePaused(t *testing.T) {
	for _, state := range []string{"paused", "paused_restart"} {
		t.Run(state, func(t *testing.T) {
			a, roots := testApp(t)
			root := roots[1]
			ctx := context.Background()
			observe := func(name string, j Job) {
				t.Helper()
				st, e := os.Stat(filepath.Join(root.Path, name))
				if e != nil {
					t.Fatal(e)
				}
				if _, e = a.observe(ctx, root, name, archiveKind(name), st, st.Size(), st.ModTime().UnixNano(), j, false); e != nil {
					t.Fatal(e)
				}
			}
			for _, name := range []string{"keep.zip", "missing.zip", "restored.zip"} {
				writeZIP(t, filepath.Join(root.Path, name))
				observe(name, Job{Type: "incremental"})
			}
			observe("restored.zip", Job{ID: "older-scan", Type: "scan"})
			for _, name := range []string{"missing.zip", "restored.zip"} {
				if e := os.Remove(filepath.Join(root.Path, name)); e != nil {
					t.Fatal(e)
				}
			}
			j := Job{ID: id(), Type: "scan", RootID: root.ID, Origin: "manual", Created: now()}
			if _, e := a.DB.Exec("INSERT INTO jobs(id,type,root_id,state,created,updated) VALUES(?,?,?,'running',?,?)", j.ID, j.Type, root.ID, j.Created, j.Created); e != nil {
				t.Fatal(e)
			}
			// Pause after the directory checkpoint, before the final sweep.
			if _, e := a.DB.Exec(`CREATE TRIGGER pause_scan_after_directory AFTER UPDATE OF done ON scan_dirs
				WHEN new.path='.' AND new.done=1 BEGIN
				UPDATE jobs SET state='paused' WHERE id=new.job_id;
				END`); e != nil {
				t.Fatal(e)
			}
			if e := a.scanJob(ctx, j); !errors.Is(e, errJobStopped) {
				t.Fatalf("scan did not pause: %v", e)
			}
			if _, e := a.DB.Exec("DROP TRIGGER pause_scan_after_directory"); e != nil {
				t.Fatal(e)
			}
			if state == "paused_restart" {
				if _, e := a.DB.Exec("UPDATE jobs SET state='running' WHERE id=?", j.ID); e != nil {
					t.Fatal(e)
				}
				if e := a.DB.Close(); e != nil {
					t.Fatal(e)
				}
				db, e := OpenDB(a.Config)
				if e != nil {
					t.Fatal(e)
				}
				a.DB = db
			}
			var gotState string
			if e := a.DB.QueryRow("SELECT state FROM jobs WHERE id=?", j.ID).Scan(&gotState); e != nil {
				t.Fatal(e)
			}
			if gotState != state {
				t.Fatalf("job state = %q, want %q", gotState, state)
			}
			for _, name := range []string{"new.zip", "restored.zip", "incremental.zip"} {
				writeZIP(t, filepath.Join(root.Path, name))
				observed := Job{Type: "incremental", Origin: "watcher"}
				if name == "incremental.zip" {
					observed = Job{ID: id(), Type: "incremental_scan", Origin: "watcher"}
				}
				observe(name, observed)
			}
			if _, e := a.DB.Exec("UPDATE jobs SET state='running' WHERE id=?", j.ID); e != nil {
				t.Fatal(e)
			}
			if e := a.scanJob(ctx, j); e != nil {
				t.Fatal(e)
			}
			checkStatus := func(name, want string) {
				t.Helper()
				var status string
				if e := a.DB.QueryRow("SELECT status FROM aliases WHERE root_id=? AND path=?", root.ID, name).Scan(&status); e != nil {
					t.Fatal(e)
				}
				if status != want {
					t.Errorf("%s status = %q, want %q", name, status, want)
				}
			}
			for _, name := range []string{"keep.zip", "new.zip", "restored.zip", "incremental.zip"} {
				checkStatus(name, "online")
			}
			checkStatus("missing.zip", "offline")

			// A later full scan must still remove aliases first observed during the pause.
			for _, name := range []string{"new.zip", "restored.zip", "incremental.zip"} {
				if e := os.Remove(filepath.Join(root.Path, name)); e != nil {
					t.Fatal(e)
				}
			}
			j.ID = id()
			if _, e := a.DB.Exec("INSERT INTO jobs(id,type,root_id,state,created,updated) VALUES(?,?,?,'running',?,?)", j.ID, j.Type, root.ID, now(), now()); e != nil {
				t.Fatal(e)
			}
			if e := a.scanJob(ctx, j); e != nil {
				t.Fatal(e)
			}
			checkStatus("keep.zip", "online")
			for _, name := range []string{"missing.zip", "new.zip", "restored.zip", "incremental.zip"} {
				checkStatus(name, "offline")
			}
		})
	}
}

func TestScanSkipsDuplicateGalleryDirectory(t *testing.T) {
	a, roots := testApp(t)
	root := roots[1]
	if e := os.Mkdir(filepath.Join(root.Path, "gallery"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(root.Path, "gallery", "page.png"), testPNG(t), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink("gallery", filepath.Join(root.Path, "duplicate")); e != nil {
		t.Fatal(e)
	}
	j := Job{ID: id(), Type: "scan", RootID: root.ID, Origin: "manual", Created: now()}
	if _, e := a.DB.Exec("INSERT INTO jobs(id,type,root_id,state,created,updated) VALUES(?,?,?,'running',?,?)", j.ID, j.Type, root.ID, j.Created, j.Created); e != nil {
		t.Fatal(e)
	}
	if e := a.scanJob(context.Background(), j); e != nil {
		t.Fatal(e)
	}
	var aliases int
	if e := a.DB.QueryRow("SELECT count(*) FROM aliases WHERE root_id=?", root.ID).Scan(&aliases); e != nil {
		t.Fatal(e)
	}
	if aliases != 1 {
		t.Fatalf("indexed %d aliases for one physical gallery, want 1", aliases)
	}
	var pending int
	if e := a.DB.QueryRow("SELECT count(*) FROM scan_dirs WHERE job_id=? AND done<>1", j.ID).Scan(&pending); e != nil {
		t.Fatal(e)
	}
	if pending != 0 {
		t.Fatalf("unfinished directories = %d, want 0", pending)
	}
}
