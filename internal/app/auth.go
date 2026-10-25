package app

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

var passwordSlots = make(chan struct{}, 2)
var errAuthBusy = errors.New("password hashing budget exhausted; retry later")

type PasswordPolicy struct {
	MemoryKiB    uint32 `json:"memory_kib"`
	Iterations   uint32 `json:"iterations"`
	Parallelism  uint8  `json:"parallelism"`
	MaxMemoryKiB uint32 `json:"max_memory_kib"`
}

func defaultPasswordPolicy() PasswordPolicy {
	return PasswordPolicy{MemoryKiB: 65536, Iterations: 3, Parallelism: 2, MaxMemoryKiB: 262144}
}
func (p PasswordPolicy) valid() bool {
	return p.MemoryKiB >= 19*1024 && p.MemoryKiB <= p.MaxMemoryKiB && p.MaxMemoryKiB <= 262144 && p.Iterations >= 2 && p.Iterations <= 10 && p.Parallelism >= 1 && p.Parallelism <= 4
}

func HashPassword(password string, policy PasswordPolicy) (string, error) {
	if !policy.valid() {
		return "", errors.New("invalid Argon2id resource policy")
	}
	if len(password) < 12 || len(password) > 1024 {
		return "", errors.New("password must be 12–1024 bytes")
	}
	select {
	case passwordSlots <- struct{}{}:
	default:
		return "", errAuthBusy
	}
	defer func() { <-passwordSlots }()
	salt := make([]byte, 16)
	if _, e := rand.Read(salt); e != nil {
		return "", e
	}
	h := argon2.IDKey([]byte(password), salt, policy.Iterations, policy.MemoryKiB, policy.Parallelism, 32)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", policy.MemoryKiB, policy.Iterations, policy.Parallelism, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(h)), nil
}
func checkPassword(encoded, password string, policy PasswordPolicy) bool {
	if len(password) > 1024 {
		return false
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false
	}
	var memory, iterations uint32
	var parallelism uint8
	n, e := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism)
	if e != nil || n != 3 || memory < 19*1024 || memory > policy.MaxMemoryKiB || iterations < 2 || iterations > 10 || parallelism < 1 || parallelism > 4 || parts[3] != fmt.Sprintf("m=%d,t=%d,p=%d", memory, iterations, parallelism) {
		return false
	}
	salt, e := base64.RawStdEncoding.DecodeString(parts[4])
	if e != nil || len(salt) != 16 {
		return false
	}
	want, e := base64.RawStdEncoding.DecodeString(parts[5])
	if e != nil || len(want) != 32 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, 32)
	return subtle.ConstantTimeCompare(got, want) == 1
}

var errAccountsExist = errors.New("bootstrap refused: accounts already exist; use authenticated administration")

