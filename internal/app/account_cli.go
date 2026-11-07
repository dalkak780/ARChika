package app

import (
	"database/sql"
	"fmt"
	"strings"
)

// Account is what the offline CLI shows about an account; it never includes the hash.
type Account struct {
	Name     string
	Admin    bool
	Disabled bool
}

// ListAccounts returns every account ordered by name.
func ListAccounts(db *sql.DB) ([]Account, error) { return listAccounts(db) }

func listAccounts(q interface {
	Query(string, ...any) (*sql.Rows, error)
}) ([]Account, error) {
	rows, e := q.Query("SELECT name,admin,disabled FROM users ORDER BY name")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		var a Account
		if e = rows.Scan(&a.Name, &a.Admin, &a.Disabled); e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ResetPassword replaces an existing account's password and signs it out everywhere.
// The new password is chosen by the operator, so no change is forced at next sign-in.
func ResetPassword(db *sql.DB, name, password string, policy PasswordPolicy) error {
	hash, e := HashPassword(password, policy)
	if e != nil {
		return e
	}
	tx, e := db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var uid string
	if e = tx.QueryRow("SELECT id FROM users WHERE name=?", name).Scan(&uid); e == sql.ErrNoRows {
		accounts, le := listAccounts(tx)
		if le != nil {
			return fmt.Errorf("account %q not found", name)
		}
		names := make([]string, len(accounts))
		for i, a := range accounts {
			names[i] = a.Name
		}
		return fmt.Errorf("account %q not found; existing accounts: %s", name, strings.Join(names, ", "))
	} else if e != nil {
		return e
	}
	if _, e = tx.Exec("UPDATE users SET password=?,must_change_password=0 WHERE id=?", hash, uid); e != nil {
		return e
	}
	if _, e = tx.Exec("DELETE FROM sessions WHERE user_id=?", uid); e != nil {
		return e
	}
	return tx.Commit()
}
