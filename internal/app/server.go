package app

import (
	"io/fs"
	"net/http"
	"runtime"
	"strings"

	"archika/web"
)

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { jsonResponse(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("POST /api/auth/login", a.login)
	mux.HandleFunc("GET /api/auth/me", a.authenticated(false, func(w http.ResponseWriter, r *http.Request, u User) { jsonResponse(w, 200, u) }))
	mux.HandleFunc("POST /api/auth/password", a.authenticated(false, a.changePassword))
	mux.HandleFunc("POST /api/auth/logout", a.authenticated(false, a.logout))
	mux.HandleFunc("PUT /api/preferences", a.authenticated(false, a.preferences))
	mux.HandleFunc("GET /api/roots", a.authenticated(false, a.roots))
	mux.HandleFunc("GET /api/works/{id}/neighbors", a.authenticated(false, a.neighbors))
	mux.HandleFunc("GET /api/works/{id}/pages/{page}/thumbnail", a.authenticated(false, a.pageThumbnailHTTP))
	mux.HandleFunc("GET /api/folders", a.authenticated(false, a.folders))
	mux.HandleFunc("GET /api/works", a.authenticated(false, a.works))
	mux.HandleFunc("GET /api/works/{id}", a.authenticated(false, a.workDetail))
	mux.HandleFunc("GET /api/works/{id}/pages/{page}", a.authenticated(false, a.pageHTTP))
	mux.HandleFunc("GET /api/works/{id}/thumbnail", a.authenticated(false, a.thumbnailHTTP))
	mux.HandleFunc("POST /api/works/{id}/prefetch", a.authenticated(false, a.prefetchHTTP))
	mux.HandleFunc("DELETE /api/works/{id}/prefetch", a.authenticated(false, a.prefetchHTTP))
	mux.HandleFunc("PUT /api/works/{id}/cover", a.authenticated(false, a.coverHTTP))
	mux.HandleFunc("PUT /api/works/{id}/favorite", a.authenticated(false, a.favoriteHTTP))
	mux.HandleFunc("POST /api/works/{id}/retry", a.authenticated(true, a.retryWork))
	mux.HandleFunc("GET /api/works/{id}/metadata", a.authenticated(false, a.metadataHTTP))
	mux.HandleFunc("PUT /api/works/{id}/metadata", a.authenticated(false, a.metadataHTTP))
	mux.HandleFunc("GET /api/saved-searches", a.authenticated(false, a.savedSearches))
	mux.HandleFunc("POST /api/saved-searches", a.authenticated(false, a.savedSearches))
	mux.HandleFunc("DELETE /api/saved-searches/{id}", a.authenticated(false, a.savedSearches))
	mux.HandleFunc("GET /api/admin/users", a.authenticated(true, a.users))
	mux.HandleFunc("POST /api/admin/users", a.authenticated(true, a.users))
	mux.HandleFunc("PUT /api/admin/users/{id}", a.authenticated(true, a.users))
	mux.HandleFunc("POST /api/admin/roots", a.authenticated(true, a.rootWrite))
	mux.HandleFunc("PUT /api/admin/roots/{id}", a.authenticated(true, a.rootWrite))
	mux.HandleFunc("DELETE /api/admin/roots/{id}", a.authenticated(true, a.rootDelete))
	mux.HandleFunc("POST /api/admin/roots/{id}/relocation", a.authenticated(true, a.relocation))
	mux.HandleFunc("GET /api/admin/config/preview", a.authenticated(true, a.configPreview))
	mux.HandleFunc("POST /api/admin/config/apply", a.authenticated(true, a.configApply))
	mux.HandleFunc("GET /api/admin/jobs", a.authenticated(true, a.jobs))
	mux.HandleFunc("POST /api/admin/jobs", a.authenticated(true, a.jobs))
	mux.HandleFunc("POST /api/admin/jobs/pause", a.authenticated(true, a.pauseJobs))
	mux.HandleFunc("POST /api/admin/jobs/{id}", a.authenticated(true, a.jobAction))
	mux.HandleFunc("GET /api/admin/offline", a.authenticated(true, a.offline))
	mux.HandleFunc("POST /api/admin/cleanup", a.authenticated(true, a.cleanupHTTP))
	mux.HandleFunc("POST /api/admin/restore", a.authenticated(true, a.restoreHTTP))
	mux.HandleFunc("DELETE /api/admin/works/{id}", a.authenticated(true, a.deleteWorkHTTP))
	mux.HandleFunc("GET /api/admin/audit", a.authenticated(true, a.auditHTTP))
	mux.HandleFunc("GET /api/admin/moves", a.authenticated(true, a.movesHTTP))
	mux.HandleFunc("POST /api/admin/moves/{id}", a.authenticated(true, a.movesHTTP))
	mux.HandleFunc("GET /api/admin/providers", a.authenticated(true, a.providersHTTP))
	mux.HandleFunc("PUT /api/admin/providers/{id}", a.authenticated(true, a.providerWrite))
	mux.HandleFunc("POST /api/admin/crawl", a.authenticated(true, a.crawlHTTP))
	mux.HandleFunc("POST /api/admin/candidates/{id}", a.authenticated(true, a.candidateHTTP))
	mux.HandleFunc("POST /api/admin/plugins", a.authenticated(true, a.pluginsHTTP))
	mux.HandleFunc("PUT /api/admin/plugins/{id}", a.authenticated(true, a.pluginsHTTP))
	mux.HandleFunc("DELETE /api/admin/plugins/{id}", a.authenticated(true, a.pluginsHTTP))
	mux.HandleFunc("GET /api/admin/plugins/{id}/manifest", a.authenticated(true, a.pluginsHTTP))
	mux.HandleFunc("GET /api/admin/stats", a.authenticated(true, func(w http.ResponseWriter, r *http.Request, u User) {
		var memory runtime.MemStats
		runtime.ReadMemStats(&memory)
		jsonResponse(w, 200, map[string]any{"cache": a.cache.Stats(), "archive_pool": a.sessions.Stats(), "go_heap_bytes": memory.HeapAlloc, "goroutines": runtime.NumGoroutine(), "generation": a.Generation(r.Context())})
	}))
	assets, e := fs.Sub(web.Assets, "dist")
	if e != nil {
		panic(e)
	}
	files := http.FileServer(http.FS(assets))
	index, e := fs.ReadFile(assets, "index.html")
	if e != nil {
		panic("SPA not built: pnpm --dir web build")
	}
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if strings.HasPrefix(p, "/api/") || p == "/api" {
			apiError(w, 404, "not_found", "Route not found")
			return
		}
		if strings.HasPrefix(p, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			files.ServeHTTP(w, r)
			return
		}
		if p != "/" && p != "/login" && p != "/admin" && !strings.HasPrefix(p, "/read/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(index)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		mux.ServeHTTP(w, r)
	})
}
