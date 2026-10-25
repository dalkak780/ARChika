package app

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path"
	"strings"
)

// A reachable empty mountpoint is not evidence that the mounted library was
// intentionally deleted. Probe at most sixteen known sources, never walk roots.
func (a *App) rootWitness(ctx context.Context, root Root, r *os.Root) (bool, error) {
	if strings.HasPrefix(root.Warning, "Force approved") {
		return true, nil
	}
	rows, e := a.DB.QueryContext(ctx, "SELECT al.path,w.kind,COALESCE(w.physical_key,''),(SELECT entry FROM pages p WHERE p.work_id=w.id AND p.version=w.version ORDER BY ordinal LIMIT 1) FROM aliases al JOIN works w ON w.id=al.work_id WHERE al.root_id=? AND al.status='online' ORDER BY al.path LIMIT 16", root.ID)
	if e != nil {
		return false, e
	}
	type sample struct {
		path, kind, key string
		page            sql.NullString
	}
	samples := []sample{}
	for rows.Next() {
		var s sample
		if e = rows.Scan(&s.path, &s.kind, &s.key, &s.page); e != nil {
			rows.Close()
			return false, e
		}
		samples = append(samples, s)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return false, e
	}
	if len(samples) == 0 {
		return true, nil
	}
	for _, s := range samples {
		if e = ctx.Err(); e != nil {
			return false, e
		}
		st, e := r.Stat(s.path)
		if e != nil {
			continue
		}
		if s.kind != "directory" && st.Mode().IsRegular() {
			return true, nil
		}
		if s.kind == "directory" && st.IsDir() {
			if sameObjectKey(s.key, physicalKey(st, root.Path, s.path)) {
				return true, nil
			}
			if !s.page.Valid {
				return true, nil
			}
			if st, e = r.Stat(path.Join(s.path, s.page.String)); e == nil && st.Mode().IsRegular() {
				return true, nil
			}
		}
	}
	return false, nil
}
func (a *App) markRootUnavailable(ctx context.Context, root Root) error {
	tx, e := a.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	_, e = tx.Exec("UPDATE roots SET status='offline',pending_path=?,warning='No known sources reachable; verify mount before reconcile or force approval',checked_at=? WHERE id=?", root.Path, now(), root.ID)
	if e == nil {
		e = bump(tx)
	}
	if e == nil {
		e = tx.Commit()
	}
	a.cancelPrefetch("")
	return e
}
func (a *App) requireRootWitness(ctx context.Context, root Root, r *os.Root) error {
	ok, e := a.rootWitness(ctx, root, r)
	if e != nil {
		return e
	}
	if ok {
		return nil
	}
	if e = a.markRootUnavailable(ctx, root); e != nil {
		return e
	}
	return errors.New("root unavailable; mount verification required")
}
