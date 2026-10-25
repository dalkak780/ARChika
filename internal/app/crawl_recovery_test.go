package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAutomaticCrawlWaitsForExplicitBulkRecovery(t *testing.T) {
	a, roots := testApp(t)
	root := roots[1]
	writeZIP(t, filepath.Join(root.Path, "existing.zip"))
	existing := registerWork(t, a, root, "existing.zip")
	if _, e := a.DB.Exec("UPDATE roots SET auto_crawl=1,providers='[\"local-json\"]' WHERE id=?", root.ID); e != nil {
		t.Fatal(e)
	}
	root.AutoCrawl = true
	root.Providers = []string{"local-json"}
	if got := registerWork(t, a, root, "existing.zip"); got != existing {
		t.Fatal("repeat observation changed identity")
	}
	var count int
	_ = a.DB.QueryRow("SELECT count(*) FROM jobs WHERE type='auto_crawl'").Scan(&count)
	if count != 0 {
		t.Fatal("enabling automatic crawling backfilled existing records")
	}
	writeZIP(t, filepath.Join(root.Path, "new.zip"))
	work := registerWork(t, a, root, "new.zip")
	job, e := scanJob(a.DB.QueryRow("SELECT "+jobColumns+" FROM jobs WHERE type='auto_crawl' AND work_id=?", work))
	if e != nil {
		t.Fatal(e)
	}
	if e = a.crawler.RunJob(context.Background(), job); !errors.Is(e, errJobDeferred) {
		t.Fatal("unstable new work was crawled", e)
	}
	var state string
	_ = a.DB.QueryRow("SELECT state FROM jobs WHERE id=?", job.ID).Scan(&state)
	if state != "paused_dependency" {
		t.Fatal("automatic job entered a retry storm", state)
	}
	config := a.Config
	if e = a.Close(); e != nil {
		t.Fatal(e)
	}
	restarted, e := New(config)
	if e != nil {
		t.Fatal(e)
	}
	defer restarted.Close()
	_ = restarted.DB.QueryRow("SELECT state FROM jobs WHERE id=?", job.ID).Scan(&state)
	if state != "paused_dependency" {
		t.Fatal("restart bypassed paused import dependency")
	}
	if e = os.WriteFile(filepath.Join(root.Path, "new.zip.json"), []byte(`{"fields":{"title":"Optional metadata"},"tags":["artist:fixture"]}`), 0600); e != nil {
		t.Fatal(e)
	}
	readyWork(t, restarted, work)
	_ = restarted.DB.QueryRow("SELECT state FROM jobs WHERE id=?", job.ID).Scan(&state)
	if state != "queued" {
		t.Fatal("explicit on-demand indexing did not release its own crawl dependency")
	}
	if e = restarted.crawler.RunJob(context.Background(), job); e != nil {
		t.Fatal(e)
	}
	_ = restarted.DB.QueryRow("SELECT count(*) FROM provider_snapshots WHERE work_id=? AND provider='local-json'", work).Scan(&count)
	if count != 1 {
		t.Fatal("new-item crawl lost its durable trigger")
	}
	_ = restarted.DB.QueryRow("SELECT count(*) FROM jobs WHERE type='auto_crawl' AND work_id=?", work).Scan(&count)
	if count != 1 {
		t.Fatal("restart duplicated automatic crawl trigger")
	}
	_ = restarted.DB.QueryRow("SELECT count(*) FROM thumbnails").Scan(&count)
	if count != 0 {
		t.Fatal("automatic metadata crawl generated thumbnails")
	}
}
