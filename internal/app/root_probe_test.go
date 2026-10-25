package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestReachableEmptyMountpointDoesNotPermitCleanupOrCachedPages(t *testing.T) {
	a, roots := testApp(t)
	root := roots[1]
	writeZIP(t, filepath.Join(root.Path, "original.zip"))
	work := registerWork(t, a, root, "original.zip")
	al := readyWork(t, a, work)
	pages, e := a.pageList(context.Background(), al)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = a.loadPage(context.Background(), User{Admin: true}, al, pages[0], Transform{}, false); e != nil {
		t.Fatal(e)
	}
	old := root.Path + ".mounted"
	if e = os.Rename(root.Path, old); e != nil {
		t.Fatal(e)
	}
	if e = os.Mkdir(root.Path, 0700); e != nil {
		t.Fatal(e)
	}
	if a.missingEligible(context.Background(), work) {
		t.Fatal("empty fallback mountpoint allowed destructive metadata cleanup")
	}
	current, e := a.getRoot(context.Background(), root.ID)
	if e != nil || current.Status != "offline" || current.PendingPath != root.Path {
		t.Fatalf("unverified mount was not isolated: %+v %v", current, e)
	}
	if _, e = a.loadPage(context.Background(), User{Admin: true}, al, pages[0], Transform{}, false); e == nil {
		t.Fatal("cached original bypassed unavailable mount")
	}
	var version int64
	_ = a.DB.QueryRow("SELECT version FROM works WHERE id=?", work).Scan(&version)
	if version != al.Version {
		t.Fatal("mount disappearance invalidated persistent source revision")
	}
	var count int
	_ = a.DB.QueryRow("SELECT count(*) FROM pages WHERE work_id=?", work).Scan(&count)
	if count != len(pages) {
		t.Fatal("mount disappearance discarded persistent manifest")
	}
}
func TestDirectoryLocksExcludeAnotherLiveServer(t *testing.T) {
	a, _ := testApp(t)
	if other, e := New(a.Config); e == nil {
		other.Close()
		t.Fatal("second server acquired the same database")
	}
	if cache, e := NewPageCache(a.Config.CacheDir, 1024, 1024, 1024); e == nil {
		cache.Close()
		t.Fatal("second cache reclaimed a live generation")
	}
}
