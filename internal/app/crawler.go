package app

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type Crawler struct {
	app   *App
	http  *CrawlerHTTP
	aead  cipher.AEAD
	slots chan struct{}
}
type providerRecord struct {
	ID, Name, Version, Kind, Manifest, Executable, Config string
	Enabled                                               bool
}

var builtinHosts = map[string][]string{"ehentai": {"e-hentai.org", "exhentai.org", "api.e-hentai.org"}, "nhentai": {"nhentai.net"}, "chaika": {"panda.chaika.moe"}, "local-json": {}}

func NewCrawler(a *App) (*Crawler, error) {
	if _, e := a.DB.Exec(crawlerSchema); e != nil {
		return nil, e
	}
	h, e := NewCrawlerHTTP(a.Config)
	if e != nil {
		return nil, e
	}
	c := &Crawler{app: a, http: h, slots: make(chan struct{}, 1)}
	if a.Config.MasterKey != "" {
		key, e := base64.StdEncoding.DecodeString(a.Config.MasterKey)
		if e != nil || len(key) != 32 {
			key, e = hex.DecodeString(a.Config.MasterKey)
		}
		if e != nil || len(key) != 32 {
			return nil, errors.New("ARCHIKA_MASTER_KEY must encode exactly 32 bytes (base64 or hex)")
		}
		block, e := aes.NewCipher(key)
		if e != nil {
			return nil, e
		}
		c.aead, e = cipher.NewGCM(block)
		if e != nil {
			return nil, e
		}
	}
	for i, provider := range []struct{ id, name, version string }{{"ehentai", "E-Hentai / ExHentai", "1"}, {"nhentai", "nHentai", "1"}, {"chaika", "Chaika.moe", "1"}, {"local-json", "Local JSON", "1"}} {
		_, e = a.DB.Exec("INSERT OR IGNORE INTO providers(id,name,version,kind,priority) VALUES(?,?,?,'builtin',?)", provider.id, provider.name, provider.version, i)
		if e != nil {
			return nil, e
		}
	}
	return c, nil
}
func (c *Crawler) provider(ctx context.Context, id string) (providerRecord, error) {
	var p providerRecord
	e := c.app.DB.QueryRowContext(ctx, "SELECT id,name,version,kind,manifest,executable,config,enabled FROM providers WHERE id=?", id).Scan(&p.ID, &p.Name, &p.Version, &p.Kind, &p.Manifest, &p.Executable, &p.Config, &p.Enabled)
	return p, e
}
func (c *Crawler) credentials(ctx context.Context, provider string) (map[string]string, error) {
	var encrypted []byte
	e := c.app.DB.QueryRowContext(ctx, "SELECT ciphertext FROM provider_credentials WHERE provider=?", provider).Scan(&encrypted)
	if e == sql.ErrNoRows {
		return map[string]string{}, nil
	}
	if e != nil {
		return nil, e
	}
	if c.aead == nil {
		return nil, errors.New("credential encryption key is not configured")
	}
	n := c.aead.NonceSize()
	if len(encrypted) < n {
		return nil, errors.New("invalid credential ciphertext")
	}
	b, e := c.aead.Open(nil, encrypted[:n], encrypted[n:], []byte(provider))
	if e != nil {
		return nil, errors.New("credential decryption failed; verify master key")
	}
	var out map[string]string
	if json.Unmarshal(b, &out) != nil {
		return nil, errors.New("invalid credentials")
	}
	return out, nil
}
func (c *Crawler) encryptCredentials(provider string, secrets map[string]string) ([]byte, error) {
	if c.aead == nil {
		return nil, errors.New("configure ARCHIKA_MASTER_KEY before storing provider credentials")
	}
	if len(secrets) > 20 {
		return nil, errors.New("too many credential fields")
	}
	for k, v := range secrets {
		if len(k) > 80 || len(v) > 16384 || strings.ContainsAny(v, "\r\n") {
			return nil, errors.New("invalid credential field")
		}
	}
	b, e := json.Marshal(secrets)
	if e != nil {
		return nil, e
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, e = io.ReadFull(rand.Reader, nonce); e != nil {
		return nil, e
	}
	return c.aead.Seal(nonce, nonce, b, []byte(provider)), nil
}
func redactValue(value any, secrets map[string]string) any {
	switch v := value.(type) {
	case string:
		for _, secret := range secrets {
			if secret != "" {
				v = strings.ReplaceAll(v, secret, "[REDACTED]")
			}
		}
		return v
	case map[string]any:
		out := map[string]any{}
		for k, val := range v {
			lower := strings.ToLower(k)
			if strings.Contains(lower, "password") || strings.Contains(lower, "cookie") || strings.Contains(lower, "authorization") || strings.Contains(lower, "secret") || strings.Contains(lower, "token") {
				continue
			}
			out[k] = redactValue(val, secrets)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, val := range v {
			out[i] = redactValue(val, secrets)
		}
		return out
	}
	return value
}
func (c *Crawler) RunJob(ctx context.Context, j Job) error {
	var payload struct {
		Provider  string          `json:"provider"`
		LocalJSON json.RawMessage `json:"local_json"`
	}
	if e := json.Unmarshal([]byte(j.Payload), &payload); e != nil {
		return e
	}
	provider := j.Provider
	if provider == "" {
		provider = payload.Provider
	}
	if provider == "" {
		return errors.New("provider required")
	}
	p, e := c.provider(ctx, provider)
	if e != nil || !p.Enabled {
		return errors.New("provider disabled or missing")
	}
	u := User{ID: j.Actor}
	if j.Origin == "automatic" {
		u = User{Admin: true}
	} else if e = c.app.DB.QueryRowContext(ctx, "SELECT admin FROM users WHERE id=? AND disabled=0", u.ID).Scan(&u.Admin); e != nil {
		return errors.New("initiating account disabled")
	}
	if !u.Admin {
		return errors.New("metadata job requires administrator permission")
	}
	u.scope = j.RootID
	if j.Origin == "automatic" {
		root, e := c.app.getRoot(ctx, j.RootID)
		if e != nil || !root.AutoCrawl {
			return errors.New("automatic crawl policy disabled or missing")
		}
		allowed := len(root.Providers) == 0
		for _, enabled := range root.Providers {
			if enabled == provider {
				allowed = true
			}
		}
		if !allowed {
			return errors.New("provider no longer enabled for root")
		}
	}
	al, e := c.app.AuthorizedAlias(ctx, u, j.WorkID)
	if e != nil {
		return errors.New("crawl target unavailable")
	}
	if al.IndexStatus == "pending_stability" {
		if j.Type == "auto_crawl" {
			_, _ = c.app.DB.ExecContext(ctx, "UPDATE jobs SET state='paused_dependency',error='Waiting for accepted stable source',updated=? WHERE id=?", now(), j.ID)
			return errJobDeferred
		}
		return errStabilizing
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	case <-ctx.Done():
		return ctx.Err()
	}
	secrets, e := c.credentials(ctx, provider)
	if e != nil {
		return e
	}
	config := map[string]any{}
	if e = json.Unmarshal([]byte(p.Config), &config); e != nil {
		return e
	}
	input := ProviderInput{WorkID: j.WorkID, Title: al.Title, LocalJSON: payload.LocalJSON, Tags: []string{}}
	if provider == "local-json" && len(input.LocalJSON) == 0 {
		rr, e := c.app.checkSource(ctx, al)
		if e != nil {
			return e
		}
		sidecar := al.Path + ".json"
		if al.Kind == "directory" {
			sidecar = strings.TrimSuffix(al.Path, "/") + "/metadata.json"
		}
		input.LocalJSON, e = readSmall(rr, sidecar, 1<<20)
		rr.Close()
		if e != nil {
			return errors.New("local JSON sidecar missing or unreadable")
		}
	}
	_ = c.app.DB.QueryRowContext(ctx, "SELECT source_id,source_url FROM provider_links WHERE work_id=? AND provider=?", j.WorkID, provider).Scan(&input.SourceID, &input.SourceURL)
	metadata, _ := c.Metadata(ctx, u, j.WorkID)
	if tags, ok := metadata["tags"].([]string); ok {
		input.Tags = tags
	}
	if input.SourceURL == "" {
		for _, tag := range input.Tags {
			if strings.HasPrefix(tag, "source:") {
				candidate := strings.TrimPrefix(tag, "source:")
				if !strings.HasPrefix(candidate, "http") {
					candidate = "https://" + candidate
				}
				if sourceID, canonical := canonicalSource(provider, candidate); canonical != "" {
					input.SourceID = sourceID
					input.SourceURL = canonical
					break
				}
			}
		}
	}
	if provider == "nhentai" && input.SourceID == "" {
		if m := nhFilename.FindStringSubmatch(al.Title); m != nil {
			input.SourceID = m[1]
			input.SourceURL = "https://nhentai.net/g/" + m[1] + "/"
		}
	}
	if enabled, _ := config["image_hash"].(bool); enabled && input.SourceURL == "" && (provider == "chaika" || provider == "ehentai") {
		if j.Type == "auto_crawl" && al.IndexStatus != "ready" {
			if al.IndexStatus == "error" {
				return errIndexFailed
			}
			_, _ = c.app.DB.ExecContext(ctx, "UPDATE jobs SET state='paused_dependency',error='Waiting for page index before image matching',updated=? WHERE id=?", now(), j.ID)
			return errJobDeferred
		}
		if e = c.app.ensureIndex(ctx, al); e != nil {
			return e
		}
		pages, e := c.app.pageList(ctx, al)
		if e != nil {
			return e
		}
		if len(pages) > 0 {
			v, e := c.app.loadPage(ctx, u, al, pages[0], Transform{}, true)
			if e != nil {
				return e
			}
			input.SHA1 = firstPageSHA1(v.Bytes)
		}
	}
	hosts := builtinHosts[provider]
	var implementation Provider = builtinProvider(provider)
	if p.Kind == "external" {
		var manifest PluginManifest
		if e = json.Unmarshal([]byte(p.Manifest), &manifest); e != nil {
			return e
		}
		hosts = manifest.Hosts
		implementation = externalProvider{app: c.app, record: p, manifest: manifest}
	}
	c.app.Log.Info("crawl started", "provider", provider, "work", j.WorkID)
	requests := 0
	broker := func(ctx context.Context, method, raw string, b []byte, headers map[string]string) ([]byte, error) {
		requests++
		if requests > 24 {
			return nil, &crawlError{Stage: "request_budget"}
		}
		data, err := c.http.Request(context.WithValue(ctx, helperLogKey{}, c.app.Log.With("provider", provider)), hosts, method, raw, b, headers)
		if err != nil {
			host := ""
			if parsed, parseErr := url.Parse(raw); parseErr == nil {
				host = parsed.Hostname()
			}
			status := 0
			var ce *crawlError
			if errors.As(err, &ce) {
				status = ce.Status
			}
			c.app.Log.Warn("provider request failed", "provider", provider, "host", host, "status", status, "error", err.Error())
		}
		return data, err
	}
	results, e := implementation.Lookup(ctx, input, broker, config, secrets)
	if e != nil {
		stage := "provider"
		var ce *crawlError
		if errors.As(e, &ce) {
			stage = ce.Stage
		}
		_, _ = c.app.DB.ExecContext(ctx, "INSERT INTO provider_errors VALUES(?,?,?,?,?) ON CONFLICT(work_id,provider) DO UPDATE SET stage=excluded.stage,message=excluded.message,failed_at=excluded.failed_at", j.WorkID, provider, stage, "Provider operation failed; prior metadata retained", now())
		if stage == "credentials" {
			_, _ = c.app.DB.ExecContext(ctx, "UPDATE providers SET status='credentials_expired' WHERE id=?", provider)
		}
		return e
	}
	if len(results) > 20 {
		return errors.New("provider returned too many candidates")
	}
	for i := range results {
		r := &results[i]
		normalizeResult(r)
		b, _ := json.Marshal(r)
		var value any
		_ = json.Unmarshal(b, &value)
		clean := redactValue(value, secrets)
		b, _ = json.Marshal(clean)
		if e = json.Unmarshal(b, r); e != nil {
			return e
		}
		if e = validateResult(*r); e != nil {
			return e
		}
		if r.SourceURL != "" {
			parsed, e := url.Parse(r.SourceURL)
			if e != nil || parsed.Scheme != "https" || parsed.User != nil || (provider != "local-json" && !allowDestination(parsed, hosts)) {
				return errors.New("provider returned unapproved source URL")
			}
		}
	}
	if !c.app.authorized(ctx, u, j.WorkID) {
		return errors.New("crawl target permission revoked")
	}
	if j.Origin != "automatic" {
		var allowed int
		if e = c.app.DB.QueryRowContext(ctx, "SELECT count(*) FROM users WHERE id=? AND admin=1 AND disabled=0", j.Actor).Scan(&allowed); e != nil || allowed != 1 {
			return errors.New("metadata write permission revoked")
		}
	}
	strong := false
	if len(results) == 1 {
		r := results[0]
		strong = provider == "local-json" && len(input.LocalJSON) > 0 || input.SourceID != "" && input.SourceID == r.SourceID || input.SourceURL != "" && sameSource(input.SourceURL, r.SourceURL)
	}
	if strong {
		if e = c.commitResult(ctx, j.WorkID, provider, results[0], j.Actor); e != nil {
			return e
		}
	} else {
		tx, e := c.app.DB.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		for _, result := range results {
			b, _ := json.Marshal(result)
			ev, _ := json.Marshal(result.Evidence)
			_, e = tx.Exec("INSERT INTO candidates(id,work_id,provider,source_id,source_url,title,confidence,evidence,result,created) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(work_id,provider,source_id,source_url) DO UPDATE SET title=excluded.title,confidence=excluded.confidence,evidence=excluded.evidence,result=excluded.result WHERE candidates.state='pending'", id(), j.WorkID, provider, result.SourceID, result.SourceURL, result.Fields["title"], result.Confidence, string(ev), string(b), now())
			if e != nil {
				return e
			}
		}
		if e = tx.Commit(); e != nil {
			return e
		}
	}
	result := "none"
	if strong {
		result = "matched"
	} else if len(results) > 0 {
		result = "candidates"
	}
	c.app.Log.Info("crawl finished", "provider", provider, "work", j.WorkID, "result", result)
	_, _ = c.app.DB.ExecContext(ctx, "UPDATE providers SET status='ready' WHERE id=?", provider)
	return nil
}
func sameSource(a, b string) bool { return strings.TrimSuffix(a, "/") == strings.TrimSuffix(b, "/") }
func (a *App) providersHTTP(w http.ResponseWriter, r *http.Request, u User) {
	rows, e := a.DB.QueryContext(r.Context(), "SELECT p.id,p.name,p.version,p.kind,p.enabled,p.status,p.config,EXISTS(SELECT 1 FROM provider_credentials c WHERE c.provider=p.id),p.priority FROM providers p ORDER BY p.priority,p.id")
	if e != nil {
		apiError(w, 503, "database", "Cannot list providers")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	plugins := []map[string]any{}
	for rows.Next() {
		var id, name, version, kind, status, config string
		var enabled, credentials bool
		var priority int
		_ = rows.Scan(&id, &name, &version, &kind, &enabled, &status, &config, &credentials, &priority)
		var settings any
		_ = json.Unmarshal([]byte(config), &settings)
		p := map[string]any{"id": id, "name": name, "version": version, "kind": kind, "enabled": enabled, "status": status, "config": settings, "credential_configured": credentials, "priority": priority}
		out = append(out, p)
		if kind == "external" {
			plugins = append(plugins, p)
		}
	}
	proxy := ""
	if a.Config.ProxyURL != "" {
		proxy = "configured"
	}
	jsonResponse(w, 200, map[string]any{"providers": out, "plugins": plugins, "network": map[string]string{"dns_mode": a.Config.DNSMode, "doh_url": a.Config.DoHURL, "proxy_url": proxy}})
}
func (a *App) providerWrite(w http.ResponseWriter, r *http.Request, u User) {
	provider := r.PathValue("id")
	p, e := a.crawler.provider(r.Context(), provider)
	if e != nil {
		apiError(w, 404, "not_found", "Provider not found")
		return
	}
	var in struct {
		Enabled     *bool             `json:"enabled"`
		Credentials map[string]string `json:"credentials"`
		Clear       bool              `json:"clear_credentials"`
		Config      map[string]any    `json:"config"`
		Priority    *int              `json:"priority"`
	}
	if decodeJSON(w, r, &in) != nil {
		apiError(w, 400, "invalid_request", "Invalid provider settings")
		return
	}
	if in.Priority != nil && (*in.Priority < 0 || *in.Priority > 1000) {
		apiError(w, 400, "invalid_priority", "Priority must be 0–1000 (lower wins)")
		return
	}
	if in.Config != nil && p.Kind == "external" {
		var manifest PluginManifest
		_ = json.Unmarshal([]byte(p.Manifest), &manifest)
		if e = validatePluginConfig(manifest.ConfigSchema, in.Config); e != nil {
			apiError(w, 400, "invalid_config", e.Error())
			return
		}
	}
	var encrypted []byte
	if in.Credentials != nil {
		encrypted, e = a.crawler.encryptCredentials(provider, in.Credentials)
		if e != nil {
			apiError(w, 400, "credential_write", e.Error())
			return
		}
	}
	tx, e := a.DB.BeginTx(r.Context(), nil)
	if e != nil {
		apiError(w, 503, "database", "Cannot update provider")
		return
	}
	defer tx.Rollback()
	if in.Credentials != nil {
		_, e = tx.Exec("INSERT INTO provider_credentials VALUES(?,?) ON CONFLICT(provider) DO UPDATE SET ciphertext=excluded.ciphertext", provider, encrypted)
	}
	if e == nil && in.Clear {
		_, e = tx.Exec("DELETE FROM provider_credentials WHERE provider=?", provider)
	}
	if e == nil && in.Config != nil {
		b, _ := json.Marshal(in.Config)
		_, e = tx.Exec("UPDATE providers SET config=? WHERE id=?", string(b), provider)
	}
	if e == nil && in.Enabled != nil {
		_, e = tx.Exec("UPDATE providers SET enabled=? WHERE id=?", *in.Enabled, provider)
	}
	if e == nil && in.Priority != nil {
		var old int
		e = tx.QueryRow("SELECT priority FROM providers WHERE id=?", provider).Scan(&old)
		if e == nil && old != *in.Priority {
			_, e = tx.Exec("UPDATE providers SET priority=? WHERE id=?", *in.Priority, provider)
			if e == nil {
				_, e = enqueueTx(tx, "metadata_remerge", "", "", u.ID, "manual", "", map[string]string{"provider": provider})
			}
		}
	}
	if e == nil {
		e = audit(tx, u.ID, "provider.configure", "", map[string]string{"provider": provider})
	}
	if e == nil {
		e = a.commitAdmin(tx, u.ID, "provider.configure", "", map[string]string{"provider": provider})
	}
	if e != nil {
		apiError(w, 503, "database", "Cannot update provider")
		return
	}
	if p.Kind == "external" {
		a.Log.Info("plugin updated", "provider", provider, "actor", u.ID)
	}
	a.signal()
	jsonResponse(w, 200, map[string]bool{"ok": true})
}
func (a *App) crawlHTTP(w http.ResponseWriter, r *http.Request, u User) {
	var in struct {
		WorkIDs   []string        `json:"work_ids"`
		RootID    string          `json:"root_id"`
		Query     string          `json:"query"`
		Provider  string          `json:"provider"`
		Preview   bool            `json:"preview"`
		LocalJSON json.RawMessage `json:"local_json"`
	}
	if decodeJSON(w, r, &in) != nil || len(in.WorkIDs) > 1000 {
		apiError(w, 400, "invalid_request", "Invalid crawl selection")
		return
	}
	if _, e := a.crawler.provider(r.Context(), in.Provider); e != nil {
		apiError(w, 400, "invalid_provider", "Select an installed provider")
		return
	}
	count, e := a.targetCount(r.Context(), u, in.RootID, in.WorkIDs, in.Query)
	if e != nil {
		apiError(w, 400, "invalid_query", e.Error())
		return
	}
	if in.Preview {
		jsonResponse(w, 200, map[string]int{"count": count})
		return
	}
	if in.Provider == "local-json" && count != 1 {
		apiError(w, 400, "local_json_scope", "Local JSON import targets exactly one work")
		return
	}
	tx, e := a.DB.BeginTx(r.Context(), nil)
	if e != nil {
		apiError(w, 503, "database", "Cannot enqueue crawl")
		return
	}
	defer tx.Rollback()
	jid, e := enqueueTx(tx, "crawl_bulk", in.RootID, "", u.ID, "manual", "", map[string]any{"work_ids": in.WorkIDs, "query": in.Query, "provider": in.Provider, "local_json": in.LocalJSON})
	if e == nil {
		_, e = tx.Exec("UPDATE jobs SET total=?,provider=? WHERE id=?", count, in.Provider, jid)
	}
	if e == nil {
		e = tx.Commit()
	}
	if e != nil {
		apiError(w, 503, "database", "Cannot enqueue crawl")
		return
	}
	a.signal()
	jsonResponse(w, 202, map[string]string{"id": jid})
}
func (a *App) candidateHTTP(w http.ResponseWriter, r *http.Request, u User) {
	var in struct {
		Action string `json:"action"`
	}
	if decodeJSON(w, r, &in) != nil || (in.Action != "approve" && in.Action != "reject") {
		apiError(w, 400, "invalid_action", "Choose approve or reject")
		return
	}
	var work, provider, payload, state string
	e := a.DB.QueryRowContext(r.Context(), "SELECT work_id,provider,result,state FROM candidates WHERE id=?", r.PathValue("id")).Scan(&work, &provider, &payload, &state)
	if e != nil || state != "pending" || !a.authorized(r.Context(), u, work) {
		apiError(w, 404, "not_found", "Candidate unavailable")
		return
	}
	if in.Action == "approve" {
		var result ProviderResult
		if json.Unmarshal([]byte(payload), &result) != nil {
			apiError(w, 422, "invalid_candidate", "Invalid stored candidate")
			return
		}
		var old string
		_ = a.DB.QueryRowContext(r.Context(), "SELECT source_id FROM provider_links WHERE work_id=? AND provider=?", work, provider).Scan(&old)
		if old != "" && old != result.SourceID {
			apiError(w, 409, "relink_refused", "An existing confirmed source cannot be destructively replaced")
			return
		}
		if e = a.crawler.commitResult(r.Context(), work, provider, result, u.ID); e != nil {
			apiError(w, 422, "merge_failed", "Candidate merge failed")
			return
		}
	}
	_, e = a.DB.ExecContext(r.Context(), "UPDATE candidates SET state=? WHERE id=?", in.Action, r.PathValue("id"))
	if e != nil {
		apiError(w, 503, "database", "Cannot update candidate")
		return
	}
	jsonResponse(w, 200, map[string]bool{"ok": true})
}

var pluginID = regexp.MustCompile(`^[a-z][a-z0-9.-]{0,79}$`)
