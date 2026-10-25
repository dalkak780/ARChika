package app

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"
)

func (a *App) commitAdmin(tx *sql.Tx, actor, action, root string, detail any) error {
	if e := tx.Commit(); e != nil {
		return e
	}
	a.Log.Info("admin action", "actor", actor, "action", action, "root", root, "detail", detail)
	return nil
}

type responseStatus struct {
	http.ResponseWriter
	status int
}

func (w *responseStatus) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *responseStatus) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	if status >= 200 || status == http.StatusSwitchingProtocols {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *responseStatus) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}
func (w *responseStatus) ReadFrom(r io.Reader) (int64, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return io.Copy(w.ResponseWriter, r)
}
func (w *responseStatus) FlushError() error {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}
func (w *responseStatus) Flush() { _ = w.FlushError() }

func (a *App) logRequest(r *http.Request, status int, duration time.Duration) {
	attrs := []any{"method", r.Method, "path", r.URL.Path, "status", status, "duration_ms", duration.Milliseconds()}
	if status >= 500 {
		a.Log.Error("request failed", attrs...)
	}
	if duration > 5*time.Second {
		a.Log.Warn("slow request", attrs...)
	}
	a.Log.Debug("request finished", attrs...)
}
func (a *App) requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		status := &responseStatus{ResponseWriter: w}
		defer func() {
			if value := recover(); value != nil {
				if value == http.ErrAbortHandler {
					panic(value)
				}
				a.Log.Error("request panic", "method", r.Method, "path", r.URL.Path, "panic", value, "stack", string(debug.Stack()))
				if status.status == 0 {
					apiError(status, 500, "internal_error", "Internal server error")
				}
			}
			code := status.status
			if code == 0 {
				code = http.StatusOK
			}
			a.logRequest(r, code, time.Since(started))
		}()
		next.ServeHTTP(status, r)
	})
}

type helperLogKey struct{}

func helperLog(ctx context.Context) *slog.Logger {
	if log, ok := ctx.Value(helperLogKey{}).(*slog.Logger); ok {
		return log
	}
	return slog.Default()
}
func logError(e error) string {
	var pathError *os.PathError
	if errors.As(e, &pathError) && filepath.IsAbs(pathError.Path) {
		return pathError.Op + ": " + pathError.Err.Error()
	}
	var linkError *os.LinkError
	if errors.As(e, &linkError) {
		return linkError.Op + ": " + linkError.Err.Error()
	}
	return e.Error()
}
