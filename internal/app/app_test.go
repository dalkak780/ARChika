package app

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if filepath.Base(os.Args[0]) == "metadata-fixture" {
		pluginFixture()
		return
	}
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "decoder":
			if e := DecoderMain(); e != nil {
				os.Exit(2)
			}
			return
		case "image":
			if e := ImageMain(); e != nil {
				os.Exit(2)
			}
			return
		case "plugin-exec":
			if e := PluginExecMain(os.Args[2]); e != nil {
				os.Exit(2)
			}
			return
		}
	}
	os.Exit(m.Run())
}
func testApp(t *testing.T) (*App, []Root) {
	t.Helper()
	base := t.TempDir()
	one := filepath.Join(base, "library")
	two := filepath.Join(base, "private")
	for _, p := range []string{one, two} {
		if e := os.Mkdir(p, 0700); e != nil {
			t.Fatal(e)
		}
	}
	c := DefaultConfig()
	c.DataDir = filepath.Join(base, "data")
	c.CacheDir = filepath.Join(base, "cache")
	c.AllowedPaths = []string{one, two}
	c.QuietSeconds = 1
	c.ScanPerSecond = 10000
	c.Roots = []RootConfig{{Key: "public", Name: "Public", Path: one, QuietSeconds: 1}, {Key: "private", Name: "Private", Path: two, QuietSeconds: 1}}
	a, e := New(c)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { a.Close() })
	rows, e := a.DB.Query("SELECT " + rootColumns + " FROM roots ORDER BY bootstrap_key")
	if e != nil {
		t.Fatal(e)
	}
	roots := []Root{}
	for rows.Next() {
		r, e := scanRoot(rows)
		if e != nil {
			t.Fatal(e)
		}
		roots = append(roots, r)
	}
	rows.Close()
	return a, roots
}
func testPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 32, 48))
	for y := range 48 {
		for x := range 32 {
			img.SetRGBA(x, y, color.RGBA{uint8(x * 7), uint8(y * 5), 123, 255})
		}
	}
	var b bytes.Buffer
	if e := png.Encode(&b, img); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}
