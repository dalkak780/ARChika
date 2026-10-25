package app

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"strings"
)

func EnsureInitialAdmin(db *sql.DB, policy PasswordPolicy, logger *slog.Logger) error {
	var count int
	if err := db.QueryRow("SELECT count(*) FROM users").Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return nil
	}
	name := os.Getenv("ARCHIKA_INITIAL_ADMIN_NAME")
	if name == "" {
		name = "admin"
	}
	password, supplied := os.LookupEnv("ARCHIKA_INITIAL_ADMIN_PASSWORD")
	if path := os.Getenv("ARCHIKA_INITIAL_ADMIN_PASSWORD_FILE"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read initial administrator password file: %w", err)
		}
		password = string(data)
		if strings.HasSuffix(password, "\n") {
			password = strings.TrimSuffix(strings.TrimSuffix(password, "\n"), "\r")
		}
		supplied = true
	}
	if !supplied {
		const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789-_"
		var secret strings.Builder
		for range 24 {
			n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
			if err != nil {
				return err
			}
			secret.WriteByte(alphabet[n.Int64()])
		}
		password = secret.String()
	}
	if err := bootstrapAdmin(db, name, password, policy, !supplied); err != nil {
		if errors.Is(err, errAccountsExist) {
			return nil
		}
		return fmt.Errorf("create initial administrator: %w", err)
	}
	if supplied {
		logger.Info("Initial administrator created from environment", "name", name)
	} else {
		logger.Warn("Initial administrator created", "name", name, "password", password, "notice", "Must be changed at first sign-in; will not be shown again")
	}
	return nil
}
