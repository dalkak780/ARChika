package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"testing"
)

func TestFolderBrowsing(t *testing.T) {
	a, roots := testApp(t)
	root := roots[0]
	u := userForRoot(t, a, root.ID)
	ids := map[string]string{}
	for _, name := range []string{"Loose.zip", "Series A/Vol 1.zip", "Series A/Vol 2.zip", "Series A/Vol 10.zip", "Series A/Extras/Bonus.zip", "Series B/Other.zip", "Vol 10/Book.zip", "Vol 2/Book.zip", "100%_/Child/Book.zip", "100xx/Child/Book.zip", "é/Book.zip", "é/Book.zip"} {
		writeZIP(t, filepath.Join(root.Path, name))
		ids[name] = registerWork(t, a, root, name)
	}
	// One work can appear in multiple directories.
	if _, e := a.DB.Exec(`INSERT INTO aliases(work_id,root_id,path,original_name,title,title_sort,parent,size,mtime) VALUES(?,?,?,?,?,?,?,1,1)`, ids["Series A/Vol 1.zip"], root.ID, "Series A/Extras/Copy.zip", "Copy.zip", "Copy", naturalKey("Copy"), "Series A/Extras"); e != nil {
		t.Fatal(e)
	}
	get := func(handler func(http.ResponseWriter, *http.Request, User), query string, user User) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest("GET", "/?"+query, nil), user)
		return w
	}
	listing := func(p string) folderListing {
		t.Helper()
		w := get(a.folders, "root="+root.ID+"&path="+url.QueryEscape(p), u)
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
		var out folderListing
		if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
		return out
	}
	top := listing("")
	if top.Works != 1 || top.RootID != root.ID || top.RootName != root.Name || top.Path != "" {
		t.Fatalf("top: %+v", top)
	}
	var names []string
	for _, f := range top.Folders {
		names = append(names, f.Name)
		if f.CoverWorkID == "" {
			t.Fatal("missing cover", f)
		}
		if f.Name == "Series A" && f.Works != 4 {
			t.Fatalf("recursive distinct count: %+v", f)
		}
	}
	want := []string{"100%_", "100xx", "é", "Series A", "Series B", "Vol 2", "Vol 10", "é"}
	// Compare natural order independently of the other names.
	two, ten := -1, -1
	for i, n := range names {
		if n == "Vol 2" {
			two = i
		}
		if n == "Vol 10" {
			ten = i
		}
	}
	if len(names) != len(want) || two < 0 || ten < two {
		t.Fatalf("folders: %v", names)
	}
	series := listing("Series A")
	if series.Works != 3 || len(series.Folders) != 1 || series.Folders[0].Path != "Series A/Extras" || series.Folders[0].Works != 2 {
		t.Fatalf("series: %+v", series)
	}
	literal := listing("100%_")
	if literal.Works != 0 || len(literal.Folders) != 1 || literal.Folders[0].Works != 1 {
		t.Fatalf("literal prefix: %+v", literal)
	}
	for _, p := range []string{"é", "é"} {
		if listing(p).Works != 1 {
			t.Fatal("unicode path", p)
		}
	}
	for _, p := range []string{"/a", "a/", "a//b", ".", "..", "a/./b", "a/../b", "a\x00b", "a\\b"} {
		for _, handler := range []func(http.ResponseWriter, *http.Request, User){a.folders, a.works, a.neighbors} {
			w := get(handler, "root="+root.ID+"&path="+url.QueryEscape(p)+"&folder="+url.QueryEscape(p), u)
			if w.Code != 400 {
				t.Fatalf("invalid %q: %d %s", p, w.Code, w.Body)
			}
		}
	}
	for _, rid := range []string{roots[1].ID, "missing", ""} {
		if w := get(a.folders, "root="+rid, u); w.Code != 404 {
			t.Fatalf("hidden root: %d %s", w.Code, w.Body)
		}
	}
	for _, handler := range []func(http.ResponseWriter, *http.Request, User){a.works, a.neighbors} {
		if w := get(handler, "folder=", u); w.Code != 400 {
			t.Fatalf("missing root: %d %s", w.Code, w.Body)
		}
	}
	works := func(query string) (int, []Alias, string) {
		t.Helper()
		w := get(a.works, "root="+root.ID+"&sort=title&direction=asc&"+query, u)
		if w.Code != 200 {
			t.Fatalf("works: %d %s", w.Code, w.Body)
		}
		var out struct {
			Total  int
			Items  []Alias
			Cursor string `json:"next_cursor"`
		}
		if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
		return out.Total, out.Items, out.Cursor
	}
	total, items, _ := works("folder=Series+A")
	var titles []string
	for _, item := range items {
		titles = append(titles, item.Title)
	}
	if total != 3 || !reflect.DeepEqual(titles, []string{"Vol 1", "Vol 2", "Vol 10"}) {
		t.Fatalf("direct: %d %v", total, titles)
	}
	total, items, _ = works("folder=")
	if total != 1 || items[0].WorkID != ids["Loose.zip"] {
		t.Fatalf("root direct: %d %v", total, items)
	}
	total, _, _ = works("")
	if total != len(ids) {
		t.Fatalf("unfiltered: %d", total)
	}
	_, _, cursor := works("folder=Series+A&limit=1")
	if cursor == "" {
		t.Fatal("missing cursor")
	}
	for _, suffix := range []string{"&folder=Series+B", "&folder=", ""} {
		w := get(a.works, "root="+root.ID+"&sort=title&direction=asc&limit=1&cursor="+cursor+suffix, u)
		if w.Code != 400 {
			t.Fatalf("cursor scope: %d %s", w.Code, w.Body)
		}
	}
	r := httptest.NewRequest("GET", "/?root="+root.ID+"&folder=Series+A&sort=title&direction=asc", nil)
	r.SetPathValue("id", ids["Series A/Vol 2.zip"])
	w := httptest.NewRecorder()
	a.neighbors(w, r, u)
	var neighbors struct{ Previous, Next *neighborWork }
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	if e := json.Unmarshal(w.Body.Bytes(), &neighbors); e != nil {
		t.Fatal(e)
	}
	if neighbors.Previous == nil || neighbors.Next == nil || neighbors.Previous.ID != ids["Series A/Vol 1.zip"] || neighbors.Next.ID != ids["Series A/Vol 10.zip"] {
		t.Fatalf("neighbors: %+v", neighbors)
	}
	if _, e := a.DB.Exec("UPDATE roots SET status='offline' WHERE id=?", root.ID); e != nil {
		t.Fatal(e)
	}
	if w := get(a.folders, "root="+root.ID, User{Admin: true}); w.Code != 404 {
		t.Fatalf("offline: %d %s", w.Code, w.Body)
	}
}
