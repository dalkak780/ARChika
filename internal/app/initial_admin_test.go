package app

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitialAdmin(t *testing.T) {
	for _, mode := range []string{"generated", "environment", "file", "short", "unreadable", "invalid-name"} {
		t.Run(mode, func(t *testing.T) {
			a, _ := testApp(t)
			t.Setenv("ARCHIKA_INITIAL_ADMIN_NAME", "")
			t.Setenv("ARCHIKA_INITIAL_ADMIN_PASSWORD_FILE", "")
			t.Setenv("ARCHIKA_INITIAL_ADMIN_PASSWORD", "")
			if err := os.Unsetenv("ARCHIKA_INITIAL_ADMIN_PASSWORD"); err != nil {
				t.Fatal(err)
			}
			password := "environment-secret-password"
			switch mode {
			case "environment":
				t.Setenv("ARCHIKA_INITIAL_ADMIN_PASSWORD", password)
				t.Setenv("ARCHIKA_INITIAL_ADMIN_NAME", "owner")
			case "file":
				path := filepath.Join(t.TempDir(), "secret")
				if err := os.WriteFile(path, []byte(password+"\r\n"), 0600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("ARCHIKA_INITIAL_ADMIN_PASSWORD_FILE", path)
				t.Setenv("ARCHIKA_INITIAL_ADMIN_PASSWORD", "ignored")
			case "short":
				t.Setenv("ARCHIKA_INITIAL_ADMIN_PASSWORD", "short")
			case "unreadable":
				t.Setenv("ARCHIKA_INITIAL_ADMIN_PASSWORD_FILE", filepath.Join(t.TempDir(), "missing"))
			case "invalid-name":
				t.Setenv("ARCHIKA_INITIAL_ADMIN_NAME", "bad\nname")
			}
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			err := EnsureInitialAdmin(a.DB, a.Config.Password, logger)
			if mode == "short" || mode == "unreadable" || mode == "invalid-name" {
				if err == nil || logs.Len() != 0 {
					t.Fatalf("error=%v logs=%s", err, &logs)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var entry struct{ Level, Msg, Name, Password, Notice string }
			if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &entry); err != nil {
				t.Fatal(err)
			}
			var hash, name string
			var admin, mustChange bool
			if err := a.DB.QueryRow("SELECT name,password,admin,must_change_password FROM users").Scan(&name, &hash, &admin, &mustChange); err != nil {
				t.Fatal(err)
			}
			if !admin || name != entry.Name || mustChange != (mode == "generated") {
				t.Fatalf("unexpected account: %s %v %v", name, admin, mustChange)
			}
			if mode == "generated" {
				password = entry.Password
				if len(password) != 24 || entry.Level != "WARN" || !strings.Contains(entry.Notice, "will not be shown again") {
					t.Fatalf("unexpected log: %s", &logs)
				}
				for _, c := range password {
					if !strings.ContainsRune("ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789-_", c) {
						t.Fatal("ambiguous password character")
					}
				}
			} else if entry.Level != "INFO" || entry.Msg != "Initial administrator created from environment" || strings.Contains(logs.String(), password) || entry.Password != "" {
				t.Fatalf("unexpected log: %s", &logs)
			}
			if !checkPassword(hash, password, a.Config.Password) {
				t.Fatal("password does not verify")
			}
			before := logs.String()
			t.Setenv("ARCHIKA_INITIAL_ADMIN_PASSWORD_FILE", "/missing")
			if err := EnsureInitialAdmin(a.DB, a.Config.Password, logger); err != nil {
				t.Fatal(err)
			}
			if logs.String() != before {
				t.Fatal("bootstrap logged twice")
			}
			if err := BootstrapAdmin(a.DB, "another", "another-long-password", a.Config.Password); err == nil {
				t.Fatal("second admin accepted")
			}
		})
	}
}

func TestMigrationTwoToThree(t *testing.T) {
	c := DefaultConfig()
	c.DataDir = t.TempDir()
	db, err := OpenDB(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO users(id,name,password) VALUES('existing','existing','hash'); ALTER TABLE users DROP COLUMN must_change_password; PRAGMA user_version=2"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = OpenDB(c)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	var mustChange bool
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow("SELECT must_change_password FROM users WHERE id='existing'").Scan(&mustChange); err != nil {
		t.Fatal(err)
	}
	if version != 3 || mustChange {
		t.Fatalf("version=%d mustChange=%v", version, mustChange)
	}
}
