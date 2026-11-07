package app

import (
	"strings"
	"testing"
)

func TestResetPassword(t *testing.T) {
	a, _ := testApp(t)
	if e := BootstrapAdmin(a.DB, "admin", "fixture-password-strong", a.Config.Password); e != nil {
		t.Fatal(e)
	}
	var uid string
	if e := a.DB.QueryRow("SELECT id FROM users WHERE name='admin'").Scan(&uid); e != nil {
		t.Fatal(e)
	}
	if _, e := a.DB.Exec("UPDATE users SET must_change_password=1 WHERE id=?; INSERT INTO sessions(token,user_id,csrf,expires) VALUES('t',?,'c',9999999999)", uid, uid); e != nil {
		t.Fatal(e)
	}
	if e := ResetPassword(a.DB, "missing", "replacement-password", a.Config.Password); e == nil || !strings.Contains(e.Error(), "admin") {
		t.Fatalf("missing account: %v", e)
	}
	if e := ResetPassword(a.DB, "admin", "short", a.Config.Password); e == nil {
		t.Fatal("short password accepted")
	}
	if e := ResetPassword(a.DB, "admin", "replacement-password", a.Config.Password); e != nil {
		t.Fatal(e)
	}
	var hash string
	var mustChange bool
	var sessions int
	if e := a.DB.QueryRow("SELECT password,must_change_password,(SELECT count(*) FROM sessions WHERE user_id=users.id) FROM users WHERE id=?", uid).Scan(&hash, &mustChange, &sessions); e != nil {
		t.Fatal(e)
	}
	if !checkPassword(hash, "replacement-password", a.Config.Password) || checkPassword(hash, "fixture-password-strong", a.Config.Password) {
		t.Fatal("password not replaced")
	}
	if mustChange || sessions != 0 {
		t.Fatalf("mustChange=%v sessions=%d", mustChange, sessions)
	}
}

func TestListAccounts(t *testing.T) {
	a, _ := testApp(t)
	if e := BootstrapAdmin(a.DB, "owner", "fixture-password-strong", a.Config.Password); e != nil {
		t.Fatal(e)
	}
	if _, e := a.DB.Exec("INSERT INTO users(id,name,password,disabled) VALUES('u2','reader','x',1)"); e != nil {
		t.Fatal(e)
	}
	got, e := ListAccounts(a.DB)
	if e != nil {
		t.Fatal(e)
	}
	want := []Account{{Name: "owner", Admin: true}, {Name: "reader", Disabled: true}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %+v", got)
	}
}
