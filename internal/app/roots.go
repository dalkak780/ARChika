package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const rootColumns = "id,COALESCE(bootstrap_key,''),name,path,watch,auto_crawl,providers,quiet_seconds,parser_enabled,parser_regex,status,pending_path,warning,trust_identity"

func scanRoot(s interface{ Scan(...any) error }) (Root, error) {
	var r Root
	var p string
	e := s.Scan(&r.ID, &r.Key, &r.Name, &r.Path, &r.Watch, &r.AutoCrawl, &p, &r.QuietSeconds, &r.ParserEnabled, &r.ParserRegex, &r.Status, &r.PendingPath, &r.Warning, &r.TrustIdentity)
	_ = json.Unmarshal([]byte(p), &r.Providers)
	if r.Providers == nil {
		r.Providers = []string{}
	}
	return r, e
}
func (a *App) getRoot(ctx context.Context, rid string) (Root, error) {
	return scanRoot(a.DB.QueryRowContext(ctx, "SELECT "+rootColumns+" FROM roots WHERE id=?", rid))
}
func (a *App) roots(w http.ResponseWriter, r *http.Request, u User) {
	q := "SELECT " + rootColumns + " FROM roots"
	args := []any{}
	if !u.Admin {
		q += " WHERE id IN (SELECT root_id FROM grants WHERE user_id=?)"
		args = append(args, u.ID)
	}
	q += " ORDER BY id"
	rows, e := a.DB.QueryContext(r.Context(), q, args...)
	if e != nil {
		apiError(w, 503, "database", "Cannot list roots")
		return
	}
	defer rows.Close()
	out := []Root{}
	for rows.Next() {
		root, e := scanRoot(rows)
		if e != nil {
			apiError(w, 503, "database", "Cannot read roots")
			return
		}
		if !u.Admin {
			root.Path = ""
			root.PendingPath = ""
			root.Key = ""
			root.Warning = ""
			root.ParserRegex = ""
		}
		out = append(out, root)
	}
	jsonResponse(w, 200, out)
}

const aliasColumns = "al.id,w.id,al.root_id,r.name,al.path,r.path,al.original_name,al.title,w.kind,w.size,w.mtime,w.version,w.registered_at,w.index_status,w.error_code,COALESCE(w.physical_key,''),w.cover,w.cover_unresolved,COALESCE(NULLIF(w.modified_at,0),w.mtime)"

