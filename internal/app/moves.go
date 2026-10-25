package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
)

func recordMoveCandidates(tx *sql.Tx, root, work, kind string, size, mtime int64) error {
	rows, e := tx.Query("SELECT DISTINCT w.id,al.root_id FROM works w JOIN aliases al ON al.work_id=w.id JOIN roots r ON r.id=al.root_id WHERE w.id!=? AND w.kind=? AND w.size=? AND w.mtime=? AND al.root_id!=? AND (al.status!='online' OR r.status!='online') LIMIT 10", work, kind, size, mtime, root)
	if e != nil {
		return e
	}
	type candidate struct{ work, root string }
	matches := []candidate{}
	for rows.Next() {
		var v candidate
		if e = rows.Scan(&v.work, &v.root); e != nil {
			rows.Close()
			return e
		}
		matches = append(matches, v)
	}
	rows.Close()
	for _, v := range matches {
		_, e = tx.Exec("INSERT INTO move_candidates(id,from_work,to_work,from_root,to_root,created) SELECT ?,?,?,?,?,? WHERE NOT EXISTS(SELECT 1 FROM move_candidates WHERE from_work=? AND to_work=?)", id(), v.work, work, v.root, root, now(), v.work, work)
		if e != nil {
			return e
		}
	}
	return nil
}
func (a *App) movesHTTP(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method == "GET" {
		rows, e := a.DB.QueryContext(r.Context(), "SELECT m.id,m.from_work,m.to_work,m.from_root,m.to_root,(SELECT path FROM aliases WHERE work_id=m.from_work AND root_id=m.from_root ORDER BY path LIMIT 1),(SELECT path FROM aliases WHERE work_id=m.to_work AND root_id=m.to_root ORDER BY path LIMIT 1) FROM move_candidates m WHERE m.state='pending' ORDER BY m.created DESC LIMIT 100")
		if e != nil {
			apiError(w, 503, "database", "Cannot load move candidates")
			return
		}
		defer rows.Close()
		out := []map[string]string{}
		for rows.Next() {
			var id, from, to, fr, tr string
			var fp, tp sql.NullString
			_ = rows.Scan(&id, &from, &to, &fr, &tr, &fp, &tp)
			out = append(out, map[string]string{"id": id, "from_work": from, "to_work": to, "from_root": fr, "to_root": tr, "from_path": fp.String, "to_path": tp.String, "evidence": "equal lightweight size/mtime; not proof of identical content"})
		}
		jsonResponse(w, 200, out)
		return
	}
	var in struct {
		Action  string `json:"action"`
		Confirm bool   `json:"confirm"`
	}
	if decodeJSON(w, r, &in) != nil || (in.Action != "approve" && in.Action != "reject") {
		apiError(w, 400, "invalid_action", "Choose approve or reject")
		return
	}
	cid := r.PathValue("id")
	if in.Action == "reject" {
		_, e := a.DB.ExecContext(r.Context(), "UPDATE move_candidates SET state='rejected' WHERE id=? AND state='pending'", cid)
		if e != nil {
			apiError(w, 503, "database", "Cannot update candidate")
			return
		}
		jsonResponse(w, 200, map[string]bool{"ok": true})
		return
	}
	if !in.Confirm {
		apiError(w, 400, "confirmation_required", "Confirm logical identity; metadata matches are not proof. Only this work will be reindexed.")
		return
	}
	var old, new, root string
	e := a.DB.QueryRowContext(r.Context(), "SELECT from_work,to_work,to_root FROM move_candidates WHERE id=? AND state='pending'", cid).Scan(&old, &new, &root)
	if e != nil {
		apiError(w, 404, "not_found", "Move candidate unavailable")
		return
	}
	al, e := a.AuthorizedAlias(r.Context(), u, new)
	if e != nil {
		apiError(w, 409, "unavailable", "Destination is not available")
		return
	}
	rr, e := a.checkSource(r.Context(), al)
	if e != nil {
		apiError(w, 409, "source_changed", "Destination revision changed")
		return
	}
	rr.Close()
	var thumb string
	_ = a.DB.QueryRowContext(r.Context(), "SELECT path FROM thumbnails WHERE work_id=?", new).Scan(&thumb)
	tx, e := a.DB.BeginTx(r.Context(), nil)
	if e != nil {
		apiError(w, 503, "database", "Cannot confirm move")
		return
	}
	defer tx.Rollback()
	var edits int
	_ = tx.QueryRow("SELECT (SELECT count(*) FROM manual_fields WHERE work_id=?)+(SELECT count(*) FROM provider_snapshots WHERE work_id=?)+(SELECT count(*) FROM tag_decisions WHERE work_id=?)+(SELECT count(*) FROM works WHERE id=? AND cover!='')", new, new, new, new).Scan(&edits)
	if edits != 0 {
		apiError(w, 409, "destination_has_metadata", "Destination already has user/provider metadata; refusing destructive merge")
		return
	}
	var version int64
	e = tx.QueryRow("SELECT version+1 FROM works WHERE id=?", old).Scan(&version)
	if e == nil {
		_, e = tx.Exec("UPDATE aliases SET work_id=? WHERE work_id=?", old, new)
	}
	if e == nil {
		_, e = tx.Exec("UPDATE works SET physical_key=?,size=?,mtime=?,kind=?,version=?,index_status='pending_stability',error_code='',error_message='' WHERE id=?", nullString(al.PhysicalKey), al.Size, al.Mtime, al.Kind, version, old)
	}
	if e == nil {
		_, e = tx.Exec("DELETE FROM works WHERE id=?", new)
	}
	if e == nil {
		_, e = tx.Exec("UPDATE move_candidates SET state='approved' WHERE id=?", cid)
	}
	if e == nil {
		_, e = enqueueTx(tx, "stability", root, old, u.ID, "confirmed_move", fmt.Sprintf("stability:%s:%d", old, version), map[string]any{"version": version, "size": al.Size, "mtime": al.Mtime})
	}
	if e == nil {
		e = audit(tx, u.ID, "move.confirm", root, map[string]string{"retained_work": old, "replaced_work": new, "evidence": "administrator-confirmed logical identity; target reindex required"})
	}
	if e == nil {
		e = bump(tx)
	}
	if e == nil {
		e = tx.Commit()
	}
	if e != nil {
		apiError(w, 409, "move_conflict", "Move confirmation failed")
		return
	}
	if thumb != "" && within(a.Config.DataDir, thumb) {
		_ = os.Remove(thumb)
	}
	a.cancelWorkPrefetch(old)
	a.cancelWorkPrefetch(new)
	a.signal()
	jsonResponse(w, 200, map[string]any{"ok": true, "id": old})
}
func (a *App) readableAlias(ctx context.Context, u User, work string, version int64) (Alias, error) {
	for range 16 {
		al, e := a.AuthorizedAlias(ctx, u, work)
		if e != nil {
			return Alias{}, e
		}
		if al.Version != version {
			return Alias{}, errors.New("source_changed")
		}
		rr, e := a.checkSource(ctx, al)
		if e == nil {
			rr.Close()
			return al, nil
		}
		if e.Error() == "source_changed" {
			return Alias{}, e
		}
		if ctx.Err() != nil {
			return Alias{}, ctx.Err()
		}
	}
	return Alias{}, errors.New("no reachable authorized alias")
}
