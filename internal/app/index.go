package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"time"

	ar "archika/internal/archive"
)

var errStabilizing = errors.New("source is waiting for its quiet interval")
var errIndexFailed = errors.New("archive index failed; explicit retry required")

func (a *App) ensureIndex(ctx context.Context, al Alias) error {
	key := fmt.Sprintf("%s:%d", al.WorkID, al.Version)
	a.indexMu.Lock()
	if a.ctx.Err() != nil {
		a.indexMu.Unlock()
		return context.Canceled
	}
	f := a.indexFlights[key]
	if f == nil {
		shared, cancel := context.WithCancel(a.ctx)
		f = &indexFlight{done: make(chan struct{}), cancel: cancel}
		f.priority.parent = signalFor(ctx)
		shared = context.WithValue(shared, priorityKey{}, &f.priority)
		shared = context.WithValue(shared, backgroundKey{}, background(ctx))
		a.indexFlights[key] = f
		a.wg.Add(1)
		go func(f *indexFlight) {
			defer a.wg.Done()
			err := a.buildIndex(shared, al)
			a.indexMu.Lock()
			f.err = err
			delete(a.indexFlights, key)
			close(f.done)
			a.indexMu.Unlock()
			cancel()
		}(f)
	}
	if !background(ctx) || promoted(ctx) {
		f.priority.foreground.Store(true)
	}
	f.waiters++
	a.indexMu.Unlock()
	select {
	case <-ctx.Done():
		a.indexMu.Lock()
		f.waiters--
		if f.waiters == 0 {
			f.cancel()
		}
		a.indexMu.Unlock()
		return ctx.Err()
	case <-f.done:
		return f.err
	}
}
func (a *App) buildIndex(ctx context.Context, al Alias) error {
	var status string
	var version int64
	e := a.DB.QueryRowContext(ctx, "SELECT index_status,version FROM works WHERE id=?", al.WorkID).Scan(&status, &version)
	if e != nil {
		return e
	}
	if version != al.Version {
		return errors.New("source_changed")
	}
	if status == "ready" {
		return nil
	}
	if status == "error" {
		return errIndexFailed
	}
	if status == "pending_stability" {
		j, e := scanJob(a.DB.QueryRowContext(ctx, "SELECT "+jobColumns+" FROM jobs WHERE work_id=? AND type IN ('stability','stability_bulk') AND not_before<=? ORDER BY created DESC LIMIT 1", al.WorkID, now()))
		if e != nil {
			return errStabilizing
		}
		if e = a.stabilizeJob(ctx, j); e != nil {
			return e
		}
		var fresh int64
		_ = a.DB.QueryRowContext(ctx, "SELECT version,index_status FROM works WHERE id=?", al.WorkID).Scan(&fresh, &status)
		if fresh != al.Version || status == "pending_stability" {
			return errStabilizing
		}
	}
	root, e := a.checkSource(ctx, al)
	if e != nil {
		return e
	}
	defer root.Close()
	if al.Kind == "directory" {
		size, mtime, _, e := galleryRevision(root, al.Path)
		if e != nil {
			return e
		}
		if size != al.Size || mtime != al.Mtime {
			st, e := root.Stat(al.Path)
			if e != nil {
				return e
			}
			r, e := a.getRoot(ctx, al.RootID)
			if e != nil {
				return e
			}
			_, e = a.observe(ctx, r, al.Path, al.Kind, st, size, mtime, Job{Type: "incremental", Origin: "read", Created: now()}, false)
			if e != nil {
				return e
			}
			return errStabilizing
		}
	}
	if e = a.acquire(ctx, background(ctx)); e != nil {
		return e
	}
	defer a.release()
	_, e = a.DB.ExecContext(ctx, "UPDATE works SET index_status='indexing' WHERE id=? AND version=?", al.WorkID, al.Version)
	if e != nil {
		return e
	}
	var entries []ar.Entry
	var capabilities ar.Capabilities
	e = a.sessions.With(ctx, al, func(engine ar.Adapter) error {
		var err error
		entries, capabilities, err = engine.List(ctx)
		return err
	})
	if ctx.Err() != nil {
		_, _ = a.DB.ExecContext(a.ctx, "UPDATE works SET index_status='pending' WHERE id=? AND version=? AND index_status='indexing'", al.WorkID, al.Version)
		return ctx.Err()
	}
	if e != nil {
		_, _ = a.DB.ExecContext(ctx, "UPDATE works SET index_status='error',error_code=?,error_message=?,error_at=? WHERE id=? AND version=?", decodeErrorCode(e), safeError(e), now(), al.WorkID, al.Version)
		return e
	}
	if len(entries) == 0 {
		_, _ = a.DB.ExecContext(ctx, "UPDATE works SET index_status='error',error_code='unsupported_format',error_message='No supported image pages',error_at=? WHERE id=? AND version=?", now(), al.WorkID, al.Version)
		return errors.New("no supported image pages")
	}
	// All enumeration results remain private until both revision checks succeed.
	verified, verifyErr := a.checkSource(ctx, al)
	if verifyErr != nil {
		return verifyErr
	}
	verified.Close()
	st, e := root.Stat(al.Path)
	if e != nil {
		return e
	}
	size, mtime := st.Size(), st.ModTime().UnixNano()
	if al.Kind == "directory" {
		size, mtime, _, e = galleryRevision(root, al.Path)
		if e != nil {
			return e
		}
	}
	if size != al.Size || mtime != al.Mtime {
		r, e := a.getRoot(ctx, al.RootID)
		if e != nil {
			return e
		}
		_, e = a.observe(ctx, r, al.Path, al.Kind, st, size, mtime, Job{Type: "incremental", Origin: "read", Created: now()}, false)
		if e != nil {
			return e
		}
		return errStabilizing
	}
	type observation struct {
		id          string
		size, mtime int64
	}
	observed := []observation{}
	if al.Kind == "directory" {
		for _, entry := range entries {
			st, e := root.Stat(path.Join(al.Path, entry.Path))
			if e != nil {
				return e
			}
			observed = append(observed, observation{entry.ID, st.Size(), st.ModTime().UnixNano()})
		}
	}
	tx, e := a.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var actual int64
	if e = tx.QueryRow("SELECT version FROM works WHERE id=?", al.WorkID).Scan(&actual); e != nil {
		return e
	}
	if actual != al.Version {
		return errors.New("source_changed")
	}
	if _, e = tx.Exec("DELETE FROM pages WHERE work_id=?", al.WorkID); e != nil {
		return e
	}
	if _, e = tx.Exec("DELETE FROM page_observations WHERE work_id=?", al.WorkID); e != nil {
		return e
	}
	stmt, e := tx.Prepare("INSERT INTO pages(work_id,version,ordinal,entry,entry_id,size,mime,locator,width,height) VALUES(?,?,?,?,?,?,?,?,?,?)")
	if e != nil {
		return e
	}
	defer stmt.Close()
	for _, entry := range entries {
		_, e = stmt.Exec(al.WorkID, al.Version, entry.Ordinal, entry.Path, entry.ID, entry.Size, entry.MIME, entry.Locator, entry.Width, entry.Height)
		if e != nil {
			return e
		}
	}
	for _, o := range observed {
		_, e = tx.Exec("INSERT INTO page_observations(work_id,version,entry_id,size,mtime) VALUES(?,?,?,?,?)", al.WorkID, al.Version, o.id, o.size, o.mtime)
		if e != nil {
			return e
		}
	}
	c, _ := json.Marshal(capabilities)
	_, e = tx.Exec("UPDATE works SET index_status='ready',error_code='',error_message='',capabilities=?,cover_unresolved=CASE WHEN cover!='' AND NOT EXISTS(SELECT 1 FROM pages WHERE work_id=works.id AND version=works.version AND entry_id=works.cover) THEN 1 ELSE 0 END WHERE id=? AND version=?", string(c), al.WorkID, al.Version)
	if e == nil {
		_, e = tx.Exec("UPDATE jobs SET state='queued',error='',not_before=0 WHERE work_id=? AND type='auto_crawl' AND state='paused_dependency'", al.WorkID)
	}
	if e == nil {
		e = tx.Commit()
	}
	return e
}
func (a *App) pageList(ctx context.Context, al Alias) ([]ar.Entry, error) {
	rows, e := a.DB.QueryContext(ctx, "SELECT ordinal,entry,entry_id,size,mime,locator,width,height FROM pages WHERE work_id=? AND version=? ORDER BY ordinal", al.WorkID, al.Version)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []ar.Entry{}
	for rows.Next() {
		var p ar.Entry
		if e = rows.Scan(&p.Ordinal, &p.Path, &p.ID, &p.Size, &p.MIME, &p.Locator, &p.Width, &p.Height); e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (a *App) page(ctx context.Context, al Alias, ordinal int) (ar.Entry, error) {
	var p ar.Entry
	e := a.DB.QueryRowContext(ctx, "SELECT ordinal,entry,entry_id,size,mime,locator,width,height FROM pages WHERE work_id=? AND version=? AND ordinal=?", al.WorkID, al.Version, ordinal).Scan(&p.Ordinal, &p.Path, &p.ID, &p.Size, &p.MIME, &p.Locator, &p.Width, &p.Height)
	return p, e
}
func (a *App) acquire(ctx context.Context, lowPriority bool) error {
	registered := false
	defer func() {
		if registered {
			a.interactive.Add(-1)
		}
	}()
	for {
		foreground := !lowPriority || promoted(ctx)
		if foreground && !registered {
			a.interactive.Add(1)
			registered = true
		}
		if !foreground && a.interactive.Load() > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(10 * time.Millisecond):
			}
			continue
		}
		select {
		case a.foreground <- struct{}{}:
			if lowPriority && !promoted(ctx) && a.interactive.Load() > 0 {
				<-a.foreground
				continue
			}
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
func (a *App) release() { <-a.foreground }
func (a *App) verifyDirectoryPage(ctx context.Context, al Alias, p ar.Entry) error {
	if al.Kind != "directory" {
		return nil
	}
	root, e := os.OpenRoot(al.RootPath)
	if e != nil {
		return e
	}
	defer root.Close()
	file, e := root.Open(path.Join(al.Path, p.Path))
	var st os.FileInfo
	if e == nil {
		st, e = file.Stat()
		_ = file.Close()
	}
	if e != nil {
		return e
	}
	var size, mtime int64
	e = a.DB.QueryRowContext(ctx, "SELECT size,mtime FROM page_observations WHERE work_id=? AND version=? AND entry_id=?", al.WorkID, al.Version, p.ID).Scan(&size, &mtime)
	if e != nil {
		return e
	}
	if st.Size() != size || st.ModTime().UnixNano() != mtime {
		if dir, err := root.Stat(al.Path); err == nil {
			_ = a.changedSource(ctx, al, dir, true)
		}
		return errors.New("source_changed")
	}
	return nil
}
