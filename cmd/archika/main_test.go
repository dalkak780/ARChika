package main

import (
	"log/slog"
	"strings"
	"testing"
)

func TestLoggerEnvironment(t *testing.T) {
	old := slog.Default()
	t.Cleanup(func() { slog.SetDefault(old) })
	for _, test := range []struct{ level, format, invalid string }{
		{"", "", ""}, {"debug", "json", ""}, {"info", "text", ""},
		{"warn", "text", ""}, {"error", "json", ""},
		{"verbose", "text", "ARCHIKA_LOG_LEVEL"}, {"info", "xml", "ARCHIKA_LOG_FORMAT"},
	} {
		t.Run(test.level+"/"+test.format, func(t *testing.T) {
			t.Setenv("ARCHIKA_LOG_LEVEL", test.level)
			t.Setenv("ARCHIKA_LOG_FORMAT", test.format)
			err := setupLogger()
			if test.invalid == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.invalid) {
				t.Fatalf("got %v, want %s", err, test.invalid)
			}
		})
	}
}
