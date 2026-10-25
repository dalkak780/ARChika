package app

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

type User struct {
	MustChangePassword bool           `json:"must_change_password"`
	ID                 string         `json:"id"`
	Name               string         `json:"name"`
	Admin              bool           `json:"admin"`
	CSRF               string         `json:"csrf,omitempty"`
	Preferences        map[string]any `json:"preferences,omitempty"`
	scope              string
}
type Root struct {
	ID            string   `json:"id"`
	Key           string   `json:"key"`
	Name          string   `json:"name"`
	Path          string   `json:"path,omitempty"`
	Watch         bool     `json:"watch"`
	AutoCrawl     bool     `json:"auto_crawl"`
	Providers     []string `json:"providers"`
	QuietSeconds  int      `json:"quiet_seconds"`
	ParserEnabled bool     `json:"parser_enabled"`
	ParserRegex   string   `json:"parser_regex"`
	Status        string   `json:"status"`
	PendingPath   string   `json:"pending_path,omitempty"`
	Warning       string   `json:"warning,omitempty"`
	TrustIdentity bool     `json:"trust_identity"`
}
type Alias struct {
	ID              int64  `json:"alias_id"`
	WorkID          string `json:"id"`
	RootID          string `json:"root_id"`
	RootName        string `json:"root_name"`
	Path            string `json:"relative_path"`
	RootPath        string `json:"-"`
	OriginalName    string `json:"original_name"`
	Title           string `json:"title"`
	Kind            string `json:"kind"`
	Size            int64  `json:"size"`
	Mtime           int64  `json:"-"` // Container observation; galleries use a metadata digest.
	ModifiedAt      int64  `json:"mtime"`
	Version         int64  `json:"version"`
	RegisteredAt    int64  `json:"registered_at"`
	IndexStatus     string `json:"index_status"`
	ErrorCode       string `json:"error_code,omitempty"`
	PhysicalKey     string `json:"-"`
	Cover           string `json:"cover"`
	CoverUnresolved bool   `json:"cover_unresolved"`
	Favorite        bool   `json:"favorite"`
}
type App struct {
	DB                 *sql.DB
	dataLock           *os.File
	Config             Config
	Log                *slog.Logger
	ctx                context.Context
	cancel             context.CancelFunc
	wg                 sync.WaitGroup
	cache              *PageCache
	sessions           *ArchivePool
	indexMu            sync.Mutex
	indexFlights       map[string]*indexFlight
	foreground         chan struct{}
	transform          chan struct{}
	delivery           chan struct{}
	thumbnails         chan struct{}
	thumbnailQueue     chan struct{}
	speculative        chan struct{}
	interactive        atomic.Int32
	costsMu            sync.Mutex
	costs              map[string]float64
	jobWake            chan struct{}
	prefetchMu         sync.Mutex
	prefetch           map[string]context.CancelFunc
	prefetchGeneration map[string]string
	authMu             sync.Mutex
	loginAttempts      map[string]loginAttempt
	watcherMu          sync.Mutex
	watcherReload      chan struct{}
	crawler            *Crawler
}
type loginAttempt struct {
	Count int
	At    time.Time
}
type indexFlight struct {
	done     chan struct{}
	err      error
	priority prioritySignal
	cancel   context.CancelFunc
	waiters  int
}

func New(c Config) (*App, error) {
	dataLock, e := LockDirectory(c.DataDir)
	if e != nil {
		return nil, e
	}
	opened := false
	defer func() {
		if !opened {
			dataLock.Close()
		}
	}()
	db, e := OpenDB(c)
	if e != nil {
		return nil, e
	}
	if c.PauseJobs {
		if _, e = db.Exec("INSERT INTO settings(key,value) VALUES('pause_jobs','1') ON CONFLICT(key) DO UPDATE SET value='1'"); e != nil {
			db.Close()
			return nil, e
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	a := &App{DB: db, Config: c, Log: slog.Default(), ctx: ctx, cancel: cancel, indexFlights: map[string]*indexFlight{}, foreground: make(chan struct{}, c.Workers), transform: make(chan struct{}, 1), delivery: make(chan struct{}, c.Workers*4), speculative: make(chan struct{}, 1), costs: map[string]float64{}, jobWake: make(chan struct{}, 1), prefetch: map[string]context.CancelFunc{}, prefetchGeneration: map[string]string{}, loginAttempts: map[string]loginAttempt{}, watcherReload: make(chan struct{}, 1)}
	a.dataLock = dataLock
	a.thumbnails = make(chan struct{}, min(c.Workers, 2))
	a.thumbnailQueue = make(chan struct{}, 64)
	a.cache, e = NewPageCache(c.CacheDir, int64(c.MemoryBytes), int64(c.DiskBytes), int64(c.PageBytes))
	if e != nil {
		db.Close()
		cancel()
		return nil, e
	}
	a.sessions = NewArchivePool(c.Workers+2, int64(c.PageBytes))
	a.crawler, e = NewCrawler(a)
	if e != nil {
		a.cache.Close()
		db.Close()
		cancel()
		return nil, e
	}
	opened = true
	return a, nil
}
func (a *App) Start() {
	a.wg.Add(3)
	go func() { defer a.wg.Done(); a.jobLoop() }()
	go func() { defer a.wg.Done(); a.watchLoop() }()
	go func() { defer a.wg.Done(); a.prefetchMaintenance() }()
}
func (a *App) Close() error {
	a.indexMu.Lock()
	a.cancel()
	a.indexMu.Unlock()
	a.prefetchMu.Lock()
	for _, cancel := range a.prefetch {
		cancel()
	}
	a.prefetchMu.Unlock()
	a.wg.Wait()
	a.cache.Close()
	a.sessions.Close()
	err := a.DB.Close()
	_ = a.dataLock.Close()
	return err
}
func (a *App) signal() {
	select {
	case a.jobWake <- struct{}{}:
	default:
	}
}
func (a *App) notifyWatch() {
	select {
	case a.watcherReload <- struct{}{}:
	default:
	}
}
func (a *App) prefetchMaintenance() {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-t.C:
			a.authMu.Lock()
			for k, v := range a.loginAttempts {
				if time.Since(v.At) > time.Minute*10 {
					delete(a.loginAttempts, k)
				}
			}
			a.authMu.Unlock()
			_, _ = a.DB.ExecContext(a.ctx, "DELETE FROM sessions WHERE expires<?", now())
		}
	}
}
func jsonResponse(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = jsonEncoder(w, v)
}
