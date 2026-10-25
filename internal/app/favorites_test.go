package app

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/text/unicode/norm"
)

func TestFavoritesArePrivateAndUnicodeNormalizationIsSearchOnly(t *testing.T) {
	a, roots := testApp(t)
	root := roots[1]
	name := norm.NFD.String("블루") + " AND.zip"
	writeZIP(t, filepath.Join(root.Path, name))
	work := registerWork(t, a, root, name)
	first := userForRoot(t, a, root.ID)
	second := User{ID: id(), Name: "second"}
	if _, e := a.DB.Exec("INSERT INTO users(id,name,password) VALUES(?,?,?)", second.ID, second.Name, "not used for this authorization scenario"); e != nil {
		t.Fatal(e)
	}
	if _, e := a.DB.Exec("INSERT INTO grants VALUES(?,?)", second.ID, root.ID); e != nil {
		t.Fatal(e)
	}
	request := httptest.NewRequest("PUT", "/api/works/"+work+"/favorite", strings.NewReader(`{"favorite":true}`))
	request.Header.Set("Content-Type", "application/json")
	request.SetPathValue("id", work)
	response := httptest.NewRecorder()
	a.favoriteHTTP(response, request, first)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	items := queryWorks(t, a, first, `favorite:true AND 블루 AND "AND"`)
	if len(items) != 1 || !items[0].Favorite || items[0].Title != strings.TrimSuffix(name, ".zip") {
		t.Fatalf("favorite or preserved display title: %+v", items)
	}
	if len(queryWorks(t, a, second, "favorite:true")) != 0 {
		t.Fatal("favorites leaked across users")
	}
	if _, e := a.DB.Exec("DELETE FROM grants WHERE user_id=?", first.ID); e != nil {
		t.Fatal(e)
	}
	if len(queryWorks(t, a, first, "favorite:true")) != 0 {
		t.Fatal("favorite bypassed revoked root grant")
	}
}
