package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyRelocation(t *testing.T) {
	a, roots := testApp(t)
	root := roots[0]
	ctx := context.Background()
	if ok, findings := a.verifyRelocation(ctx, root, root.Path); ok || len(findings) != 1 || findings[0]["reason"] != "No indexed sample; equivalence is inconclusive" {
		t.Fatalf("empty root: ok=%v findings=%v", ok, findings)
	}
	const name = "sample.zip"
	writeZIP(t, filepath.Join(root.Path, name))
	registerWork(t, a, root, name)
	target := filepath.Join(t.TempDir(), "relocated")
	if e := os.Rename(root.Path, target); e != nil {
		t.Fatal(e)
	}
	stat, e := os.Stat(filepath.Join(target, name))
	if e != nil {
		t.Fatal(e)
	}
	key := physicalKey(stat, target, name)
	for _, tc := range []struct {
		name     string
		physical string
		size     int64
		wantOK   bool
		identity bool
	}{
		{"metadata only", "", stat.Size(), true, false},
		{"metadata mismatch", "", stat.Size() + 1, false, false},
		{"identity mismatch", "different-key", stat.Size(), false, false},
		{"matching identity", key, stat.Size(), true, key != ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, e := a.DB.Exec("UPDATE aliases SET physical_key=?,size=?,mtime=? WHERE root_id=?", tc.physical, tc.size, stat.ModTime().UnixNano(), root.ID); e != nil {
				t.Fatal(e)
			}
			ok, findings := a.verifyRelocation(ctx, root, target)
			if ok != tc.wantOK || len(findings) != 1 {
				t.Fatalf("ok=%v findings=%v, want ok=%v and one finding", ok, findings, tc.wantOK)
			}
			f := findings[0]
			if len(f) != 3 || f["relative_path"] != name || f["metadata_matches"] != (tc.size == stat.Size()) || f["identity_matches"] != tc.identity {
				t.Fatalf("unexpected finding: %v", f)
			}
		})
	}
}