func BootstrapAdmin(db *sql.DB, name, password string, policy PasswordPolicy) error {
	return bootstrapAdmin(db, name, password, policy, false)
}
func bootstrapAdmin(db *sql.DB, name, password string, policy PasswordPolicy, mustChange bool) error {
	if !validName(name) {
		return errors.New("account name must be 1–80 non-control characters")
	}
	hash, e := HashPassword(password, policy)
	if e != nil {
		return e
	}
	tx, e := db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var n int
	if e = tx.QueryRow("SELECT count(*) FROM users").Scan(&n); e != nil {
		return e
	}
	if n != 0 {
		return errAccountsExist
	}
	_, e = tx.Exec("INSERT INTO users(id,name,password,admin,must_change_password) VALUES(?,?,?,1,?)", id(), name, hash, mustChange)
	if e != nil {
		return e
	}
	return tx.Commit()
}
func validName(s string) bool {
	if len(s) == 0 || len(s) > 80 {
		return false
	}
	for _, c := range s {
		if c < 32 || c == 127 {
			return false
		}
	}
	return strings.TrimSpace(s) == s
}
func tokenHash(s string) string { b := sha256.Sum256([]byte(s)); return hex.EncodeToString(b[:]) }
func (a *App) User(r *http.Request) (User, error) {
	var u User
	cookie, e := r.Cookie("archika_session")
	if e != nil {
		return u, errors.New("sign in required")
	}
	var prefs string
	e = a.DB.QueryRowContext(r.Context(), "SELECT u.id,u.name,u.admin,s.csrf,u.preferences,u.must_change_password FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token=? AND s.expires>? AND u.disabled=0", tokenHash(cookie.Value), now()).Scan(&u.ID, &u.Name, &u.Admin, &u.CSRF, &prefs, &u.MustChangePassword)
	if e != nil {
		return u, errors.New("sign in required")
	}
	_ = json.Unmarshal([]byte(prefs), &u.Preferences)
	return u, nil
}
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, e := url.Parse(origin)
	return e == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host == r.Host && u.Path == ""
}
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return errors.New("application/json required")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return errors.New("invalid JSON request")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("request must contain one JSON value")
	}
	return nil
}
func jsonEncoder(w io.Writer, v any) error { return json.NewEncoder(w).Encode(v) }
func apiError(w http.ResponseWriter, status int, code, message string) {
	jsonResponse(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
func (a *App) authenticated(admin bool, fn func(http.ResponseWriter, *http.Request, User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, e := a.User(r)
		if e != nil {
			apiError(w, 401, "unauthorized", "Sign in required")
			return
		}
		if u.MustChangePassword && !(r.Method == "GET" && r.URL.Path == "/api/auth/me" || r.Method == "POST" && (r.URL.Path == "/api/auth/logout" || r.URL.Path == "/api/auth/password")) {
			apiError(w, 403, "password_change_required", "Change your password before continuing")
			return
		}
		if admin && !u.Admin {
			apiError(w, 403, "forbidden", "Administrator permission required")
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			if !sameOrigin(r) || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(u.CSRF)) != 1 {
				apiError(w, 403, "csrf", "Invalid request origin or CSRF token")
				return
			}
		}
		fn(w, r, u)
	}
}
func (a *App) login(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		apiError(w, 403, "csrf", "Invalid request origin")
		return
	}
	var in struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if e := decodeJSON(w, r, &in); e != nil {
		apiError(w, 400, "invalid_request", e.Error())
		return
	}
	if !a.allowPasswordAttempt(w, r) {
		return
	}
	var u User
	var hash, prefs string
	var disabled bool
	e := a.DB.QueryRowContext(r.Context(), "SELECT id,name,password,admin,disabled,preferences,must_change_password FROM users WHERE name=?", in.Name).Scan(&u.ID, &u.Name, &hash, &u.Admin, &disabled, &prefs, &u.MustChangePassword)
	if e != nil || disabled {
		hash = fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", a.Config.Password.MemoryKiB, a.Config.Password.Iterations, a.Config.Password.Parallelism)
	}
	select {
	case passwordSlots <- struct{}{}:
	default:
		apiError(w, 429, "busy", "Password verification budget exhausted; retry later")
		return
	}
	valid := checkPassword(hash, in.Password, a.Config.Password)
	<-passwordSlots
	if !valid || e != nil || disabled {
		apiError(w, 401, "invalid_credentials", "Invalid account or password")
		return
	}
	secret := id() + id()
	u.CSRF = id()
	expires := now() + int64(time.Duration(a.Config.SessionHours)*time.Hour/time.Millisecond)
	tx, e := a.DB.BeginTx(r.Context(), nil)
	if e != nil {
		apiError(w, 503, "database", "Sign in temporarily unavailable")
		return
	}
	defer tx.Rollback()
	if old, e := r.Cookie("archika_session"); e == nil {
		_, _ = tx.Exec("DELETE FROM sessions WHERE token=?", tokenHash(old.Value))
	}
	_, e = tx.Exec("INSERT INTO sessions(token,user_id,csrf,expires) VALUES(?,?,?,?)", tokenHash(secret), u.ID, u.CSRF, expires)
	if e == nil {
		e = tx.Commit()
	}
	if e != nil {
		apiError(w, 503, "database", "Sign in temporarily unavailable")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "archika_session", Value: secret, Path: "/", HttpOnly: true, Secure: a.Config.SecureCookies || r.TLS != nil, SameSite: http.SameSiteLaxMode, MaxAge: a.Config.SessionHours * 3600})
	_ = json.Unmarshal([]byte(prefs), &u.Preferences)
	jsonResponse(w, 200, u)
}
func (a *App) logout(w http.ResponseWriter, r *http.Request, u User) {
	c, _ := r.Cookie("archika_session")
	_, e := a.DB.ExecContext(r.Context(), "DELETE FROM sessions WHERE token=?", tokenHash(c.Value))
	if e != nil {
		apiError(w, 503, "database", "Sign out failed")
		return
	}
	a.cancelPrefetch(u.ID)
	http.SetCookie(w, &http.Cookie{Name: "archika_session", Value: "", Path: "/", HttpOnly: true, Secure: a.Config.SecureCookies || r.TLS != nil, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	jsonResponse(w, 200, map[string]bool{"ok": true})
}
func (a *App) preferences(w http.ResponseWriter, r *http.Request, u User) {
	var in map[string]any
	if e := decodeJSON(w, r, &in); e != nil {
		apiError(w, 400, "invalid_request", e.Error())
		return
	}
	for k, v := range in {
		switch k {
		case "reader_mode":
			if v != "paged" && v != "scroll" {
				apiError(w, 400, "invalid_preference", "Invalid reader mode")
				return
			}
		case "layout":
			if v != "single" && v != "spread" {
				apiError(w, 400, "invalid_preference", "Invalid layout")
				return
			}
		case "fit":
			if v != "viewport" && v != "width" && v != "original" {
				apiError(w, 400, "invalid_preference", "Invalid fit")
				return
			}
		case "resize_width":
			n, ok := v.(float64)
			if !ok || n < 0 || n > 4096 {
				apiError(w, 400, "invalid_preference", "Invalid resize width")
				return
			}
		default:
			apiError(w, 400, "invalid_preference", "Unknown preference")
			return
		}
	}
	b, _ := json.Marshal(in)
	_, e := a.DB.ExecContext(r.Context(), "UPDATE users SET preferences=? WHERE id=?", string(b), u.ID)
	if e != nil {
		apiError(w, 503, "database", "Preference write failed")
		return
	}
	jsonResponse(w, 200, map[string]bool{"ok": true})
}
func (a *App) users(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method == "GET" {
		rows, e := a.DB.QueryContext(r.Context(), "SELECT id,name,admin,disabled FROM users ORDER BY name LIMIT 1000")
		if e != nil {
			apiError(w, 503, "database", "Cannot list accounts")
			return
		}
		out := []map[string]any{}
		for rows.Next() {
			var uid, n string
			var admin, disabled bool
			if rows.Scan(&uid, &n, &admin, &disabled) == nil {
				out = append(out, map[string]any{"id": uid, "name": n, "admin": admin, "disabled": disabled, "roots": []string{}})
			}
		}
		rows.Close()
		for _, v := range out {
			rs, e := a.DB.QueryContext(r.Context(), "SELECT root_id FROM grants WHERE user_id=?", v["id"])
			if e != nil {
				continue
			}
			roots := []string{}
			for rs.Next() {
				var id string
				_ = rs.Scan(&id)
				roots = append(roots, id)
			}
			rs.Close()
			v["roots"] = roots
		}
		jsonResponse(w, 200, out)
		return
	}
	var in struct {
		Name     string    `json:"name"`
		Password *string   `json:"password"`
		Admin    *bool     `json:"admin"`
		Disabled *bool     `json:"disabled"`
		Roots    *[]string `json:"roots"`
	}
	if e := decodeJSON(w, r, &in); e != nil {
		apiError(w, 400, "invalid_request", e.Error())
		return
	}
	uid := r.PathValue("id")
	create := r.Method == "POST"
	if create {
		uid = id()
		if !validName(in.Name) || in.Password == nil {
			apiError(w, 400, "invalid_account", "Name and a strong password are required")
			return
		}
	}
	hash := ""
	if in.Password != nil {
		var e error
		hash, e = HashPassword(*in.Password, a.Config.Password)
		if e != nil {
			apiError(w, 400, "invalid_password", e.Error())
			return
		}
	}
	if uid == u.ID && ((in.Disabled != nil && *in.Disabled) || (in.Admin != nil && !*in.Admin)) {
		apiError(w, 400, "self_lockout", "Cannot disable or demote the active administrator")
		return
	}
	tx, e := a.DB.BeginTx(r.Context(), nil)
	if e != nil {
		apiError(w, 503, "database", "Account write failed")
		return
	}
	defer tx.Rollback()
	if create {
		_, e = tx.Exec("INSERT INTO users(id,name,password) VALUES(?,?,?)", uid, in.Name, hash)
	} else {
		var exists int
		e = tx.QueryRow("SELECT count(*) FROM users WHERE id=?", uid).Scan(&exists)
		if e == nil && exists == 0 {
			e = errors.New("account not found")
		}
	}
	if e == nil && hash != "" {
		_, e = tx.Exec("UPDATE users SET password=? WHERE id=?", hash, uid)
	}
	if e == nil && in.Admin != nil {
		_, e = tx.Exec("UPDATE users SET admin=? WHERE id=?", *in.Admin, uid)
	}
	if e == nil && in.Disabled != nil {
		_, e = tx.Exec("UPDATE users SET disabled=? WHERE id=?", *in.Disabled, uid)
	}
	if e == nil && in.Roots != nil {
		_, e = tx.Exec("DELETE FROM grants WHERE user_id=?", uid)
		if len(*in.Roots) > 1000 {
			e = errors.New("too many grants")
		}
		for _, rid := range *in.Roots {
			if e != nil {
				break
			}
			_, e = tx.Exec("INSERT INTO grants(user_id,root_id) VALUES(?,?)", uid, rid)
		}
	}
	if e == nil && !create {
		_, e = tx.Exec("DELETE FROM sessions WHERE user_id=? AND user_id!=?", uid, u.ID)
	}
	if e == nil {
		e = bump(tx)
	}
	if e == nil {
		e = audit(tx, u.ID, "account.update", "", map[string]string{"user_id": uid})
	}
	if e == nil {
		e = tx.Commit()
	}
	if e != nil {
		apiError(w, 400, "account_write", fmt.Sprintf("Account change rejected: %s", safeError(e)))
		return
	}
	a.cancelPrefetch("")
	jsonResponse(w, 200, map[string]any{"ok": true, "id": uid})
}

func (a *App) allowPasswordAttempt(w http.ResponseWriter, r *http.Request) bool {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip, e := netip.ParseAddr(host); e == nil {
		ip = ip.Unmap()
		if ip.Is6() {
			host = netip.PrefixFrom(ip, 64).Masked().String()
		} else {
			host = ip.String()
		}
	}
	a.authMu.Lock()
	at := time.Now()
	v, tracked := a.loginAttempts[host]
	if at.Sub(v.At) > time.Minute {
		v = loginAttempt{At: at}
	}
	v.Count++
	for !tracked && len(a.loginAttempts) >= 10000 {
		var oldestHost string
		var oldestAt time.Time
		for k, attempt := range a.loginAttempts {
			if at.Sub(attempt.At) > time.Minute {
				delete(a.loginAttempts, k)
				continue
			}
			if oldestAt.IsZero() || attempt.At.Before(oldestAt) {
				oldestHost, oldestAt = k, attempt.At
			}
		}
		if len(a.loginAttempts) >= 10000 {
			delete(a.loginAttempts, oldestHost)
		}
	}
	a.loginAttempts[host] = v
	a.authMu.Unlock()
	if v.Count > 10 {
		apiError(w, 429, "rate_limited", "Try again in a minute")
		return false
	}
	return true
}