func writeZIP(t *testing.T, file string) []byte {
	t.Helper()
	b := testPNG(t)
	if e := os.MkdirAll(filepath.Dir(file), 0700); e != nil {
		t.Fatal(e)
	}
	f, e := os.Create(file)
	if e != nil {
		t.Fatal(e)
	}
	z := zip.NewWriter(f)
	for _, name := range []string{"nested/10.png", "nested/2.png", "front-cover.png"} {
		w, e := z.Create(name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = w.Write(b); e != nil {
			t.Fatal(e)
		}
	}
	if e = z.Close(); e != nil {
		t.Fatal(e)
	}
	f.Close()
	return b
}
func registerWork(t *testing.T, a *App, root Root, rel string) string {
	t.Helper()
	st, e := os.Stat(filepath.Join(root.Path, rel))
	if e != nil {
		t.Fatal(e)
	}
	work, e := a.observe(context.Background(), root, rel, archiveKind(rel), st, st.Size(), st.ModTime().UnixNano(), Job{Type: "scan", Origin: "manual", Created: now()}, false)
	if e != nil {
		t.Fatal(e)
	}
	return work
}
func readyWork(t *testing.T, a *App, work string) Alias {
	t.Helper()
	_, e := a.DB.Exec("UPDATE jobs SET not_before=0 WHERE work_id=?", work)
	if e != nil {
		t.Fatal(e)
	}
	al, e := a.AuthorizedAlias(context.Background(), User{Admin: true}, work)
	if e != nil {
		t.Fatal(e)
	}
	if e = a.ensureIndex(context.Background(), al); e != nil {
		t.Fatal(e)
	}
	al, e = a.AuthorizedAlias(context.Background(), User{Admin: true}, work)
	if e != nil {
		t.Fatal(e)
	}
	return al
}
func userForRoot(t *testing.T, a *App, root string) User {
	t.Helper()
	u := User{ID: id(), Name: "reader"}
	if _, e := a.DB.Exec("INSERT INTO users(id,name,password) VALUES(?,?,?)", u.ID, u.Name, "not a login fixture"); e != nil {
		t.Fatal(e)
	}
	if _, e := a.DB.Exec("INSERT INTO grants VALUES(?,?)", u.ID, root); e != nil {
		t.Fatal(e)
	}
	return u
}
func queryWorks(t *testing.T, a *App, u User, q string) []Alias {
	t.Helper()
	r := httptest.NewRequest("GET", "/api/works?q="+url.QueryEscape(q), nil)
	w := httptest.NewRecorder()
	a.works(w, r, u)
	if w.Code != 200 {
		t.Fatalf("search %q: %d %s", q, w.Code, w.Body.String())
	}
	var out struct{ Items []Alias }
	if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
		t.Fatal(e)
	}
	return out.Items
}
func TestStableIdentityACLAndReadThroughCache(t *testing.T) {
	a, roots := testApp(t)
	public, private := roots[1], roots[0]
	original := writeZIP(t, filepath.Join(public.Path, "日本語", "블루", "[Artist] Work_Name.zip"))
	wid := registerWork(t, a, public, "日本語/블루/[Artist] Work_Name.zip")
	al, e := a.AuthorizedAlias(context.Background(), User{Admin: true}, wid)
	if e != nil {
		t.Fatal(e)
	}
	if e = a.ensureIndex(context.Background(), al); e != errStabilizing {
		t.Fatalf("decoder ran before quiet interval: %v", e)
	}
	al = readyWork(t, a, wid)
	pages, e := a.pageList(context.Background(), al)
	if e != nil {
		t.Fatal(e)
	}
	if pages[0].Path != "front-cover.png" || pages[1].Path != "nested/2.png" {
		t.Fatalf("page order %+v", pages)
	}
	if e = os.Link(filepath.Join(public.Path, al.Path), filepath.Join(private.Path, "Secret-private-name.zip")); e != nil {
		t.Fatal(e)
	}
	if other := registerWork(t, a, private, "Secret-private-name.zip"); other != wid {
		t.Fatal("same physical object was duplicated")
	}
	u := userForRoot(t, a, public.ID)
	items := queryWorks(t, a, u, "path:블루 AND NOT title:Secret")
	if len(items) != 1 || items[0].Title != "[Artist] Work_Name" || items[0].RootID != public.ID {
		t.Fatalf("visible alias mismatch %+v", items)
	}
	if got := queryWorks(t, a, u, "Secret"); len(got) != 0 {
		t.Fatal("private alias search leaked")
	}
	v, e := a.loadPage(context.Background(), u, al, pages[0], Transform{}, false)
	if e != nil || !bytes.Equal(v.Bytes, original) {
		t.Fatalf("original bytes changed: %v", e)
	}
	if _, e = a.DB.Exec("DELETE FROM grants WHERE user_id=?", u.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = a.loadPage(context.Background(), u, al, pages[0], Transform{}, false); e == nil {
		t.Fatal("cached page bypassed revoked grant")
	}
	renamed := "Renamed.zip"
	if e = os.Rename(filepath.Join(public.Path, al.Path), filepath.Join(public.Path, renamed)); e != nil {
		t.Fatal(e)
	}
	moved := registerWork(t, a, public, renamed)
	if moved != wid {
		t.Fatal("rename lost identity")
	}
	fresh := readyWork(t, a, wid)
	if fresh.Version != al.Version || fresh.RegisteredAt != al.RegisteredAt {
		t.Fatal("rename invalidated content or registration time")
	}
	if e = os.Remove(filepath.Join(private.Path, "Secret-private-name.zip")); e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(filepath.Join(public.Path, renamed)); e != nil {
		t.Fatal(e)
	}
	root, e := a.checkSource(context.Background(), fresh)
	if root != nil {
		root.Close()
	}
	if e == nil {
		t.Fatal("offline source served cached originals")
	}
}
func TestMetadataRefreshPreservesManualDecisions(t *testing.T) {
	a, roots := testApp(t)
	root := roots[1]
	writeZIP(t, filepath.Join(root.Path, "Raw_[Name].zip"))
	wid := registerWork(t, a, root, "Raw_[Name].zip")
	al := readyWork(t, a, wid)
	r := ProviderResult{SourceID: "123", Fields: map[string]string{"title": "Remote title", "description": "first"}, Tags: []string{"artist:one", "language:english"}, Confidence: 1, Version: "fixture"}
	if e := a.crawler.commitResult(context.Background(), wid, "nhentai", r, "admin"); e != nil {
		t.Fatal(e)
	}
	_, e := a.DB.Exec("INSERT INTO manual_fields VALUES(?,?,?)", wid, "description", "")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = a.DB.Exec("INSERT INTO tag_decisions VALUES(?,?,-1)", wid, "artist:one"); e != nil {
		t.Fatal(e)
	}
	if _, e = a.DB.Exec("INSERT INTO tag_decisions VALUES(?,?,1)", wid, "artist:manual"); e != nil {
		t.Fatal(e)
	}
	r.Fields["description"] = "refresh"
	r.Tags = []string{"artist:one", "artist:two"}
	if e = a.crawler.commitResult(context.Background(), wid, "nhentai", r, "admin"); e != nil {
		t.Fatal(e)
	}
	m, e := a.crawler.Metadata(context.Background(), User{Admin: true}, wid)
	if e != nil {
		t.Fatal(e)
	}
	fields := m["fields"].(map[string]FieldValue)
	if fields["description"].Value != "" || !fields["description"].Manual || fields["display_title"].Value != "Raw_[Name]" {
		t.Fatalf("manual clearing or filename changed: %+v", fields)
	}
	tags := m["tags"].([]string)
	effective := []string{}
	for _, tag := range tags {
		if strings.HasPrefix(tag, "artist:") || tag == "language:english" {
			effective = append(effective, tag)
		}
	}
	if strings.Join(effective, ",") != "artist:manual,artist:two" {
		t.Fatalf("provider tag membership refreshed incorrectly %v", tags)
	}
	fresh, _ := a.AuthorizedAlias(context.Background(), User{Admin: true}, wid)
	if fresh.Version != al.Version || fresh.IndexStatus != "ready" {
		t.Fatal("metadata refresh invalidated page index")
	}
}
func TestRestartRecoveryAndBootstrapAuthority(t *testing.T) {
	a, roots := testApp(t)
	for _, typ := range []string{"scan", "reconcile", "thumbnail", "crawl_bulk", "stability_bulk", "index_bulk_item", "incremental", "stability", "auto_crawl"} {
		tx, e := a.DB.Begin()
		if e != nil {
			t.Fatal(e)
		}
		jid, e := enqueueTx(tx, typ, roots[1].ID, "", "", "manual", "", nil)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = tx.Exec("UPDATE jobs SET state='running' WHERE id=?", jid); e != nil {
			t.Fatal(e)
		}
		if e = tx.Commit(); e != nil {
			t.Fatal(e)
		}
	}
	cfg := a.Config
	cfg.Roots = []RootConfig{{Key: "public", Name: "Changed YAML", Path: "/unreachable/not-mounted"}, {Key: "unexpected", Name: "Must not be imported", Path: "/never-open"}}
	db, e := OpenDB(cfg)
	if e != nil {
		t.Fatal("restart inspected bootstrap paths", e)
	}
	defer db.Close()
	var rootCount int
	_ = db.QueryRow("SELECT count(*) FROM roots").Scan(&rootCount)
	if rootCount != 2 {
		t.Fatal("restart silently imported configuration")
	}
	var name string
	_ = db.QueryRow("SELECT name FROM roots WHERE bootstrap_key='public'").Scan(&name)
	if name != "Public" {
		t.Fatal("configuration overwrote SQLite state")
	}
	rows, e := db.Query("SELECT type,state FROM jobs")
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	for rows.Next() {
		var typ, state string
		_ = rows.Scan(&typ, &state)
		want := "paused_restart"
		if typ == "incremental" || typ == "stability" || typ == "auto_crawl" {
			want = "queued"
		}
		if state != want {
			t.Fatalf("%s resumed incorrectly: %s", typ, state)
		}
	}
}
func TestAuthSessionCSRFAndNoArchiveDownload(t *testing.T) {
	a, _ := testApp(t)
	if e := BootstrapAdmin(a.DB, "admin", "fixture-password-strong", a.Config.Password); e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(a.Handler())
	defer server.Close()
	body := strings.NewReader(`{"name":"admin","password":"fixture-password-strong"}`)
	req, _ := http.NewRequest("POST", server.URL+"/api/auth/login", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", server.URL)
	res, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	var u User
	_ = json.NewDecoder(res.Body).Decode(&u)
	res.Body.Close()
	if res.StatusCode != 200 || u.CSRF == "" {
		t.Fatal("login failed")
	}
	cookies := res.Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatal("session cookie protection missing")
	}
	req, _ = http.NewRequest("PUT", server.URL+"/api/preferences", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://attacker.example")
	req.AddCookie(cookies[0])
	res, e = http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatal("cross-site session mutation accepted")
	}
	for _, route := range []string{"/api/works/example/download", "/api/unknown", "/download/example"} {
		req, _ = http.NewRequest("GET", server.URL+route, nil)
		req.AddCookie(cookies[0])
		res, e = http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != 404 || strings.Contains(string(b), "<html") {
			t.Fatalf("unexpected download or SPA route: %s %d", route, res.StatusCode)
		}
	}
}
func TestTargetedSourceChangeInvalidatesOnlyItsWork(t *testing.T) {
	a, roots := testApp(t)
	root := roots[1]
	for _, name := range []string{"one.zip", "two.zip"} {
		writeZIP(t, filepath.Join(root.Path, name))
	}
	one := registerWork(t, a, root, "one.zip")
	two := registerWork(t, a, root, "two.zip")
	al := readyWork(t, a, one)
	other := readyWork(t, a, two)
	changed := filepath.Join(root.Path, "one.zip")
	future := time.Now().Add(time.Minute)
	if e := os.Chtimes(changed, future, future); e != nil {
		t.Fatal(e)
	}
	rr, e := a.checkSource(context.Background(), al)
	if rr != nil {
		rr.Close()
	}
	if e == nil {
		t.Fatal("mismatched source revision accepted")
	}
	fresh, _ := a.AuthorizedAlias(context.Background(), User{Admin: true}, one)
	unchanged, _ := a.AuthorizedAlias(context.Background(), User{Admin: true}, two)
	if fresh.Version != al.Version+1 || fresh.IndexStatus != "pending_stability" || fresh.RegisteredAt != al.RegisteredAt {
		t.Fatalf("target revision not stabilized: %+v", fresh)
	}
	if unchanged.Version != other.Version || unchanged.IndexStatus != "ready" {
		t.Fatal("unrelated page index was invalidated")
	}
}
