package app

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBulkRetryUsesOnlyFailedWorkAndRetainsSuccessfulMetadata(t *testing.T) {
	a, roots := testApp(t)
	root := roots[1]
	ids := []string{}
	for _, name := range []string{"first.zip", "second.zip"} {
		writeZIP(t, filepath.Join(root.Path, name))
		work := registerWork(t, a, root, name)
		readyWork(t, a, work)
		ids = append(ids, work)
	}
	admin := userForRoot(t, a, root.ID)
	_, e := a.DB.Exec("UPDATE users SET admin=1 WHERE id=?", admin.ID)
	if e != nil {
		t.Fatal(e)
	}
	admin.Admin = true
	first := `{"fields":{"title":"First successful result"},"tags":["artist:first"]}`
	if e = os.WriteFile(filepath.Join(root.Path, "first.zip.json"), []byte(first), 0600); e != nil {
		t.Fatal(e)
	}
	tx, e := a.DB.Begin()
	if e != nil {
		t.Fatal(e)
	}
	jid, e := enqueueTx(tx, "crawl_bulk", root.ID, "", admin.ID, "manual", "", map[string]any{"provider": "local-json", "work_ids": ids})
	if e != nil {
		t.Fatal(e)
	}
	_, e = tx.Exec("UPDATE jobs SET state='running',total=2 WHERE id=?", jid)
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	j, e := scanJob(a.DB.QueryRow("SELECT "+jobColumns+" FROM jobs WHERE id=?", jid))
	if e != nil {
		t.Fatal(e)
	}
	if e = a.bulkJob(context.Background(), j); e != nil {
		t.Fatal(e)
	}
	var failures int
	_ = a.DB.QueryRow("SELECT count(*) FROM job_failures WHERE job_id=?", jid).Scan(&failures)
	if failures != 1 {
		t.Fatalf("durable failures=%d", failures)
	}
	_, _ = a.DB.Exec("UPDATE jobs SET state='completed' WHERE id=?", jid)
	if e = os.WriteFile(filepath.Join(root.Path, "first.zip.json"), []byte(`{"fields":{"title":"Must not refresh successful work"}}`), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(root.Path, "second.zip.json"), []byte(`{"fields":{"title":"Recovered result"}}`), 0600); e != nil {
		t.Fatal(e)
	}
	request := httptest.NewRequest("POST", "/api/admin/jobs/"+jid, strings.NewReader(`{"action":"retry"}`))
	request.Header.Set("Content-Type", "application/json")
	request.SetPathValue("id", jid)
	response := httptest.NewRecorder()
	a.jobAction(response, request, admin)
	if response.Code != 200 {
		t.Fatal("retry action", response.Body.String())
	}
	_, _ = a.DB.Exec("UPDATE jobs SET state='running' WHERE id=?", jid)
	j, e = scanJob(a.DB.QueryRow("SELECT "+jobColumns+" FROM jobs WHERE id=?", jid))
	if e != nil {
		t.Fatal(e)
	}
	if e = a.bulkJob(context.Background(), j); e != nil {
		t.Fatal(e)
	}
	for i, wid := range ids {
		var fields string
		_ = a.DB.QueryRow("SELECT fields FROM provider_snapshots WHERE work_id=? AND provider='local-json'", wid).Scan(&fields)
		var values map[string]string
		if e = json.Unmarshal([]byte(fields), &values); e != nil {
			t.Fatal(e)
		}
		want := "First successful result"
		if i == 1 {
			want = "Recovered result"
		}
		if values["title"] != want {
			t.Fatalf("retry changed wrong work: %v", values)
		}
	}
	_ = a.DB.QueryRow("SELECT count(*) FROM job_failures WHERE job_id=?", jid).Scan(&failures)
	if failures != 0 {
		t.Fatal("recovered failures were not cleared")
	}
}
