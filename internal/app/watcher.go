package app

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

type directoryWatcher interface {
	Add(string) error
	Remove(string) error
}

// registerWatches is called only by watchLoop, which owns watched.
func (a *App) registerWatches(watcher directoryWatcher, watched map[string]Root) {
	desired := map[string]Root{}
	after := ""
	for {
		rows, e := a.DB.QueryContext(a.ctx, "SELECT "+rootColumns+" FROM roots WHERE watch=1 AND id>? ORDER BY id LIMIT 32", after)
		if e != nil {
			return
		}
		roots := []Root{}
		for rows.Next() {
			r, e := scanRoot(rows)
			if e != nil {
				rows.Close()
				return
			}
			roots = append(roots, r)
		}
		err := rows.Err()
		rows.Close()
		if err != nil {
			return
		}
		if len(roots) == 0 {
			break
		}
		for _, root := range roots {
			after = root.ID
			desired[root.Path] = root
			if _, ok := watched[root.Path]; !ok {
				if e = watcher.Add(root.Path); e != nil {
					_, _ = a.DB.ExecContext(a.ctx, "UPDATE roots SET warning='Watcher unavailable; explicit reconciliation required' WHERE id=?", root.ID)
					continue
				}
			}
			watched[root.Path] = root
			cursor := ""
			for {
				rs, e := a.DB.QueryContext(a.ctx, "SELECT path FROM watch_dirs WHERE root_id=? AND path>? ORDER BY path LIMIT 128", root.ID, cursor)
				if e != nil {
					return
				}
				dirs := []string{}
				for rs.Next() {
					var p string
					if e := rs.Scan(&p); e != nil {
						rs.Close()
						return
					}
					dirs = append(dirs, p)
				}
				err := rs.Err()
				rs.Close()
				if err != nil {
					return
				}
				if len(dirs) == 0 {
					break
				}
				for _, rel := range dirs {
					cursor = rel
					select {
					case <-a.ctx.Done():
						return
					default:
					}
					p := filepath.Join(root.Path, filepath.FromSlash(rel))
					desired[p] = root
					if _, ok := watched[p]; ok {
						watched[p] = root
						continue
					}
					if e = watcher.Add(p); e == nil {
						watched[p] = root
					} else {
						_, _ = a.DB.ExecContext(a.ctx, "UPDATE roots SET warning='Some watches could not be installed; explicit reconciliation required' WHERE id=?", root.ID)
					}
				}
			}
		}
	}
	for p := range watched {
		if _, ok := desired[p]; ok {
			continue
		}
		if e := watcher.Remove(p); e == nil || errors.Is(e, fsnotify.ErrNonExistentWatch) {
			delete(watched, p)
		}
	}
	counts := map[string]int{}
	for _, root := range watched {
		counts[root.ID]++
	}
	for root, dirs := range counts {
		a.Log.Info("watching root", "root", root, "dirs", dirs)
	}
}

