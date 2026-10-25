package app

import (
	"database/sql/driver"
	"strings"

	"golang.org/x/text/unicode/norm"
	"modernc.org/sqlite"
)

func normalizeSearch(s string) string { return strings.ToLower(norm.NFC.String(s)) }
func init() {
	sqlite.MustRegisterDeterministicScalarFunction("archika_search", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		s, _ := args[0].(string)
		return normalizeSearch(s), nil
	})
}
