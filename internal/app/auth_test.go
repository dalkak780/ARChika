package app

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPasswordPolicySaltAndVerificationBounds(t *testing.T) {
	policy := PasswordPolicy{MemoryKiB: 19 * 1024, Iterations: 2, Parallelism: 1, MaxMemoryKiB: 65536}
	password := "a-long-unique-fixture-password"
	first, e := HashPassword(password, policy)
	if e != nil {
		t.Fatal(e)
	}
	second, e := HashPassword(password, policy)
	if e != nil {
		t.Fatal(e)
	}
	if first == second {
		t.Fatal("password hashes reused a salt")
	}
	if !checkPassword(first, password, policy) || checkPassword(first, "wrong password", policy) {
		t.Fatal("Argon2id password verification failed")
	}
	hostile := strings.Replace(first, "m=19456", "m=4294967295", 1)
	if checkPassword(hostile, password, policy) {
		t.Fatal("unbounded KDF parameters accepted")
	}
	if _, e = HashPassword("short", policy); e == nil {
		t.Fatal("weak password accepted")
	}
	weak := policy
	weak.MemoryKiB = 8
	if _, e = HashPassword(password, weak); e == nil {
		t.Fatal("unsafe Argon2 resource policy accepted")
	}
}

func TestLanguagePreferences(t *testing.T) {
	a, roots := testApp(t)
	u := userForRoot(t, a, roots[0].ID)
	for _, value := range []string{`"en"`, `"ko"`, `""`, `"ja"`, `"ko-KR"`, `null`, `1`, `true`, `[]`, `{}`} {
		t.Run(value, func(t *testing.T) {
			request := httptest.NewRequest("PUT", "/api/preferences", strings.NewReader(`{"language":`+value+`,"layout":"spread"}`))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			a.preferences(response, request, u)
			valid := value == `"en"` || value == `"ko"` || value == `""`
			if !valid {
				if response.Code != 400 || !strings.Contains(response.Body.String(), `"code":"invalid_preference"`) || !strings.Contains(response.Body.String(), `"message":"Invalid language"`) {
					t.Fatalf("invalid language: %d %s", response.Code, response.Body)
				}
				return
			}
			if response.Code != 200 {
				t.Fatalf("save language: %d %s", response.Code, response.Body)
			}
			var stored string
			if err := a.DB.QueryRow("SELECT preferences FROM users WHERE id=?", u.ID).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			var preferences map[string]string
			if err := json.Unmarshal([]byte(stored), &preferences); err != nil {
				t.Fatal(err)
			}
			if preferences["language"] != strings.Trim(value, `"`) || preferences["layout"] != "spread" {
				t.Fatalf("stored preferences: %s", stored)
			}
		})
	}
}

func TestAccountPageReload(t *testing.T) {
	a, _ := testApp(t)
	response := httptest.NewRecorder()
	a.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/account", nil))
	if response.Code != 200 || !strings.Contains(response.Header().Get("Content-Type"), "text/html") || !strings.Contains(response.Body.String(), `id="root"`) {
		t.Fatalf("account SPA: %d %s", response.Code, response.Body)
	}
}
