package app

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestShortMultilingualTermsAndBooleanPrecedence(t *testing.T) {
	a, roots := testApp(t)
	root := roots[1]
	for _, rel := range []string{"작가/블루 東方 AI.zip", "other/아블루 패스.zip", "other/Book 10.zip"} {
		writeZIP(t, filepath.Join(root.Path, rel))
		registerWork(t, a, root, rel)
	}
	u := userForRoot(t, a, root.ID)
	for _, tc := range []struct {
		q    string
		want int
	}{{"블루", 1}, {"루", 0}, {"東方", 1}, {"AI", 1}, {"path:작가", 1}, {"블루 OR title:Book AND NOT 패스", 2}, {"(블루 OR title:Book) AND NOT path:작가", 1}, {"title:아블루", 1}, {"path:가", 0}} {
		if got := len(queryWorks(t, a, u, tc.q)); got != tc.want {
			t.Errorf("%q got %d want %d", tc.q, got, tc.want)
		}
	}
	for _, q := range []string{"AND", "a OR", "(a", "NOT", "a )", strings.Repeat("(", 17) + "test" + strings.Repeat(")", 17)} {
		if _, e := parseQuery(q); e == nil {
			t.Errorf("malformed query accepted %q", q)
		}
	}
}
func TestSearchScale(t *testing.T) {
	raw := os.Getenv("ARCHIKA_SCALE")
	if raw == "" {
		t.Skip("set ARCHIKA_SCALE=100000 for catalog benchmark")
	}
	n, e := strconv.Atoi(raw)
	if e != nil || n < 1000 || n > 1000000 {
		t.Fatal("scale must be 1000..1000000")
	}
	a, roots := testApp(t)
	root, private := roots[1], roots[0]
	started := time.Now()
	tx, e := a.DB.Begin()
	if e != nil {
		t.Fatal(e)
	}
	work, e := tx.Prepare("INSERT INTO works(id,kind,size,mtime,registered_at,index_status) VALUES(?,'zip',1,1,1000,'ready')")
	if e != nil {
		t.Fatal(e)
	}
	alias, e := tx.Prepare("INSERT INTO aliases(work_id,root_id,path,original_name,title,title_sort,parent,size,mtime,filename_tags) VALUES(?,?,?,?,?,?,?,1,1,'[\"artist:test\"]')")
	if e != nil {
		t.Fatal(e)
	}
	for i := range n {
		wid := fmt.Sprintf("%032x", i)
		rid := root.ID
		if i%10 == 0 {
			rid = private.ID
		}
		title := fmt.Sprintf("Book %d 블루 東方 AI", i)
		if _, e = work.Exec(wid); e != nil {
			t.Fatal(e)
		}
		if _, e = alias.Exec(wid, rid, fmt.Sprintf("작가/series/%d.zip", i), title+".zip", title, naturalKey(title), "작가/series"); e != nil {
			t.Fatal(e)
		}
	}
	work.Close()
	alias.Close()
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	insertTime := time.Since(started)
	u := userForRoot(t, a, root.ID)
	t.Logf("catalog=%d insertion=%s", n, insertTime)
	for _, q := range []string{"블루", "東方", "AI", "substring:Book", "path:작가", "tag:artist:test"} {
		started = time.Now()
		r := httptest.NewRequest("GET", "/api/works?q="+url.QueryEscape(q)+"&limit=7", nil)
		w := httptest.NewRecorder()
		a.works(w, r, u)
		elapsed := time.Since(started)
		if w.Code != 200 {
			t.Fatalf("scale search %q: %s", q, w.Body)
		}
		var result struct {
			Total      int
			Items      []Alias
			NextCursor string `json:"next_cursor"`
		}
		if e = json.Unmarshal(w.Body.Bytes(), &result); e != nil {
			t.Fatal(e)
		}
		if result.Total != n-n/10 || len(result.Items) != 7 {
			t.Fatalf("scale ACL/count q=%s total=%d items=%d", q, result.Total, len(result.Items))
		}
		seen := map[string]bool{}
		for _, v := range result.Items {
			seen[v.WorkID] = true
		}
		next := httptest.NewRecorder()
		a.works(next, httptest.NewRequest("GET", "/api/works?q="+url.QueryEscape(q)+"&limit=7&cursor="+result.NextCursor, nil), u)
		if next.Code != 200 {
			t.Fatalf("cursor: %s", next.Body)
		}
		var second struct{ Items []Alias }
		_ = json.Unmarshal(next.Body.Bytes(), &second)
		for _, v := range second.Items {
			if seen[v.WorkID] {
				t.Fatal("equal-sort cursor duplicated an item")
			}
		}
		t.Logf("q=%s first-page=%s count=%d", q, elapsed, result.Total)
	}
	for _, table := range []string{"alias_words", "alias_substrings"} {
		rows, e := a.DB.Query("EXPLAIN QUERY PLAN SELECT rowid FROM "+table+" WHERE "+table+" MATCH ?", "\"블루\"*")
		if e != nil {
			t.Fatal(e)
		}
		for rows.Next() {
			var id, parent, unused int
			var detail string
			_ = rows.Scan(&id, &parent, &unused, &detail)
			t.Log(table, detail)
		}
		rows.Close()
	}
	_, _ = a.DB.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	if st, e := os.Stat(filepath.Join(a.Config.DataDir, "archika.sqlite")); e == nil {
		t.Logf("database_bytes=%d", st.Size())
	}
}
