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

func TestAmbiguousCrossRootMoveRequiresConfirmationAndChangesACL(t *testing.T) {
	a, roots := testApp(t)
	from, to := roots[1], roots[0]
	file := filepath.Join(from.Path, "old.zip")
	writeZIP(t, file)
	old := registerWork(t, a, from, "old.zip")
	before := readyWork(t, a, old)
	data, e := os.ReadFile(file)
	if e != nil {
		t.Fatal(e)
	}
	st, e := os.Stat(file)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(file); e != nil {
		t.Fatal(e)
	}
	if _, e = a.DB.Exec("UPDATE aliases SET status='offline' WHERE work_id=?", old); e != nil {
		t.Fatal(e)
	}
	target := filepath.Join(to.Path, "new.zip")
	if e = os.WriteFile(target, data, 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.Chtimes(target, st.ModTime(), st.ModTime()); e != nil {
		t.Fatal(e)
	}
	newID := registerWork(t, a, to, "new.zip")
	if newID == old {
		t.Fatal("ambiguous cross-root copy silently merged")
	}
	var candidate string
	if e = a.DB.QueryRow("SELECT id FROM move_candidates WHERE from_work=? AND to_work=? AND state='pending'", old, newID).Scan(&candidate); e != nil {
		t.Fatal(e)
	}
	if _, e = a.DB.Exec("INSERT INTO manual_fields VALUES(?,?,?)", old, "description", "retained human metadata"); e != nil {
		t.Fatal(e)
	}
	oldUser := userForRoot(t, a, from.ID)
	request := httptest.NewRequest("POST", "/api/admin/moves/"+candidate, strings.NewReader(`{"action":"approve","confirm":true}`))
	request.Header.Set("Content-Type", "application/json")
	request.SetPathValue("id", candidate)
	response := httptest.NewRecorder()
	a.movesHTTP(response, request, User{Admin: true})
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	if _, e = a.AuthorizedAlias(context.Background(), oldUser, old); e == nil {
		t.Fatal("move retained old-root authorization")
	}
	fresh, e := a.AuthorizedAlias(context.Background(), User{Admin: true}, old)
	if e != nil {
		t.Fatal(e)
	}
	if fresh.RootID != to.ID || fresh.Version != before.Version+1 || fresh.RegisteredAt != before.RegisteredAt {
		t.Fatalf("move damaged identity/revision: %+v", fresh)
	}
	var description string
	if e = a.DB.QueryRow("SELECT value FROM manual_fields WHERE work_id=? AND field='description'", old).Scan(&description); e != nil || description != "retained human metadata" {
		t.Fatal("move lost manual provenance")
	}
	if _, e = os.Stat(target); e != nil {
		t.Fatal("move operation mutated original")
	}
}
func TestCleanupRechecksAbsenceAndBlocksOfflineRoots(t *testing.T) {
	a, roots := testApp(t)
	root := roots[1]
	file := filepath.Join(root.Path, "keep.zip")
	writeZIP(t, file)
	work := registerWork(t, a, root, "keep.zip")
	writeZIP(t, filepath.Join(root.Path, "live.zip"))
	registerWork(t, a, root, "live.zip")
	if a.missingEligible(context.Background(), work) {
		t.Fatal("existing source eligible for cleanup")
	}
	bytes, e := os.ReadFile(file)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(file); e != nil {
		t.Fatal(e)
	}
	if !a.missingEligible(context.Background(), work) {
		t.Fatal("missing source was not eligible")
	}
	if _, e = a.DB.Exec("UPDATE roots SET status='offline' WHERE id=?", root.ID); e != nil {
		t.Fatal(e)
	}
	if a.missingEligible(context.Background(), work) {
		t.Fatal("offline root permitted cleanup")
	}
	if _, e = a.DB.Exec("UPDATE roots SET status='online' WHERE id=?", root.ID); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(file, bytes, 0600); e != nil {
		t.Fatal(e)
	}
	body, _ := json.Marshal(map[string]any{"ids": []string{work}, "confirm": true})
	request := httptest.NewRequest("POST", "/api/admin/cleanup", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	a.cleanupHTTP(response, request, User{Admin: true})
	if response.Code != 200 {
		t.Fatal(response.Code)
	}
	var result struct {
		Count   int
		Blocked []string
	}
	if e = json.Unmarshal(response.Body.Bytes(), &result); e != nil || result.Count != 0 || len(result.Blocked) != 1 {
		t.Fatal("cleanup did not recheck restored source", response.Body.String())
	}
	var n int
	_ = a.DB.QueryRow("SELECT count(*) FROM works WHERE id=?", work).Scan(&n)
	if n != 1 {
		t.Fatal("cleanup removed restored metadata")
	}
}