func scanAlias(s interface{ Scan(...any) error }) (Alias, error) {
	var v Alias
	e := s.Scan(&v.ID, &v.WorkID, &v.RootID, &v.RootName, &v.Path, &v.RootPath, &v.OriginalName, &v.Title, &v.Kind, &v.Size, &v.Mtime, &v.Version, &v.RegisteredAt, &v.IndexStatus, &v.ErrorCode, &v.PhysicalKey, &v.Cover, &v.CoverUnresolved, &v.ModifiedAt)
	return v, e
}
func (a *App) AuthorizedAlias(ctx context.Context, u User, work string) (Alias, error) {
	return scanAlias(a.DB.QueryRowContext(ctx, "SELECT "+aliasColumns+" FROM works w JOIN aliases al ON al.work_id=w.id JOIN roots r ON r.id=al.root_id WHERE w.id=? AND al.status='online' AND r.status='online' AND (? OR EXISTS(SELECT 1 FROM users account WHERE account.id=? AND account.disabled=0 AND (account.admin=1 OR EXISTS(SELECT 1 FROM grants g WHERE g.user_id=account.id AND g.root_id=al.root_id)))) AND (?='' OR al.root_id=?) ORDER BY r.id,al.path LIMIT 1", work, u.Admin && u.ID == "", u.ID, u.scope, u.scope))
}
func (a *App) authorized(ctx context.Context, u User, work string) bool {
	_, e := a.AuthorizedAlias(ctx, u, work)
	return e == nil
}
func (a *App) rootWrite(w http.ResponseWriter, r *http.Request, u User) {
	var in Root
	if e := decodeJSON(w, r, &in); e != nil {
		apiError(w, 400, "invalid_request", e.Error())
		return
	}
	creating := r.Method == "POST"
	old := Root{}
	var e error
	if !creating {
		old, e = a.getRoot(r.Context(), r.PathValue("id"))
		if e != nil {
			apiError(w, 404, "not_found", "Root not found")
			return
		}
		in.ID = old.ID
		in.PendingPath = old.PendingPath
		in.Warning = old.Warning
		if in.Key == "" {
			in.Key = old.Key
		}
		if in.Path == "" {
			in.Path = old.Path
		}
		if in.Name == "" {
			in.Name = old.Name
		}
	} else {
		in.ID = id()
	}
	if !creating && in.Key != old.Key {
		apiError(w, 400, "immutable_key", "A root bootstrap key cannot be changed")
		return
	}
	if in.Key == "" || !validName(in.Name) || len(in.Key) > 80 || len(in.ParserRegex) > 4096 {
		apiError(w, 400, "invalid_root", "Root key and name are required")
		return
	}
	if in.QuietSeconds == 0 {
		in.QuietSeconds = a.Config.QuietSeconds
	}
	if in.QuietSeconds < 1 || in.QuietSeconds > 86400 {
		apiError(w, 400, "invalid_root", "Quiet interval must be 1–86400 seconds")
		return
	}
	if _, e = regexp.Compile(in.ParserRegex); e != nil {
		apiError(w, 400, "invalid_regex", "Invalid RE2 parser expression")
		return
	}
	p, e := a.Config.RootPath(in.Path)
	if e != nil {
		apiError(w, 400, "invalid_root", e.Error())
		return
	}
	in.Path = p
	var findings []map[string]any
	pending := false
	if !creating && in.Path != old.Path {
		ok, f := a.verifyRelocation(r.Context(), old, in.Path)
		findings = f
		if !ok {
			pending = true
			in.PendingPath = in.Path
			in.Path = old.Path
			in.Warning = "Target awaits reconciliation or explicit force approval"
		} else {
			in.PendingPath = ""
			in.Warning = ""
		}
	}
	if in.Providers == nil {
		in.Providers = []string{}
	}
	if len(in.Providers) > 32 {
		apiError(w, 400, "invalid_root", "Too many providers")
		return
	}
	pb, _ := json.Marshal(in.Providers)
	tx, e := a.DB.BeginTx(r.Context(), nil)
	if e != nil {
		apiError(w, 503, "database", "Cannot update root")
		return
	}
	defer tx.Rollback()
	if creating {
		_, e = tx.Exec("INSERT INTO roots(id,bootstrap_key,name,path,watch,auto_crawl,providers,quiet_seconds,parser_enabled,parser_regex,trust_identity) VALUES(?,?,?,?,?,?,?,?,?,?,?)", in.ID, in.Key, in.Name, in.Path, in.Watch, in.AutoCrawl, string(pb), in.QuietSeconds, in.ParserEnabled, in.ParserRegex, in.TrustIdentity)
	} else {
		_, e = tx.Exec("UPDATE roots SET name=?,path=?,watch=?,auto_crawl=?,providers=?,quiet_seconds=?,parser_enabled=?,parser_regex=?,pending_path=?,warning=?,trust_identity=? WHERE id=?", in.Name, in.Path, in.Watch, in.AutoCrawl, string(pb), in.QuietSeconds, in.ParserEnabled, in.ParserRegex, in.PendingPath, in.Warning, in.TrustIdentity, in.ID)
	}
	if e == nil && !creating && (in.ParserEnabled != old.ParserEnabled || in.ParserRegex != old.ParserRegex) {
		_, e = enqueueTx(tx, "filename_reparse", in.ID, "", u.ID, "manual", "", nil)
	}
	if e == nil {
		e = bump(tx)
	}
	if e == nil {
		e = audit(tx, u.ID, "root.update", in.ID, map[string]any{"former": old.Path, "path": in.Path, "pending": in.PendingPath, "findings": findings})
	}
	if e == nil && !creating && in.Path != old.Path && !pending {
		_, e = tx.Exec("UPDATE roots SET status='online',checked_at=? WHERE id=?", now(), in.ID)
	}
	if e == nil {
		e = tx.Commit()
	}
	if e != nil {
		apiError(w, 409, "root_conflict", "Root key or path conflicts with an existing root")
		return
	}
	a.cancelPrefetch("")
	a.notifyWatch()
	result, _ := a.getRoot(r.Context(), in.ID)
	jsonResponse(w, 200, map[string]any{"root": result, "pending": pending, "findings": findings})
}
func (a *App) verifyRelocation(ctx context.Context, root Root, p string) (bool, []map[string]any) {
	f, e := os.OpenRoot(p)
	if e != nil {
		return false, []map[string]any{{"error": "target cannot be opened"}}
	}
	defer f.Close()
	rows, e := a.DB.QueryContext(ctx, "SELECT path,size,mtime,COALESCE(physical_key,'') FROM aliases WHERE root_id=? AND status='online' ORDER BY path LIMIT 16", root.ID)
	if e != nil {
		return false, nil
	}
	type sample struct {
		path        string
		size, mtime int64
		physical    string
	}
	samples := []sample{}
	for rows.Next() {
		var s sample
		_ = rows.Scan(&s.path, &s.size, &s.mtime, &s.physical)
		samples = append(samples, s)
	}
	rows.Close()
	findings := []map[string]any{}
	ok := len(samples) > 0
	for _, s := range samples {
		stat, e := f.Stat(s.path)
		same := e == nil && stat.Size() == s.size && stat.ModTime().UnixNano() == s.mtime
		identity := false
		if e == nil && s.physical != "" {
			identity = physicalKey(stat, p, s.path) == s.physical
		}
		if !same || (s.physical != "" && !identity) {
			ok = false
		}
		findings = append(findings, map[string]any{"relative_path": s.path, "metadata_matches": same, "identity_matches": identity})
	}
	if len(samples) == 0 {
		findings = append(findings, map[string]any{"reason": "No indexed sample; equivalence is inconclusive"})
	}
	return ok, findings
}
func (a *App) relocation(w http.ResponseWriter, r *http.Request, u User) {
	var in struct {
		Action  string `json:"action"`
		Confirm bool   `json:"confirm"`
	}
	if e := decodeJSON(w, r, &in); e != nil || !in.Confirm {
		apiError(w, 400, "confirmation_required", "Explicit confirmation required")
		return
	}
	root, e := a.getRoot(r.Context(), r.PathValue("id"))
	if e != nil || root.PendingPath == "" {
		apiError(w, 404, "not_found", "No pending relocation")
		return
	}
	p, e := a.Config.RootPath(root.PendingPath)
	if e != nil {
		apiError(w, 400, "invalid_root", e.Error())
		return
	}
	_, findings := a.verifyRelocation(r.Context(), root, p)
	if in.Action == "reconcile" {
		tx, e := a.DB.BeginTx(r.Context(), nil)
		if e != nil {
			apiError(w, 503, "database", "Cannot schedule reconciliation")
			return
		}
		defer tx.Rollback()
		_, e = tx.Exec("UPDATE roots SET status='offline',warning='Target reconciliation in progress' WHERE id=?", root.ID)
		jid := ""
		if e == nil {
			jid, e = enqueueTx(tx, "reconcile", root.ID, "", u.ID, "manual", "", map[string]any{"target_path": p})
		}
		if e == nil {
			e = audit(tx, u.ID, "root.reconcile", root.ID, map[string]any{"old": root.Path, "new": p, "findings": findings})
		}
		if e == nil {
			e = bump(tx)
		}
		if e == nil {
			e = tx.Commit()
		}
		if e != nil {
			apiError(w, 503, "database", "Cannot schedule reconciliation")
			return
		}
		a.cancelPrefetch("")
		a.signal()
		jsonResponse(w, 200, map[string]any{"ok": true, "job_id": jid})
		return
	}
	if in.Action != "force" {
		apiError(w, 400, "invalid_action", "Choose reconcile or force")
		return
	}
	tx, e := a.DB.BeginTx(r.Context(), nil)
	if e != nil {
		apiError(w, 503, "database", "Cannot approve relocation")
		return
	}
	defer tx.Rollback()
	_, e = tx.Exec("UPDATE roots SET path=?,pending_path='',status='online',warning='Force approved; indexed files require targeted verification' WHERE id=?", p, root.ID)
	if e == nil {
		e = audit(tx, u.ID, "root.force_approve", root.ID, map[string]any{"old": root.Path, "new": p, "findings": findings, "mode": "force"})
	}
	if e == nil {
		e = bump(tx)
	}
	if e == nil {
		e = tx.Commit()
	}
	if e != nil {
		apiError(w, 503, "database", "Cannot approve relocation")
		return
	}
	a.cancelPrefetch("")
	a.notifyWatch()
	jsonResponse(w, 200, map[string]bool{"ok": true})
}
func (a *App) rootDelete(w http.ResponseWriter, r *http.Request, u User) {
	rid := r.PathValue("id")
	var aliases, works, grants int
	_ = a.DB.QueryRowContext(r.Context(), "SELECT count(*),count(DISTINCT work_id) FROM aliases WHERE root_id=?", rid).Scan(&aliases, &works)
	_ = a.DB.QueryRowContext(r.Context(), "SELECT count(*) FROM grants WHERE root_id=?", rid).Scan(&grants)
	if r.URL.Query().Get("preview") == "true" {
		jsonResponse(w, 200, map[string]int{"aliases": aliases, "works": works, "grants": grants})
		return
	}
	var in struct {
		Confirm bool `json:"confirm"`
	}
	if decodeJSON(w, r, &in) != nil || !in.Confirm {
		apiError(w, 400, "confirmation_required", "Preview before removing root index mappings; originals remain unchanged")
		return
	}
	tx, e := a.DB.BeginTx(r.Context(), nil)
	if e != nil {
		apiError(w, 503, "database", "Cannot remove root")
		return
	}
	defer tx.Rollback()
	_, e = tx.Exec("DELETE FROM roots WHERE id=?", rid)
	if e == nil {
		_, e = tx.Exec("DELETE FROM works WHERE NOT EXISTS(SELECT 1 FROM aliases WHERE work_id=works.id)")
	}
	if e == nil {
		e = bump(tx)
	}
	if e == nil {
		e = audit(tx, u.ID, "root.remove", rid, map[string]int{"aliases": aliases, "grants": grants})
	}
	if e == nil {
		e = tx.Commit()
	}
	if e != nil {
		apiError(w, 503, "database", "Cannot remove root")
		return
	}
	a.cancelPrefetch("")
	a.notifyWatch()
	jsonResponse(w, 200, map[string]bool{"ok": true})
}
func (a *App) configPreview(w http.ResponseWriter, r *http.Request, u User) {
	changes := []map[string]any{}
	for _, root := range a.Config.Roots {
		var rid, name, path, pending string
		var watch, autoCrawl bool
		var quiet int
		e := a.DB.QueryRowContext(r.Context(), "SELECT id,name,path,COALESCE(pending_path,''),watch,auto_crawl,quiet_seconds FROM roots WHERE bootstrap_key=?", root.Key).Scan(&rid, &name, &path, &pending, &watch, &autoCrawl, &quiet)
		action := "update"
		if e == sql.ErrNoRows {
			action = "add"
		} else if e == nil {
			want := root.QuietSeconds
			if want < 1 {
				want = a.Config.QuietSeconds
			}
			resolved, pe := a.Config.RootPath(root.Path)
			if pe == nil && name == root.Name && (resolved == path || resolved == pending) && watch == root.Watch && autoCrawl == root.AutoCrawl && quiet == want {
				continue
			}
		}
		changes = append(changes, map[string]any{"key": root.Key, "name": root.Name, "path": root.Path, "existing_id": rid, "action": action})
	}
	jsonResponse(w, 200, map[string]any{"changes": changes})
}
func (a *App) configApply(w http.ResponseWriter, r *http.Request, u User) {
	var in struct {
		Keys []string `json:"keys"`
	}
	if decodeJSON(w, r, &in) != nil || len(in.Keys) > 1000 {
		apiError(w, 400, "invalid_request", "Select bounded bootstrap keys")
		return
	}
	selected := map[string]bool{}
	for _, k := range in.Keys {
		selected[k] = true
	}
	type prepared struct {
		r RootConfig
		p string
	}
	changes := []prepared{}
	for _, root := range a.Config.Roots {
		if !selected[root.Key] {
			continue
		}
		p, e := a.Config.RootPath(root.Path)
		if e != nil {
			apiError(w, 400, "invalid_root", e.Error())
			return
		}
		changes = append(changes, prepared{root, p})
	}
	if len(changes) != len(selected) {
		apiError(w, 400, "unknown_key", "Unknown bootstrap key")
		return
	}
	tx, e := a.DB.BeginTx(r.Context(), nil)
	if e != nil {
		apiError(w, 503, "database", "Cannot import configuration")
		return
	}
	defer tx.Rollback()
	for _, v := range changes {
		var rid, oldPath string
		lookup := tx.QueryRow("SELECT id,path FROM roots WHERE bootstrap_key=?", v.r.Key).Scan(&rid, &oldPath)
		quiet := v.r.QuietSeconds
		if quiet < 1 {
			quiet = a.Config.QuietSeconds
		}
		if lookup == sql.ErrNoRows {
			_, e = tx.Exec("INSERT INTO roots(id,bootstrap_key,name,path,watch,auto_crawl,quiet_seconds) VALUES(?,?,?,?,?,?,?)", id(), v.r.Key, v.r.Name, v.p, v.r.Watch, v.r.AutoCrawl, quiet)
		} else if lookup == nil {
			if oldPath != v.p {
				_, e = tx.Exec("UPDATE roots SET name=?,watch=?,auto_crawl=?,quiet_seconds=?,pending_path=?,warning='Configuration relocation awaits verification' WHERE id=?", v.r.Name, v.r.Watch, v.r.AutoCrawl, quiet, v.p, rid)
			} else {
				_, e = tx.Exec("UPDATE roots SET name=?,watch=?,auto_crawl=?,quiet_seconds=? WHERE id=?", v.r.Name, v.r.Watch, v.r.AutoCrawl, quiet, rid)
			}
		} else {
			e = lookup
		}
		if e != nil {
			break
		}
	}
	if e == nil {
		e = bump(tx)
	}
	if e == nil {
		e = audit(tx, u.ID, "config.apply", "", in.Keys)
	}
	if e == nil {
		e = tx.Commit()
	}
	if e != nil {
		apiError(w, 409, "config_conflict", "Configuration import rejected")
		return
	}
	a.cancelPrefetch("")
	a.notifyWatch()
	jsonResponse(w, 200, map[string]bool{"ok": true})
}
func (a *App) checkSource(ctx context.Context, al Alias) (*os.Root, error) {
	root, e := os.OpenRoot(al.RootPath)
	if e != nil {
		_, _ = a.DB.ExecContext(ctx, "UPDATE roots SET status='offline',checked_at=? WHERE id=?", now(), al.RootID)
		return nil, errors.New("root_offline")
	}
	source, e := root.Open(al.Path)
	var st os.FileInfo
	if e == nil {
		st, e = source.Stat()
		_ = source.Close()
	}
	if e != nil {
		record, lookupErr := a.getRoot(ctx, al.RootID)
		if lookupErr != nil {
			root.Close()
			return nil, lookupErr
		}
		if probeErr := a.requireRootWitness(ctx, record, root); probeErr != nil {
			root.Close()
			return nil, probeErr
		}
		root.Close()
		if errors.Is(e, os.ErrNotExist) || errors.Is(e, os.ErrPermission) {
			_, _ = a.DB.ExecContext(ctx, "UPDATE aliases SET status='offline' WHERE id=?", al.ID)
		}
		return nil, errors.New("source_unavailable")
	}
	currentIdentity := physicalKey(st, al.RootPath, al.Path)
	identityChanged := al.PhysicalKey != "" && currentIdentity != "" && !sameObjectKey(al.PhysicalKey, currentIdentity)
	if identityChanged || al.Kind != "directory" && (st.Size() != al.Size || st.ModTime().UnixNano() != al.Mtime) {
		root.Close()
		_ = a.changedSource(ctx, al, st, identityChanged)
		return nil, errors.New("source_changed")
	}
	return root, nil
}
func readSmall(root *os.Root, path string, limit int64) ([]byte, error) {
	f, e := root.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, limit+1))
	if e == nil && int64(len(b)) > limit {
		return nil, errors.New("file exceeds limit")
	}
	return b, e
}
func basenameTitle(path, kind string) (string, string) {
	n := filepath.Base(path)
	if kind == "directory" {
		return n, n
	}
	ext := filepath.Ext(n)
	return n, strings.TrimSuffix(n, ext)
}
