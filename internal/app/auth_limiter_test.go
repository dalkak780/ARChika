package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLoginAttemptsAtCapacity(t *testing.T) {
	a, _ := testApp(t)
	a.Config.Password = PasswordPolicy{MemoryKiB: 19 * 1024, Iterations: 2, Parallelism: 1, MaxMemoryKiB: 65536}
	if e := BootstrapAdmin(a.DB, "admin", "fixture-password-strong", a.Config.Password); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		name     string
		size     int
		existing bool
		expired  bool
	}{
		{name: "full new host", size: 10000},
		{name: "over capacity new host", size: 10001},
		{name: "full existing host", size: 10000, existing: true},
		{name: "over capacity existing host", size: 10001, existing: true},
		{name: "expired entry", size: 10000, expired: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at := time.Now()
			a.authMu.Lock()
			a.loginAttempts = make(map[string]loginAttempt)
			for i := range tc.size {
				a.loginAttempts[fmt.Sprintf("198.18.%d.%d", i/256, i%256)] = loginAttempt{Count: 1, At: at}
			}
			a.loginAttempts["198.18.0.0"] = loginAttempt{Count: 1, At: at.Add(-30 * time.Second)}
			a.loginAttempts["198.18.0.1"] = loginAttempt{Count: 9, At: at}
			if tc.expired {
				a.loginAttempts["198.18.0.2"] = loginAttempt{Count: 10, At: at.Add(-2 * time.Minute)}
			}
			a.authMu.Unlock()
			host := "203.0.113.1"
			if tc.existing {
				host = "198.18.0.1"
			}
			login := func() *httptest.ResponseRecorder {
				r := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"name":"admin","password":"fixture-password-strong"}`))
				r.Header.Set("Content-Type", "application/json")
				r.RemoteAddr = host + ":12345"
				w := httptest.NewRecorder()
				a.login(w, r)
				return w
			}
			if w := login(); w.Code != http.StatusOK {
				t.Fatalf("login at capacity: status %d, body %s", w.Code, w.Body.String())
			}
			a.authMu.Lock()
			size := len(a.loginAttempts)
			_, oldestKept := a.loginAttempts["198.18.0.0"]
			_, expiredKept := a.loginAttempts["198.18.0.2"]
			a.authMu.Unlock()
			if !tc.existing && size > 10000 {
				t.Fatalf("new host grew limiter to %d entries", size)
			}
			if !tc.existing && !tc.expired && oldestKept {
				t.Fatal("oldest entry was not evicted")
			}
			if tc.expired && (expiredKept || !oldestKept) {
				t.Fatal("expired entries must be pruned before active entries are evicted")
			}
			if tc.existing {
				if w := login(); w.Code != http.StatusTooManyRequests {
					t.Fatalf("eleventh attempt: status %d, body %s", w.Code, w.Body.String())
				}
			}
		})
	}
}

func TestLoginAttemptLimits(t *testing.T) {
	a, _ := testApp(t)
	a.Config.Password = PasswordPolicy{MemoryKiB: 19 * 1024, Iterations: 2, Parallelism: 1, MaxMemoryKiB: 65536}
	for _, tc := range []struct {
		name    string
		address func(int) string
		other   string
	}{
		{name: "IPv4", address: func(i int) string { return fmt.Sprintf("192.0.2.1:%d", 10000+i) }, other: "192.0.2.2:12345"},
		{name: "IPv6 prefix", address: func(i int) string { return fmt.Sprintf("[2001:db8:1:2::%x]:12345", i+1) }, other: "[2001:db8:1:3::1]:12345"},
		{name: "mapped IPv4", address: func(i int) string {
			if i%2 == 0 {
				return "192.0.2.1:12345"
			}
			return "[::ffff:192.0.2.1]:12345"
		}, other: "192.0.2.2:12345"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a.authMu.Lock()
			a.loginAttempts = make(map[string]loginAttempt)
			a.authMu.Unlock()
			login := func(address string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"name":"missing","password":"wrong"}`))
				r.Header.Set("Content-Type", "application/json")
				r.RemoteAddr = address
				w := httptest.NewRecorder()
				a.login(w, r)
				return w
			}
			for i := range 11 {
				want := http.StatusUnauthorized
				if i == 10 {
					want = http.StatusTooManyRequests
				}
				if w := login(tc.address(i)); w.Code != want {
					t.Fatalf("attempt %d: status %d, want %d, body %s", i+1, w.Code, want, w.Body.String())
				}
			}
			if w := login(tc.other); w.Code != http.StatusUnauthorized {
				t.Fatalf("unrelated host: status %d, body %s", w.Code, w.Body.String())
			}
			a.authMu.Lock()
			for host, v := range a.loginAttempts {
				v.At = time.Now().Add(-2 * time.Minute)
				a.loginAttempts[host] = v
			}
			a.authMu.Unlock()
			if w := login(tc.address(0)); w.Code != http.StatusUnauthorized {
				t.Fatalf("expired window: status %d, body %s", w.Code, w.Body.String())
			}
		})
	}
}
