package app

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type RootConfig struct {
	Key          string `json:"key"`
	Name         string `json:"name"`
	Path         string `json:"path"`
	Watch        bool   `json:"watch"`
	AutoCrawl    bool   `json:"auto_crawl"`
	QuietSeconds int    `json:"quiet_seconds"`
}
type Config struct {
	Listen          string         `json:"listen"`
	DataDir         string         `json:"data_dir"`
	CacheDir        string         `json:"cache_dir"`
	AllowedPaths    []string       `json:"allowed_paths"`
	Roots           []RootConfig   `json:"roots"`
	MemoryBytes     int64          `json:"memory_bytes"`
	DiskBytes       int64          `json:"disk_bytes"`
	PageBytes       int64          `json:"page_bytes"`
	Workers         int            `json:"workers"`
	SessionHours    int            `json:"session_hours"`
	Password        PasswordPolicy `json:"password"`
	QuietSeconds    int            `json:"quiet_seconds"`
	ScanPerSecond   int            `json:"scan_per_second"`
	MaxPixels       int64          `json:"max_pixels"`
	SecureCookies   bool           `json:"secure_cookies"`
	PauseJobs       bool           `json:"pause_jobs"`
	MasterKey       string         `json:"-"`
	DNSMode         string         `json:"dns_mode"`
	DoHURL          string         `json:"doh_url"`
	ProxyURL        string         `json:"proxy_url"`
	PluginDirectory string         `json:"plugin_directory"`
}

func DefaultConfig() Config {
	return Config{Listen: "127.0.0.1:8080", DataDir: "./data", CacheDir: filepath.Join(os.TempDir(), "archika-pages"), MemoryBytes: 64 << 20, DiskBytes: 1 << 30, PageBytes: 64 << 20, Workers: 2, SessionHours: 168, Password: defaultPasswordPolicy(), QuietSeconds: 3, ScanPerSecond: 200, MaxPixels: 64_000_000, DNSMode: "doh", DoHURL: "https://cloudflare-dns.com/dns-query", PluginDirectory: "./plugins"}
}
func LoadConfig(path string) (Config, error) {
	c := DefaultConfig()
	if path != "" {
		b, e := os.ReadFile(path)
		if e != nil {
			return c, e
		}
		if ext := strings.ToLower(filepath.Ext(path)); ext == ".yaml" || ext == ".yml" {
			var raw any
			if e = yaml.Unmarshal(b, &raw); e != nil {
				return c, e
			}
			b, e = json.Marshal(raw)
			if e != nil {
				return c, e
			}
		}
		if e = json.Unmarshal(b, &c); e != nil {
			return c, e
		}
	}
	c.MasterKey = os.Getenv("ARCHIKA_MASTER_KEY")
	if !c.Password.valid() {
		return c, errors.New("invalid Argon2id resource policy")
	}
	if c.Workers < 1 || c.Workers > 32 || c.PageBytes < 1<<20 || c.PageBytes > 512<<20 || c.MemoryBytes < 0 || c.DiskBytes < 0 || c.SessionHours < 1 || c.QuietSeconds < 1 || c.ScanPerSecond < 1 || c.MaxPixels < 1 || c.MaxPixels > 256_000_000 {
		return c, errors.New("invalid resource limits")
	}
	var e error
	c.DataDir, e = filepath.Abs(c.DataDir)
	if e != nil {
		return c, e
	}
	c.CacheDir, e = filepath.Abs(c.CacheDir)
	if e != nil {
		return c, e
	}
	if c.CacheDir == c.DataDir || within(c.DataDir, c.CacheDir) {
		return c, errors.New("temporary page cache must be outside persistent data")
	}
	for i, p := range c.AllowedPaths {
		c.AllowedPaths[i], e = filepath.Abs(p)
		if e != nil {
			return c, e
		}
	}
	return c, nil
}
func within(root, path string) bool {
	r, e := filepath.Rel(root, path)
	return e == nil && r != ".." && filepath.IsLocal(r)
}
func (c Config) RootPath(p string) (string, error) {
	p, e := filepath.Abs(p)
	if e != nil {
		return "", e
	}
	p, e = filepath.EvalSymlinks(p)
	if e != nil {
		return "", e
	}
	allowed := false
	for _, a := range c.AllowedPaths {
		canonical, err := filepath.EvalSymlinks(a)
		if err != nil {
			continue
		}
		canonical, err = filepath.Abs(canonical)
		if err == nil && (p == canonical || within(canonical, p)) {
			allowed = true
		}
	}
	if !allowed {
		return "", errors.New("path is outside approved library mounts")
	}
	f, e := os.Open(p)
	if e != nil {
		return "", e
	}
	defer f.Close()
	s, e := f.Stat()
	if e != nil || !s.IsDir() {
		return "", errors.New("root must be a readable directory")
	}
	_, e = f.ReadDir(1)
	if e != nil && e.Error() != "EOF" {
		return "", e
	}
	return p, nil
}
func id() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func now() int64 { return time.Now().UnixMilli() }
func safeError(err error) string {
	if err == nil {
		return ""
	}
	var network *crawlError
	if errors.As(err, &network) {
		return network.Error()
	}
	return fmt.Sprintf("operation failed (%T); administrator can retry", err)
}
