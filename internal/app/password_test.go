package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPasswordChangeAndGating(t *testing.T) {
	a, _ := testApp(t)
	const original = "original-long-password"
	if err := bootstrapAdmin(a.DB, "admin", original, a.Config.Password, true); err != nil {
		t.Fatal(err)
	}
	var uid string
	if err := a.DB.QueryRow("SELECT id FROM users").Scan(&uid); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"current", "other"} {
		if _, err := a.DB.Exec("INSERT INTO sessions(token,user_id,csrf,expires) VALUES(?,?,?,?)", tokenHash(token), uid, "csrf", now()+60000); err != nil {
			t.Fatal(err)
		}
	}
	handler := a.Handler()
	request := func(method, path, body, token, csrf string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "archika_session", Value: token})
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", csrf)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/api/roots", "/api/works", "/api/admin/users", "/api/works/missing/pages/1", "/api/works/missing/thumbnail"} {
		w := request("GET", path, "", "current", "")
		if w.Code != 403 || !strings.Contains(w.Body.String(), "password_change_required") {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
	}
	if w := request("PUT", "/api/preferences", "{}", "current", "csrf"); w.Code != 403 || !strings.Contains(w.Body.String(), "password_change_required") {
		t.Fatalf("mutation gating: %d %s", w.Code, w.Body)
	}
	if w := request("HEAD", "/api/auth/me", "", "current", ""); w.Code != 403 {
		t.Fatalf("HEAD gating: %d", w.Code)
	}
	if w := request("GET", "/api/auth/me", "", "current", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"must_change_password":true`) {
		t.Fatalf("me: %d %s", w.Code, w.Body)
	}
	if w := request("POST", "/api/auth/password", "{}", "current", ""); w.Code != 403 || !strings.Contains(w.Body.String(), "csrf") {
		t.Fatalf("csrf: %d %s", w.Code, w.Body)
	}
	for _, tc := range []struct {
		current, next, code string
		status              int
	}{
		{"wrong", "replacement-long-password", "invalid_credentials", 400},
		{original, "short", "password_policy", 400},
		{original, strings.Repeat("x", 1025), "password_policy", 400},
		{original, original, "password_policy", 400},
		{original, "replacement-long-password", "", 200},
	} {
		body, _ := json.Marshal(map[string]string{"current_password": tc.current, "new_password": tc.next})
		w := request("POST", "/api/auth/password", string(body), "current", "csrf")
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.code) {
			t.Fatalf("change: %d %s", w.Code, w.Body)
		}
	}
	var hash string
	var flag bool
	if err := a.DB.QueryRow("SELECT password,must_change_password FROM users").Scan(&hash, &flag); err != nil {
		t.Fatal(err)
	}
	if flag || !checkPassword(hash, "replacement-long-password", a.Config.Password) {
		t.Fatal("password not updated")
	}
	if w := request("GET", "/api/roots", "", "current", ""); w.Code != 200 {
		t.Fatalf("current session: %d %s", w.Code, w.Body)
	}
	if w := request("GET", "/api/auth/me", "", "other", ""); w.Code != 401 {
		t.Fatal("other session survived")
	}
	for range 7 {
		request("POST", "/api/auth/password", `{"current_password":"wrong","new_password":"long-password-value"}`, "current", "csrf")
	}
	if w := request("POST", "/api/auth/login", `{"name":"admin","password":"replacement-long-password"}`, "", ""); w.Code != 429 {
		t.Fatalf("limiter not shared: %d", w.Code)
	}
	if _, err := a.DB.Exec("UPDATE users SET must_change_password=1"); err != nil {
		t.Fatal(err)
	}
	if w := request("POST", "/api/auth/logout", "{}", "current", "csrf"); w.Code != 204 && w.Code != 200 {
		t.Fatalf("logout gated: %d", w.Code)
	}
}