func (a *App) watchLoop() {
	watcher, e := fsnotify.NewWatcher()
	if e != nil {
		a.Log.Error("watchers unavailable", "error", logError(e))
		return
	}
	defer watcher.Close()
	watched := map[string]Root{}
	register := func() { a.registerWatches(watcher, watched) }
	register()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-a.watcherReload:
			register()
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			var root Root
			found := false
			best := 0
			for p, r := range watched {
				if event.Name == p || within(p, event.Name) {
					if len(p) > best {
						root = r
						found = true
						best = len(p)
					}
				}
			}
			if !found {
				continue
			}
			rel, e := filepath.Rel(root.Path, event.Name)
			if e != nil || !filepath.IsLocal(rel) {
				continue
			}
			rel = filepath.ToSlash(rel)
			a.Log.Debug("watch event", "root", root.ID, "path", rel, "op", event.Op.String())
			if rel == "." {
				if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
					_, _ = a.DB.ExecContext(a.ctx, "UPDATE roots SET status='offline',warning='Root watch lost; verify mount and reconcile explicitly' WHERE id=?", root.ID)
				} else {
					_, _ = a.DB.ExecContext(a.ctx, "UPDATE roots SET warning='Root metadata changed; explicit reconciliation may be needed' WHERE id=?", root.ID)
				}
				continue
			}
			kind := "change"
			if event.Op&fsnotify.Write != 0 {
				kind = "write"
			}
			if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
				kind = "remove"
			}
			_, e = a.DB.ExecContext(a.ctx, "INSERT INTO watch_events(root_id,path,kind,observed) VALUES(?,?,?,?) ON CONFLICT(root_id,path) DO UPDATE SET kind=excluded.kind,observed=excluded.observed", root.ID, rel, kind, now())
			if e == nil {
				a.signal()
			}
		case _, ok := <-watcher.Errors:
			if !ok {
				return
			}
			for _, root := range watched {
				_, _ = a.DB.ExecContext(a.ctx, "UPDATE roots SET warning='Watcher overflow/error; explicit reconciliation required' WHERE id=?", root.ID)
			}
		}
	}
}
func (a *App) processWatchEvents(ctx context.Context) error {
	rows, e := a.DB.QueryContext(ctx, "SELECT root_id,path,kind,observed FROM watch_events ORDER BY observed LIMIT 32")
	if e != nil {
		return e
	}
	type event struct {
		root, rel, kind string
		observed        int64
	}
	events := []event{}
	for rows.Next() {
		var v event
		_ = rows.Scan(&v.root, &v.rel, &v.kind, &v.observed)
		events = append(events, v)
	}
	rows.Close()
	for _, v := range events {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		root, e := a.getRoot(ctx, v.root)
		if e == sql.ErrNoRows {
			_, _ = a.DB.ExecContext(ctx, "DELETE FROM watch_events WHERE root_id=?", v.root)
			continue
		}
		if e != nil {
			return e
		}
		r, e := os.OpenRoot(root.Path)
		if e != nil {
			_, _ = a.DB.ExecContext(ctx, "UPDATE roots SET status='offline',warning='Root unavailable; reconcile explicitly after restoring mount' WHERE id=?", root.ID)
			continue
		}
		st, se := r.Stat(v.rel)
		if errors.Is(se, os.ErrNotExist) {
			_, _ = a.DB.ExecContext(ctx, "UPDATE aliases SET status='offline' WHERE root_id=? AND (path=? OR path LIKE ? ESCAPE '\\')", root.ID, v.rel, escapeLike(v.rel+"/")+"%")
			a.cancelPrefetch("")
		} else if se == nil && st.IsDir() {
			tx, e := a.DB.BeginTx(ctx, nil)
			if e != nil {
				r.Close()
				return e
			}
			_, e = enqueueTx(tx, "incremental_scan", root.ID, "", "", "watcher", root.ID+":"+v.rel+":"+time.UnixMilli(v.observed).String(), map[string]string{"path": v.rel})
			if e == nil {
				e = tx.Commit()
			} else {
				tx.Rollback()
			}
			if e != nil {
				r.Close()
				return e
			}
		} else if se == nil && archiveKind(v.rel) != "" {
			_, e = a.observe(ctx, root, v.rel, archiveKind(v.rel), st, st.Size(), st.ModTime().UnixNano(), Job{Type: "incremental", Origin: "watcher", Created: now()}, v.kind == "write")
		}
		if imageName(v.rel) {
			dir := path.Dir(v.rel)
			var wid string
			_ = a.DB.QueryRowContext(ctx, "SELECT work_id FROM aliases WHERE root_id=? AND path=?", root.ID, dir).Scan(&wid)
			ds, de := r.Stat(dir)
			if de == nil {
				size, mt, n, ge := galleryRevision(r, dir)
				if ge == nil && n > 0 {
					leaf := true
					f, fe := r.Open(dir)
					if fe == nil {
						for {
							es, re := f.ReadDir(128)
							for _, entry := range es {
								s, e := r.Stat(path.Join(dir, entry.Name()))
								if e == nil && s.IsDir() {
									leaf = false
									break
								}
							}
							if !leaf || re != nil {
								break
							}
						}
						f.Close()
					}
					if leaf {
						_, e = a.observe(ctx, root, dir, "directory", ds, size, mt, Job{Type: "incremental", Origin: "watcher", Created: now()}, wid != "" && v.kind == "write")
					}
				}
			}
		}
		r.Close()
		if e != nil {
			if errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded) {
				return e
			}
			a.Log.Warn("watch event observation failed", "root", v.root, "path", v.rel, "error", logError(e))
		}
		_, e = a.DB.ExecContext(ctx, "DELETE FROM watch_events WHERE root_id=? AND path=? AND observed=?", v.root, v.rel, v.observed)
		if e != nil {
			return e
		}
	}
	return nil
}
