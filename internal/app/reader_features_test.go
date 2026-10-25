package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func TestPageThumbnailHTTP(t *testing.T) {
	a, roots := testApp(t)
	writeZIP(t, filepath.Join(roots[0].Path, "book.cbz"))
	al := readyWork(t, a, registerWork(t, a, roots[0], "book.cbz"))
	p, err := a.page(context.Background(), al, 0)
	if err != nil {
		t.Fatal(err)
	}
	var source bytes.Buffer
	if err := png.Encode(&source, image.NewRGBA(image.Rect(0, 0, 400, 1200))); err != nil {
		t.Fatal(err)
	}
	if _, err := a.cache.Load(context.Background(), fmt.Sprintf("original-page/%s/%d/%s", al.WorkID, al.Version, p.ID), false, func(context.Context) (PageValue, error) {
		return PageValue{Bytes: source.Bytes(), MIME: "image/png"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	u := userForRoot(t, a, roots[0].ID)
	request := func(page, etag string, user User) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/works/"+al.WorkID+"/pages/"+page+"/thumbnail", nil)
		r.SetPathValue("id", al.WorkID)
		r.SetPathValue("page", page)
		r.Header.Set("If-None-Match", etag)
		w := httptest.NewRecorder()
		a.pageThumbnailHTTP(w, r, user)
		return w
	}
	w := request("0", "", u)
	if w.Code != 200 {
		t.Fatalf("thumbnail: %d %s", w.Code, w.Body)
	}
	if w.Header().Get("Content-Type") != "image/png" || w.Header().Get("Cache-Control") != "private, max-age=86400" {
		t.Fatal(w.Header())
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(w.Body.Bytes()))
	if err != nil || cfg.Width != 100 || cfg.Height != 300 {
		t.Fatalf("dimensions: %+v %v", cfg, err)
	}
	a.cache.mu.Lock()
	diskEntries := len(a.cache.disk)
	a.cache.mu.Unlock()
	if diskEntries == 0 {
		t.Fatal("thumbnail was not admitted to disk")
	}
	etag := w.Header().Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag")
	}
	for _, match := range []string{etag, "W/" + etag, "\"other\", " + etag, "*"} {
		if got := request("0", match, u); got.Code != 304 || got.Body.Len() != 0 {
			t.Fatalf("conditional: %d %s", got.Code, got.Body)
		}
	}
	for _, page := range []string{"-1", "bad", "9999"} {
		if got := request(page, "", u); got.Code != 404 {
			t.Fatalf("page %s: %d", page, got.Code)
		}
	}
	if got := request("0", etag, User{ID: "no-access"}); got.Code != 404 {
		t.Fatalf("ACL: %d", got.Code)
	}
	for i := 0; i < cap(a.thumbnailQueue); i++ {
		a.thumbnailQueue <- struct{}{}
	}
	w = request("0", "", u)
	for i := 0; i < cap(a.thumbnailQueue); i++ {
		<-a.thumbnailQueue
	}
	if w.Code != 503 || w.Header().Get("Retry-After") != "2" || !strings.Contains(w.Body.String(), `"busy"`) {
		t.Fatalf("busy: %d %s", w.Code, w.Body)
	}
	var n int
	if err := a.DB.QueryRow("SELECT count(*) FROM thumbnails").Scan(&n); err != nil || n != 0 {
		t.Fatalf("cover rows: %d %v", n, err)
	}
}

func TestPageThumbnailFailures(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprint(corrupt), func(t *testing.T) {
			a, roots := testApp(t)
			writeZIP(t, filepath.Join(roots[0].Path, "book.cbz"))
			al := readyWork(t, a, registerWork(t, a, roots[0], "book.cbz"))
			p, err := a.page(context.Background(), al, 0)
			if err != nil {
				t.Fatal(err)
			}
			_, err = a.cache.Load(context.Background(), fmt.Sprintf("original-page/%s/%d/%s", al.WorkID, al.Version, p.ID), false, func(context.Context) (PageValue, error) {
				if corrupt {
					return PageValue{Bytes: []byte("bad"), MIME: "image/png"}, nil
				}
				return PageValue{}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if !corrupt {
				a.transform <- struct{}{}
				defer func() { <-a.transform }()
			}
			r := httptest.NewRequest("GET", "/", nil)
			r.SetPathValue("id", al.WorkID)
			r.SetPathValue("page", "0")
			w := httptest.NewRecorder()
			a.pageThumbnailHTTP(w, r, User{Admin: true})
			want, code := 503, "busy"
			if corrupt {
				want, code = 415, "unsupported"
			}
			if w.Code != want || !strings.Contains(w.Body.String(), `"`+code+`"`) {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			var n int
			if err := a.DB.QueryRow("SELECT count(*) FROM thumbnails").Scan(&n); err != nil || n != 0 {
				t.Fatalf("persisted failure: %d %v", n, err)
			}
		})
	}
}

func TestNeighborsMatchListing(t *testing.T) {
	a, roots := testApp(t)
	var ids []string
	for i, name := range []string{"one/Book.zip", "two/Book.zip", "Other.zip", "Last.zip"} {
		writeZIP(t, filepath.Join(roots[0].Path, name))
		al := registerWork(t, a, roots[0], name)
		ids = append(ids, al)
		if _, err := a.DB.Exec("UPDATE works SET registered_at=?,modified_at=?,mtime=? WHERE id=?", 10+i/2, i/2, 3, al); err != nil {
			t.Fatal(err)
		}
	}
	writeZIP(t, filepath.Join(roots[1].Path, "Private.zip"))
	private := registerWork(t, a, roots[1], "Private.zip")
	ids = append(ids, private, "missing")
	u := userForRoot(t, a, roots[0].ID)
	for _, sort := range []string{"recent", "title", "mtime"} {
		for _, direction := range []string{"asc", "desc"} {
			for _, filter := range []string{"", "&q=" + url.QueryEscape("title:Book"), "&root=" + roots[1].ID, "&root=" + roots[0].ID} {
				query := "?sort=" + sort + "&direction=" + direction + filter
				t.Run(query, func(t *testing.T) {
					w := httptest.NewRecorder()
					a.works(w, httptest.NewRequest("GET", "/api/works"+query, nil), u)
					if w.Code != 200 {
						t.Fatal(w.Body)
					}
					var listing struct{ Items []Alias }
					if err := json.Unmarshal(w.Body.Bytes(), &listing); err != nil {
						t.Fatal(err)
					}
					for _, id := range ids {
						r := httptest.NewRequest("GET", "/api/works/"+id+"/neighbors"+query, nil)
						r.SetPathValue("id", id)
						w := httptest.NewRecorder()
						a.neighbors(w, r, u)
						if w.Code != 200 {
							t.Fatalf("%d %s", w.Code, w.Body)
						}
						var got struct {
							Previous *neighborWork
							Next     *neighborWork
						}
						if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
							t.Fatal(err)
						}
						var previous, next *neighborWork
						for i, item := range listing.Items {
							if item.WorkID != id {
								continue
							}
							if i > 0 {
								v := listing.Items[i-1]
								previous = &neighborWork{v.WorkID, v.Title}
							}
							if i+1 < len(listing.Items) {
								v := listing.Items[i+1]
								next = &neighborWork{v.WorkID, v.Title}
							}
						}
						same := func(a, b *neighborWork) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
						if !same(got.Previous, previous) || !same(got.Next, next) {
							t.Fatalf("id=%s got=%+v want previous=%+v next=%+v", id, got, previous, next)
						}
					}
				})
			}
		}
	}
}

func TestReaderPreferences(t *testing.T) {
	a, roots := testApp(t)
	u := userForRoot(t, a, roots[0].ID)
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"fit":"height","reading_direction":"rtl","preload_count":0,"auto_next_seconds":2}`, 200},
		{`{"fit":"viewport","reading_direction":"ltr","preload_count":10,"auto_next_seconds":120}`, 200},
		{`{"fit":"width"}`, 200}, {`{"fit":"original"}`, 200},
		{`{"reading_direction":"up"}`, 400}, {`{"fit":"bad"}`, 400},
		{`{"preload_count":1.5}`, 400}, {`{"preload_count":-1}`, 400}, {`{"preload_count":11}`, 400},
		{`{"auto_next_seconds":1.5}`, 400}, {`{"auto_next_seconds":1}`, 400}, {`{"auto_next_seconds":121}`, 400},
		{`{"auto_next_seconds":"2"}`, 400}, {`{"preload_count":null}`, 400}, {`{"unknown":1}`, 400},
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("PUT", "/", strings.NewReader(tc.body))
		r.Header.Set("Content-Type", "application/json")
		a.preferences(w, r, u)
		if w.Code != tc.status {
			t.Errorf("%s: %d %s", tc.body, w.Code, w.Body)
		}
	}
}
