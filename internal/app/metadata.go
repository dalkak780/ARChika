package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
)

const crawlerSchema = `
CREATE TABLE IF NOT EXISTS providers(id TEXT PRIMARY KEY,name TEXT NOT NULL,version TEXT NOT NULL,kind TEXT NOT NULL,enabled INTEGER NOT NULL DEFAULT 1,priority INTEGER NOT NULL DEFAULT 100,manifest TEXT NOT NULL DEFAULT '{}',executable TEXT NOT NULL DEFAULT '',config TEXT NOT NULL DEFAULT '{}',status TEXT NOT NULL DEFAULT 'ready');
CREATE TABLE IF NOT EXISTS provider_credentials(provider TEXT PRIMARY KEY REFERENCES providers(id) ON DELETE CASCADE,ciphertext BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS provider_links(work_id TEXT NOT NULL REFERENCES works(id) ON DELETE CASCADE,provider TEXT NOT NULL,source_id TEXT NOT NULL,source_url TEXT NOT NULL,confirmed_at INTEGER NOT NULL,PRIMARY KEY(work_id,provider));
CREATE TABLE IF NOT EXISTS provider_snapshots(work_id TEXT NOT NULL REFERENCES works(id) ON DELETE CASCADE,provider TEXT NOT NULL,source_id TEXT NOT NULL,source_url TEXT NOT NULL,fields TEXT NOT NULL,tags TEXT NOT NULL,raw TEXT NOT NULL,fetched_at INTEGER NOT NULL,version TEXT NOT NULL,evidence TEXT NOT NULL,PRIMARY KEY(work_id,provider));
CREATE TABLE IF NOT EXISTS provider_history(id TEXT PRIMARY KEY,work_id TEXT NOT NULL REFERENCES works(id) ON DELETE CASCADE,provider TEXT NOT NULL,raw TEXT NOT NULL,fetched_at INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS provider_history_work ON provider_history(work_id,provider,fetched_at DESC);
CREATE TABLE IF NOT EXISTS provider_errors(work_id TEXT NOT NULL REFERENCES works(id) ON DELETE CASCADE,provider TEXT NOT NULL,stage TEXT NOT NULL,message TEXT NOT NULL,failed_at INTEGER NOT NULL,PRIMARY KEY(work_id,provider));
CREATE TABLE IF NOT EXISTS manual_fields(work_id TEXT NOT NULL REFERENCES works(id) ON DELETE CASCADE,field TEXT NOT NULL,value TEXT NOT NULL,PRIMARY KEY(work_id,field));
CREATE TABLE IF NOT EXISTS tag_assertions(work_id TEXT NOT NULL REFERENCES works(id) ON DELETE CASCADE,provider TEXT NOT NULL,tag TEXT NOT NULL,PRIMARY KEY(work_id,provider,tag));
CREATE TABLE IF NOT EXISTS tag_decisions(work_id TEXT NOT NULL REFERENCES works(id) ON DELETE CASCADE,tag TEXT NOT NULL,decision INTEGER NOT NULL,PRIMARY KEY(work_id,tag));
CREATE TABLE IF NOT EXISTS effective_fields(work_id TEXT NOT NULL REFERENCES works(id) ON DELETE CASCADE,field TEXT NOT NULL,value TEXT NOT NULL,origin TEXT NOT NULL,manual INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(work_id,field));
CREATE TABLE IF NOT EXISTS candidates(id TEXT PRIMARY KEY,work_id TEXT NOT NULL REFERENCES works(id) ON DELETE CASCADE,provider TEXT NOT NULL,source_id TEXT NOT NULL,source_url TEXT NOT NULL,title TEXT NOT NULL,confidence REAL NOT NULL,evidence TEXT NOT NULL,result TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'pending',created INTEGER NOT NULL,UNIQUE(work_id,provider,source_id,source_url));
`

type FieldValue struct {
	Value  string `json:"value"`
	Origin string `json:"origin"`
	Manual bool   `json:"manual"`
}

