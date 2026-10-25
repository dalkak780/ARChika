package app

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
)

func (a *App) missingEligible(ctx context.Context, work string) bool {
	rows, e := a.DB.QueryContext(ctx, "SELECT r.path,al.path,r.status,r.id FROM aliases al JOIN roots r ON r.id=al.root_id WHERE al.work_id=? LIMIT 1000", work)
	if e != nil {
		return false
	}
	type item struct{ base, path, status, rootID string }
	items := []item{}
	for rows.Next() {
		var v item
		_ = rows.Scan(&v.base, &v.path, &v.status, &v.rootID)
		items = append(items, v)
	}
	rows.Close()
	if len(items) == 0 {
		return false
	}
	for _, v := range items {
		if v.status != "online" {
			return false
		}
		root, e := os.OpenRoot(v.base)
		if e != nil {
			return false
		}
		record, err := a.getRoot(ctx, v.rootID)
		if err != nil {
			root.Close()
			return false
		}
		if err = a.requireRootWitness(ctx, record, root); err != nil {
			root.Close()
			return false
		}
		_, e = root.Stat(v.path)
		root.Close()
		if !errors.Is(e, os.ErrNotExist) {
			return false
		}
	}
	return true
}
func (a *App) cleanupHTTP(w http.ResponseWriter, r *http.Request, u User) {
	var in struct {
		IDs     []string `json:"ids"`
		Preview bool     `json:"preview"`
		Confirm bool     `json:"confirm"`
	}
	if decodeJSON(w, r, &in) != nil || len(in.IDs) == 0 || len(in.IDs) > 500 {
		apiError(w, 400, "invalid_request", "Choose 1–500 work records")
		return
	}
	eligible := []string{}
	blocked := []string{}
	for _, wid := range in.IDs {
		if a.missingEligible(r.Context(), wid) {
			eligible = append(eligible, wid)
		} else {
			blocked = append(blocked, wid)
		}
	}
	if in.Preview {
		jsonResponse(w, 200, map[string]any{"count": len(eligible), "eligible": eligible, "blocked": blocked})
		return
	}
	if !in.Confirm {
		apiError(w, 400, "confirmation_required", "Preview and explicitly confirm index-only cleanup")
		return
	}
	removed := 0
	for _, wid := range eligible {
		if !a.missingEligible(r.Context(), wid) {
			blocked = append(blocked, wid)
			continue
		}
		if e := a.deleteWork(r.Context(), wid, u.ID); e == nil {
			removed++
		}
	}
	jsonResponse(w, 200, map[string]any{"count": removed, "blocked": blocked})
}
func (a *App) deleteWork(ctx context.Context, wid, actor string) error {
	var thumbnail string
	_ = a.DB.QueryRowContext(ctx, "SELECT path FROM thumbnails WHERE work_id=?", wid).Scan(&thumbnail)
	tx, e := a.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	_, e = tx.Exec("DELETE FROM works WHERE id=?", wid)
	if e == nil {
		for _, table := range []string{"metadata_words", "metadata_substrings"} {
			_, e = tx.Exec("DELETE FROM "+table+" WHERE work_id=?", wid)
			if e != nil {
				break
			}
		}
	}
	if e == nil {
		e = bump(tx)
	}
	if e == nil {
		e = audit(tx, actor, "work.index_remove", "", map[string]string{"work": wid})
	}
	if e == nil {
		e = a.commitAdmin(tx, actor, "work.index_remove", "", map[string]string{"work": wid})
	}
	if e == nil {
		a.cancelWorkPrefetch(wid)
		if thumbnail != "" && within(filepath.Join(a.Config.DataDir, "thumbnails"), thumbnail) {
			_ = os.Remove(thumbnail)
		}
	}
	return e
}
func (a *App) deleteWorkHTTP(w http.ResponseWriter, r *http.Request, u User) {
	var in struct {
		Confirm bool `json:"confirm"`
	}
	if decodeJSON(w, r, &in) != nil || !in.Confirm {
		apiError(w, 400, "confirmation_required", "Confirm removing the index record only; originals are never deleted")
		return
	}
	if e := a.deleteWork(r.Context(), r.PathValue("id"), u.ID); e != nil {
		apiError(w, 503, "database", "Cannot delete work record")
		return
	}
	jsonResponse(w, 200, map[string]bool{"ok": true})
}
func (a *App) restoreHTTP(w http.ResponseWriter, r *http.Request, u User) {
	var in struct {
		IDs []string `json:"ids"`
	}
	if decodeJSON(w, r, &in) != nil || len(in.IDs) > 500 {
		apiError(w, 400, "invalid_request", "Choose at most 500 work records")
		return
	}
	restored := 0
	for _, wid := range in.IDs {
		rows, e := a.DB.QueryContext(r.Context(), "SELECT al.root_id,al.path,w.kind FROM aliases al JOIN works w ON w.id=al.work_id WHERE w.id=? LIMIT 1000", wid)
		if e != nil {
			continue
		}
		type item struct{ root, path, kind string }
		items := []item{}
		for rows.Next() {
			var v item
			_ = rows.Scan(&v.root, &v.path, &v.kind)
			items = append(items, v)
		}
		rows.Close()
		success := false
		for _, v := range items {
			root, e := a.getRoot(r.Context(), v.root)
			if e != nil {
				continue
			}
			rr, e := os.OpenRoot(root.Path)
			if e != nil {
				continue
			}
			st, e := rr.Stat(v.path)
			if e != nil {
				rr.Close()
				continue
			}
			size, mt := st.Size(), st.ModTime().UnixNano()
			if v.kind == "directory" {
				size, mt, _, e = galleryRevision(rr, v.path)
			}
			rr.Close()
			if e != nil {
				continue
			}
			_, e = a.observe(r.Context(), root, v.path, v.kind, st, size, mt, Job{Type: "incremental", Origin: "manual_restore", Actor: u.ID, Created: now()}, false)
			if e == nil {
				_, _ = a.DB.ExecContext(r.Context(), "UPDATE roots SET status='online',checked_at=? WHERE id=?", now(), root.ID)
				success = true
			}
		}
		if success {
			restored++
		}
	}
	jsonResponse(w, 200, map[string]int{"count": restored})
}
func (a *App) auditHTTP(w http.ResponseWriter, r *http.Request, u User) {
	rows, e := a.DB.QueryContext(r.Context(), "SELECT id,actor,action,root_id,detail,created FROM audit ORDER BY id DESC LIMIT 200")
	if e != nil {
		apiError(w, 503, "database", "Cannot read audit records")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, created int64
		var actor, action, root, detail string
		_ = rows.Scan(&id, &actor, &action, &root, &detail, &created)
		out = append(out, map[string]any{"id": id, "actor": actor, "action": action, "root_id": root, "detail": detail, "created": created})
	}
	jsonResponse(w, 200, out)
}