func mergeMetadata(tx *sql.Tx, work string) error {
	if _, e := tx.Exec("DELETE FROM effective_fields WHERE work_id=?", work); e != nil {
		return e
	}
	if _, e := tx.Exec("INSERT INTO effective_fields(work_id,field,value,origin,manual) SELECT work_id,field,value,'manual',1 FROM manual_fields WHERE work_id=?", work); e != nil {
		return e
	}
	rows, e := tx.Query("SELECT s.provider,s.fields FROM provider_snapshots s LEFT JOIN providers p ON p.id=s.provider WHERE s.work_id=? ORDER BY COALESCE(p.priority,1000),s.provider", work)
	if e != nil {
		return e
	}
	type snapshot struct {
		provider string
		fields   map[string]string
	}
	snapshots := []snapshot{}
	for rows.Next() {
		var p, fields string
		if e = rows.Scan(&p, &fields); e != nil {
			rows.Close()
			return e
		}
		var data map[string]string
		if e = json.Unmarshal([]byte(fields), &data); e != nil {
			rows.Close()
			return e
		}
		snapshots = append(snapshots, snapshot{p, data})
	}
	if e = rows.Close(); e != nil {
		return e
	}
	for _, s := range snapshots {
		for k, v := range s.fields {
			if _, e = tx.Exec("INSERT OR IGNORE INTO effective_fields VALUES(?,?,?,?,0)", work, k, v, s.provider); e != nil {
				return e
			}
		}
	}
	if _, e = tx.Exec("DELETE FROM effective_tags WHERE work_id=?", work); e != nil {
		return e
	}
	if _, e = tx.Exec("INSERT OR IGNORE INTO effective_tags SELECT work_id,tag FROM tag_assertions a WHERE work_id=? AND NOT EXISTS(SELECT 1 FROM tag_decisions d WHERE d.work_id=a.work_id AND d.tag=a.tag AND d.decision=-1)", work); e != nil {
		return e
	}
	if _, e = tx.Exec("INSERT OR IGNORE INTO effective_tags SELECT work_id,tag FROM tag_decisions WHERE work_id=? AND decision=1", work); e != nil {
		return e
	}
	words := []string{}
	rs, e := tx.Query("SELECT value FROM effective_fields WHERE work_id=? ORDER BY field", work)
	if e != nil {
		return e
	}
	for rs.Next() {
		var value string
		_ = rs.Scan(&value)
		words = append(words, value)
	}
	rs.Close()
	rs, e = tx.Query("SELECT tag FROM effective_tags WHERE work_id=? ORDER BY tag", work)
	if e != nil {
		return e
	}
	for rs.Next() {
		var tag string
		_ = rs.Scan(&tag)
		words = append(words, tag)
	}
	rs.Close()
	for _, table := range []string{"metadata_words", "metadata_substrings"} {
		if _, e = tx.Exec("DELETE FROM "+table+" WHERE work_id=?", work); e != nil {
			return e
		}
		if _, e = tx.Exec("INSERT INTO "+table+"(work_id,text) VALUES(?,?)", work, normalizeSearch(strings.Join(words, " "))); e != nil {
			return e
		}
	}
	return bump(tx)
}
func (c *Crawler) Metadata(ctx context.Context, u User, work string) (map[string]any, error) {
	al, e := c.app.AuthorizedAlias(ctx, u, work)
	if e != nil {
		return nil, e
	}
	db := c.app.DB
	fields := map[string]FieldValue{"display_title": {Value: al.Title, Origin: "filename"}}
	rows, e := db.QueryContext(ctx, "SELECT field,value,origin,manual FROM effective_fields WHERE work_id=?", work)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var field string
		var v FieldValue
		if e = rows.Scan(&field, &v.Value, &v.Origin, &v.Manual); e != nil {
			rows.Close()
			return nil, e
		}
		fields[field] = v
	}
	rows.Close()
	tags := []string{}
	rows, e = db.QueryContext(ctx, "SELECT tag FROM effective_tags WHERE work_id=? ORDER BY tag", work)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var tag string
		_ = rows.Scan(&tag)
		tags = append(tags, tag)
	}
	rows.Close()
	var filename string
	_ = db.QueryRowContext(ctx, "SELECT filename_tags FROM aliases WHERE id=?", al.ID).Scan(&filename)
	var ft []string
	_ = json.Unmarshal([]byte(filename), &ft)
	tags = append(tags, ft...)
	sort.Strings(tags)
	tags = dedupStrings(tags)
	providers := []map[string]any{}
	rows, e = db.QueryContext(ctx, "SELECT provider,source_id,source_url,fields,tags,fetched_at,version,evidence FROM provider_snapshots WHERE work_id=? ORDER BY provider", work)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var p, sid, url, f, t, version, ev string
		var at int64
		_ = rows.Scan(&p, &sid, &url, &f, &t, &at, &version, &ev)
		var ff map[string]string
		var tt, ee []string
		_ = json.Unmarshal([]byte(f), &ff)
		_ = json.Unmarshal([]byte(t), &tt)
		_ = json.Unmarshal([]byte(ev), &ee)
		providers = append(providers, map[string]any{"id": p, "provider": p, "source_id": sid, "source_url": url, "fields": ff, "tags": tt, "fetched_at": at, "version": version, "evidence": ee})
	}
	rows.Close()
	candidates := []map[string]any{}
	if u.Admin {
		rows, e = db.QueryContext(ctx, "SELECT id,provider,source_id,source_url,title,confidence,evidence FROM candidates WHERE work_id=? AND state='pending' ORDER BY confidence DESC LIMIT 20", work)
		if e != nil {
			return nil, e
		}
		for rows.Next() {
			var id, p, sid, url, title, evidence string
			var confidence float64
			_ = rows.Scan(&id, &p, &sid, &url, &title, &confidence, &evidence)
			var ev []string
			_ = json.Unmarshal([]byte(evidence), &ev)
			candidates = append(candidates, map[string]any{"id": id, "provider": p, "source_id": sid, "source_url": url, "title": title, "confidence": confidence, "evidence": ev})
		}
		rows.Close()
	}
	return map[string]any{"fields": fields, "tags": tags, "providers": providers, "candidates": candidates}, nil
}
func (a *App) metadataHTTP(w http.ResponseWriter, r *http.Request, u User) {
	work := r.PathValue("id")
	if !a.authorized(r.Context(), u, work) {
		apiError(w, 404, "not_found", "Work unavailable")
		return
	}
	if r.Method == "GET" {
		m, e := a.crawler.Metadata(r.Context(), u, work)
		if e != nil {
			apiError(w, 503, "database", "Cannot read metadata")
			return
		}
		jsonResponse(w, 200, m)
		return
	}
	if !u.Admin {
		apiError(w, 403, "forbidden", "Administrator required to edit metadata")
		return
	}
	var in struct {
		Fields      map[string]*string `json:"fields"`
		ResetFields []string           `json:"reset_fields"`
		AddTags     []string           `json:"add_tags"`
		RemoveTags  []string           `json:"remove_tags"`
		ResetTags   []string           `json:"reset_tags"`
	}
	if decodeJSON(w, r, &in) != nil || len(in.Fields) > 32 || len(in.AddTags)+len(in.RemoveTags)+len(in.ResetTags) > 1000 {
		apiError(w, 400, "invalid_metadata", "Invalid or oversized metadata update")
		return
	}
	for k, v := range in.Fields {
		if !allowedField(k) || (v != nil && len(*v) > 65536) {
			apiError(w, 400, "invalid_field", "Unsupported or oversized metadata field")
			return
		}
	}
	tx, e := a.DB.BeginTx(r.Context(), nil)
	if e != nil {
		apiError(w, 503, "database", "Cannot update metadata")
		return
	}
	defer tx.Rollback()
	for k, v := range in.Fields {
		value := ""
		if v != nil {
			value = *v
		}
		_, e = tx.Exec("INSERT INTO manual_fields VALUES(?,?,?) ON CONFLICT(work_id,field) DO UPDATE SET value=excluded.value", work, k, value)
		if e != nil {
			break
		}
	}
	if e == nil {
		for _, k := range in.ResetFields {
			_, e = tx.Exec("DELETE FROM manual_fields WHERE work_id=? AND field=?", work, k)
			if e != nil {
				break
			}
		}
	}
	if e == nil {
		for _, pair := range []struct {
			tags     []string
			decision int
		}{{in.AddTags, 1}, {in.RemoveTags, -1}, {in.ResetTags, 0}} {
			for _, raw := range pair.tags {
				tag := normalizeTag(raw)
				if tag == "" || len(tag) > 300 {
					e = errors.New("invalid tag")
					break
				}
				if pair.decision == 0 {
					_, e = tx.Exec("DELETE FROM tag_decisions WHERE work_id=? AND tag=?", work, tag)
				} else {
					_, e = tx.Exec("INSERT INTO tag_decisions VALUES(?,?,?) ON CONFLICT(work_id,tag) DO UPDATE SET decision=excluded.decision", work, tag, pair.decision)
				}
				if e != nil {
					break
				}
			}
			if e != nil {
				break
			}
		}
	}
	if e == nil {
		e = mergeMetadata(tx, work)
	}
	if e == nil {
		e = audit(tx, u.ID, "metadata.edit", "", map[string]string{"work": work})
	}
	if e == nil {
		e = a.commitAdmin(tx, u.ID, "metadata.edit", "", map[string]string{"work": work})
	}
	if e != nil {
		apiError(w, 400, "invalid_metadata", "Metadata update rejected")
		return
	}
	jsonResponse(w, 200, map[string]bool{"ok": true})
}
func allowedField(k string) bool {
	switch k {
	case "title", "title_japanese", "description", "creators", "language", "publication", "published_at", "source_url", "category", "uploader":
		return true
	}
	return false
}
func dedupStrings(in []string) []string {
	out := in[:0]
	for _, s := range in {
		if len(out) == 0 || s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	return out
}
func (c *Crawler) commitResult(ctx context.Context, work, provider string, result ProviderResult, actor string) error {
	if e := validateResult(result); e != nil {
		return e
	}
	fields, _ := json.Marshal(result.Fields)
	tags, _ := json.Marshal(result.Tags)
	raw, _ := json.Marshal(result.Raw)
	if len(raw) > 1<<20 {
		return errors.New("raw provider payload exceeds limit")
	}
	evidence, _ := json.Marshal(result.Evidence)
	tx, e := c.app.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	_, e = tx.Exec("INSERT INTO provider_snapshots VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(work_id,provider) DO UPDATE SET source_id=excluded.source_id,source_url=excluded.source_url,fields=excluded.fields,tags=excluded.tags,raw=excluded.raw,fetched_at=excluded.fetched_at,version=excluded.version,evidence=excluded.evidence", work, provider, result.SourceID, result.SourceURL, string(fields), string(tags), string(raw), now(), result.Version, string(evidence))
	if e != nil {
		return e
	}
	_, e = tx.Exec("INSERT INTO provider_links VALUES(?,?,?,?,?) ON CONFLICT(work_id,provider) DO UPDATE SET source_id=excluded.source_id,source_url=excluded.source_url,confirmed_at=excluded.confirmed_at", work, provider, result.SourceID, result.SourceURL, now())
	if e != nil {
		return e
	}
	_, e = tx.Exec("DELETE FROM tag_assertions WHERE work_id=? AND provider=?", work, provider)
	if e != nil {
		return e
	}
	for _, tag := range result.Tags {
		_, e = tx.Exec("INSERT OR IGNORE INTO tag_assertions VALUES(?,?,?)", work, provider, normalizeTag(tag))
		if e != nil {
			return e
		}
	}
	_, e = tx.Exec("DELETE FROM provider_errors WHERE work_id=? AND provider=?", work, provider)
	if e != nil {
		return e
	}
	_, e = tx.Exec("INSERT INTO provider_history VALUES(?,?,?,?,?)", id(), work, provider, string(raw), now())
	if e != nil {
		return e
	}
	_, e = tx.Exec("DELETE FROM provider_history WHERE work_id=? AND provider=? AND id NOT IN(SELECT id FROM provider_history WHERE work_id=? AND provider=? ORDER BY fetched_at DESC,id DESC LIMIT 3)", work, provider, work, provider)
	if e != nil {
		return e
	}
	if e = mergeMetadata(tx, work); e != nil {
		return e
	}
	if e = audit(tx, actor, "metadata.provider_commit", "", map[string]string{"work": work, "provider": provider}); e != nil {
		return e
	}
	return c.app.commitAdmin(tx, actor, "metadata.provider_commit", "", map[string]string{"work": work, "provider": provider})
}
