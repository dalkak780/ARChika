package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

func archiveKind(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".zip", ".cbz":
		return "zip"
	case ".rar", ".cbr":
		return "rar"
	case ".7z", ".cb7":
		return "7z"
	}
	return ""
}
func imageName(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg", ".png", ".apng", ".gif", ".webp", ".avif", ".jxl":
		return true
	}
	return false
}

type scanCounts struct{ new, changed, aliases, missing, dirs int64 }
type scanCountsKey struct{}

func (a *App) scanJob(ctx context.Context, j Job) error {
	started := time.Now()
	counts := &scanCounts{}
	ctx = context.WithValue(ctx, scanCountsKey{}, counts)
	root, e := a.getRoot(ctx, j.RootID)
	if e != nil {
		return e
	}
	defer func() {
		a.Log.Info("scan finished", "job", j.ID, "root", root.ID, "name", root.Name, "new", counts.new, "changed", counts.changed, "missing", counts.missing, "aliases", counts.aliases, "dirs", counts.dirs, "duration_ms", time.Since(started).Milliseconds())
	}()
	var payload struct {
		Target string `json:"target_path"`
		Path   string `json:"path"`
	}
	_ = json.Unmarshal([]byte(j.Payload), &payload)
	base := root.Path
	if payload.Target != "" {
		base = payload.Target
	}
	r, e := os.OpenRoot(base)
	if e != nil {
		_, _ = a.DB.ExecContext(ctx, "UPDATE roots SET status='offline',checked_at=? WHERE id=?", now(), root.ID)
		return e
	}
	defer r.Close()
	root.Path = base
	start := "."
	if payload.Path != "" {
		start = payload.Path
	}
	if !filepath.IsLocal(start) {
		return errors.New("invalid relative scan path")
	}
	if start == "." {
		if e = a.requireRootWitness(ctx, root, r); e != nil {
			return e
		}
	}
	tx, e := a.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	res, e := tx.Exec("INSERT INTO scan_dirs(job_id,path) VALUES(?,?) ON CONFLICT DO NOTHING", j.ID, start)
	if e == nil && start == "." {
		if n, _ := res.RowsAffected(); n > 0 {
			// Mark sweep candidates only at the first checkpoint; observations replace this marker.
			_, e = tx.Exec("UPDATE aliases SET seen=? WHERE root_id=?", "unseen:"+j.ID, root.ID)
		}
	}
	if e == nil {
		e = tx.Commit()
	}
	if e != nil {
		return e
	}
	tick := time.NewTicker(time.Second / time.Duration(a.Config.ScanPerSecond))
	defer tick.Stop()
	for {
		if e = a.jobAlive(ctx, j.ID); e != nil {
			return e
		}
		var dir, listed string
		e = a.DB.QueryRowContext(ctx, "SELECT path,after_name FROM scan_dirs WHERE job_id=? AND done=0 ORDER BY path LIMIT 1", j.ID).Scan(&dir, &listed)
		if e == sql.ErrNoRows {
			break
		}
		if e != nil {
			return e
		}
		counts.dirs++
		if listed != "listed" {
			skipped, err := a.listScanDirectory(ctx, j, root, r, dir)
			if e = err; e != nil {
				if errors.Is(e, context.Canceled) || errors.Is(e, errJobStopped) {
					return e
				}
				_, _ = a.DB.ExecContext(ctx, "UPDATE jobs SET failed=failed+1,error='Some directories could not be read',updated=? WHERE id=?", now(), j.ID)
				_, _ = a.DB.ExecContext(ctx, "UPDATE scan_dirs SET done=2 WHERE job_id=? AND path=?", j.ID, dir)
				continue
			}
			if skipped {
				continue
			}
		}
		// Direct child detection uses secure stat; subfolders organize works, not galleries.
		hasDir := false
		f, err := r.Open(dir)
		if err == nil {
			for {
				entries, err := f.ReadDir(128)
				for _, entry := range entries {
					st, se := r.Stat(path.Join(dir, entry.Name()))
					if se == nil && st.IsDir() {
						hasDir = true
						break
					}
				}
				if hasDir || err != nil {
					break
				}
			}
			f.Close()
		}
		if !hasDir {
			size, revision, images, err := galleryRevision(r, dir)
			if err == nil && images > 0 {
				st, se := r.Stat(dir)
				if se == nil {
					_, err = a.observe(ctx, root, dir, "directory", st, size, revision, j, false)
					if err != nil {
						return err
					}
				}
			}
		}
		for {
			if e = a.jobAlive(ctx, j.ID); e != nil {
				return e
			}
			rows, e := a.DB.QueryContext(ctx, "SELECT name FROM scan_entries WHERE job_id=? AND parent=? AND done=0 ORDER BY name LIMIT 128", j.ID, dir)
			if e != nil {
				return e
			}
			names := []string{}
			for rows.Next() {
				var name string
				_ = rows.Scan(&name)
				names = append(names, name)
			}
			rows.Close()
			if len(names) == 0 {
				break
			}
			for _, name := range names {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-tick.C:
				}
				if e = a.jobAlive(ctx, j.ID); e != nil {
					return e
				}
				rel := path.Join(dir, name)
				kind := archiveKind(name)
				if kind != "" {
					st, se := r.Stat(rel)
					if se == nil && st.Mode().IsRegular() {
						_, e = a.observe(ctx, root, rel, kind, st, st.Size(), st.ModTime().UnixNano(), j, false)
						if e != nil {
							return e
						}
					} else {
						_, _ = a.DB.ExecContext(ctx, "UPDATE jobs SET failed=failed+1 WHERE id=?", j.ID)
					}
				} else if hasDir && imageName(name) {
					_, _ = a.DB.ExecContext(ctx, "UPDATE jobs SET failed=failed+1,error='Direct images in non-leaf folders were not indexed as a gallery' WHERE id=?", j.ID)
				}
				_, e = a.DB.ExecContext(ctx, "UPDATE scan_entries SET done=1 WHERE job_id=? AND parent=? AND name=?", j.ID, dir, name)
				if e != nil {
					return e
				}
				_, _ = a.DB.ExecContext(ctx, "UPDATE jobs SET done=done+1,updated=? WHERE id=?", now(), j.ID)
			}
		}
		_, e = a.DB.ExecContext(ctx, "UPDATE scan_dirs SET done=1 WHERE job_id=? AND path=?", j.ID, dir)
		if e != nil {
			return e
		}
	}
	var failedDirectories int
	if e = a.DB.QueryRowContext(ctx, "SELECT count(*) FROM scan_dirs WHERE job_id=? AND done=2", j.ID).Scan(&failedDirectories); e != nil {
		return e
	}
	if failedDirectories > 0 {
		_, _ = a.DB.ExecContext(ctx, "UPDATE roots SET warning='Reconciliation incomplete; unseen records retained' WHERE id=?", root.ID)
		return errors.New("scan incomplete; unreadable directories require explicit retry")
	}
	tx, e = a.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var missing int64
	if start == "." {
		res, err := tx.Exec("UPDATE aliases SET status='offline' WHERE root_id=? AND seen=? AND status!='offline'", root.ID, "unseen:"+j.ID)
		e = err
		if e == nil {
			missing, e = res.RowsAffected()
		}
	}
	if e == nil && payload.Target != "" {
		_, e = tx.Exec("UPDATE roots SET path=?,pending_path='',warning='',status='online',checked_at=? WHERE id=?", payload.Target, now(), root.ID)
	} else if e == nil {
		_, e = tx.Exec("UPDATE roots SET status='online',warning='',checked_at=? WHERE id=?", now(), root.ID)
	}
	if e == nil {
		e = bump(tx)
	}
	if e == nil {
		e = tx.Commit()
	}
	if e == nil {
		counts.missing = missing
	}
	a.notifyWatch()
	return e
}
func (a *App) listScanDirectory(ctx context.Context, j Job, root Root, r *os.Root, dir string) (bool, error) {
	st, e := r.Stat(dir)
	if e != nil {
		return false, e
	}
	key := physicalKey(st, root.Path, dir)
	if key == "" {
		canonical, err := filepath.EvalSymlinks(filepath.Join(root.Path, filepath.FromSlash(dir)))
		if err != nil {
			return false, err
		}
		if canonical != root.Path && !within(root.Path, canonical) {
			return false, os.ErrPermission
		}
		key = "canonical:" + canonical
	}
	var previous string
	err := a.DB.QueryRowContext(ctx, "SELECT path FROM scan_seen WHERE job_id=? AND physical_key=?", j.ID, key).Scan(&previous)
	if err == nil && previous != dir {
		_, e = a.DB.ExecContext(ctx, "UPDATE scan_dirs SET done=1,after_name='listed' WHERE job_id=? AND path=?", j.ID, dir)
		return true, e
	}
	_, e = a.DB.ExecContext(ctx, "INSERT INTO scan_seen(job_id,physical_key,path) VALUES(?,?,?) ON CONFLICT DO NOTHING", j.ID, key, dir)
	if e != nil {
		return false, e
	}
	// os.Root rejects escapes atomically, including symlink swaps, on supported Go platforms.
	f, e := r.Open(dir)
	if e != nil {
		return false, e
	}
	defer f.Close()
	_, _ = a.DB.ExecContext(ctx, "INSERT INTO watch_dirs(root_id,path) VALUES(?,?) ON CONFLICT DO NOTHING", root.ID, dir)
	for {
		if e = a.jobAlive(ctx, j.ID); e != nil {
			return false, e
		}
		entries, re := f.ReadDir(128)
		if len(entries) > 0 {
			tx, e := a.DB.BeginTx(ctx, nil)
			if e != nil {
				return false, e
			}
			for _, entry := range entries {
				rel := path.Join(dir, entry.Name())
				s, se := r.Stat(rel)
				if se != nil {
					_, _ = tx.Exec("UPDATE jobs SET failed=failed+1 WHERE id=?", j.ID)
					continue
				}
				if s.IsDir() {
					_, e = tx.Exec("INSERT INTO scan_dirs(job_id,path) VALUES(?,?) ON CONFLICT DO NOTHING", j.ID, rel)
				} else if s.Mode().IsRegular() && (archiveKind(entry.Name()) != "" || imageName(entry.Name())) {
					_, e = tx.Exec("INSERT INTO scan_entries(job_id,parent,name) VALUES(?,?,?) ON CONFLICT DO NOTHING", j.ID, dir, entry.Name())
				}
				if e != nil {
					break
				}
			}
			if e == nil {
				e = tx.Commit()
			} else {
				tx.Rollback()
			}
			if e != nil {
				return false, e
			}
		}
		if re == io.EOF {
			break
		}
		if re != nil {
			return false, re
		}
	}
	_, e = a.DB.ExecContext(ctx, "UPDATE scan_dirs SET after_name='listed' WHERE job_id=? AND path=?", j.ID, dir)
	return false, e
}
func escapeLike(s string) string {
	return strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(s)
}
func galleryRevision(root *os.Root, dir string) (int64, int64, int, error) {
	f, e := root.Open(dir)
	if e != nil {
		return 0, 0, 0, e
	}
	defer f.Close()
	var total int64
	var count int
	var hash [32]byte
	for {
		entries, e := f.ReadDir(128)
		for _, entry := range entries {
			if !imageName(entry.Name()) {
				continue
			}
			st, se := root.Stat(path.Join(dir, entry.Name()))
			if se != nil {
				if entry.Type()&os.ModeSymlink != 0 {
					continue
				}
				return 0, 0, 0, se
			}
			if !st.Mode().IsRegular() {
				continue
			}
			count++
			total += st.Size()
			sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%d", entry.Name(), st.Size(), st.ModTime().UnixNano())))
			for i, b := range sum {
				hash[i] ^= b
			}
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return 0, 0, 0, e
		}
	}
	return total, int64(binary.BigEndian.Uint64(hash[:8]) & 0x7fffffffffffffff), count, nil
}
func (a *App) observe(ctx context.Context, root Root, rel, kind string, st os.FileInfo, size, mtime int64, j Job, force bool) (string, error) {
	original, title := basenameTitle(rel, kind)
	if rel == "." {
		original = filepath.Base(root.Path)
		title = original
	}
	parser := filenameParserVersion + ":" + root.ParserRegex + fmt.Sprint(root.ParserEnabled)
	physical := ""
	if root.TrustIdentity {
		physical = physicalKey(st, root.Path, rel)
	}
	var existingID string
	var existingSize, existingMtime, existingModified int64
	var previousName, previousParser, previousTags, previousStatus, previousSort, previousPhysical string
	var version int64
	e := a.DB.QueryRowContext(ctx, "SELECT w.id,w.size,w.mtime,w.version,w.modified_at,al.original_name,al.parser_version,al.filename_tags,al.status,al.title_sort,COALESCE(w.physical_key,'') FROM aliases al JOIN works w ON w.id=al.work_id WHERE al.root_id=? AND al.path=?", root.ID, rel).Scan(&existingID, &existingSize, &existingMtime, &version, &existingModified, &previousName, &previousParser, &previousTags, &previousStatus, &previousSort, &previousPhysical)
	if e != nil && e != sql.ErrNoRows {
		return "", e
	}
	newAlias := existingID == ""
	previousWork := existingID
	scanMarker := ""
	if j.ID != "" && (j.Type == "scan" || j.Type == "reconcile" || j.Type == "incremental_scan") {
		scanMarker = "scan:" + j.ID
	}
	nameChanged := newAlias || previousName != original || previousParser != parser
	tagJSON := previousTags
	if nameChanged {
		tags := filenameTags(title, root.ParserRegex, root.ParserEnabled)
		encoded, err := json.Marshal(tags)
		if err != nil {
			return "", err
		}
		tagJSON = string(encoded)
	}
	titleSort := previousSort
	if newAlias || previousName != original {
		titleSort = naturalKey(title)
	}
	if existingID != "" && physical != "" && previousPhysical != "" && !sameObjectKey(physical, previousPhysical) {
		other, err := a.hasOtherPhysicalAlias(ctx, existingID, root.ID, rel, previousPhysical)
		if err != nil {
			return "", err
		}
		if other {
			existingID = ""
			version = 0
		}
	}
	isNew := false
	changed := false
	if existingID == "" && physical != "" {
		rows, e := a.DB.QueryContext(ctx, "SELECT w.id,w.size,w.mtime,w.version,al.root_id,al.path,r.path FROM works w JOIN aliases al ON al.work_id=w.id JOIN roots r ON r.id=al.root_id WHERE w.physical_key=? AND w.kind=? AND w.size=? AND w.mtime=? LIMIT 32", physical, kind, size, mtime)
		if e != nil {
			return "", e
		}
		type candidate struct {
			id                   string
			size, mtime, version int64
			root, rel, base      string
		}
		candidates := []candidate{}
		for rows.Next() {
			var c candidate
			_ = rows.Scan(&c.id, &c.size, &c.mtime, &c.version, &c.root, &c.rel, &c.base)
			candidates = append(candidates, c)
		}
		rows.Close()
		for _, c := range candidates {
			verified := false
			old, e := os.OpenRoot(c.base)
			if e == nil {
				s, se := old.Stat(c.rel)
				verified = se == nil && physicalKey(s, c.base, c.rel) == physical
				old.Close()
			}
			if verified || c.root == root.ID && strings.Contains(physical, ":born=") {
				existingID = c.id
				existingSize = c.size
				existingMtime = c.mtime
				version = c.version
				break
			}
		}
	}
	tx, e := a.DB.BeginTx(ctx, nil)
	if e != nil {
		return "", e
	}
	defer tx.Rollback()
	if existingID == "" {
		existingID = id()
		version = 1
		isNew = true
		_, e = tx.Exec("INSERT INTO works(id,physical_key,kind,size,mtime,modified_at,registered_at) VALUES(?,?,?,?,?,?,?)", existingID, nullString(physical), kind, size, mtime, st.ModTime().UnixNano(), now())
	} else if force && previousWork == existingID || existingSize != size || existingMtime != mtime {
		changed = true
		version++
		_, e = tx.Exec("UPDATE works SET physical_key=?,size=?,mtime=?,modified_at=?,version=?,index_status='pending_stability',error_code='',error_message='' WHERE id=?", nullString(physical), size, mtime, st.ModTime().UnixNano(), version, existingID)
	}
	if e == nil && !isNew && !changed && (previousPhysical != physical && physical != "" || existingModified != st.ModTime().UnixNano()) {
		_, e = tx.Exec("UPDATE works SET modified_at=?,physical_key=COALESCE(?,physical_key) WHERE id=?", st.ModTime().UnixNano(), nullString(physical), existingID)
	}
	if e != nil {
		return "", e
	}
	if isNew {
		if e = recordMoveCandidates(tx, root.ID, existingID, kind, size, mtime); e != nil {
			return "", e
		}
	}
	_, e = tx.Exec("INSERT INTO aliases(work_id,root_id,path,original_name,title,title_sort,parent,physical_key,size,mtime,seen,filename_tags,parser_version) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(root_id,path) DO UPDATE SET work_id=excluded.work_id,original_name=excluded.original_name,title=excluded.title,title_sort=excluded.title_sort,parent=excluded.parent,status='online',physical_key=excluded.physical_key,size=excluded.size,mtime=excluded.mtime,seen=excluded.seen,filename_tags=excluded.filename_tags,parser_version=excluded.parser_version", existingID, root.ID, rel, original, title, titleSort, path.Dir(rel), nullString(physical), size, mtime, scanMarker, tagJSON, parser)
	if e != nil {
		return "", e
	}
	if isNew || changed {
		typ := "stability"
		if j.Type == "scan" || j.Type == "reconcile" {
			typ = "stability_bulk"
		}
		jid, qe := enqueueTx(tx, typ, root.ID, existingID, j.Actor, j.Origin, fmt.Sprintf("stability:%s:%d", existingID, version), map[string]any{"version": version, "size": size, "mtime": mtime})
		if qe != nil {
			return "", qe
		}
		_, e = tx.Exec("UPDATE jobs SET not_before=? WHERE id=?", now()+int64(root.QuietSeconds)*1000, jid)
		if e != nil {
			return "", e
		}
	}
	if isNew && root.AutoCrawl {
		providers := root.Providers
		if len(providers) == 0 {
			providers = []string{"ehentai", "nhentai", "chaika"}
		}
		for _, provider := range providers {
			jid, qe := enqueueTx(tx, "auto_crawl", root.ID, existingID, j.Actor, "automatic", fmt.Sprintf("auto:%s:%d:%s:%s", existingID, version, provider, strings.Join(providers, ",")), map[string]any{"provider": provider})
			if qe != nil {
				return "", qe
			}
			_, e = tx.Exec("UPDATE jobs SET provider=?,not_before=? WHERE id=?", provider, now()+int64(root.QuietSeconds)*1000, jid)
			if e != nil {
				return "", e
			}
		}
	}
	if isNew || changed || newAlias || nameChanged || previousWork != existingID || previousStatus != "online" || existingModified != st.ModTime().UnixNano() {
		e = bump(tx)
	}
	if e != nil {
		return "", e
	}
	if e = tx.Commit(); e != nil {
		return "", e
	}
	a.Log.Debug("source observed", "root", root.ID, "path", rel, "work", existingID, "new", isNew, "changed", changed, "new_alias", newAlias)
	if counts, ok := ctx.Value(scanCountsKey{}).(*scanCounts); ok {
		if isNew {
			counts.new++
		}
		if changed {
			counts.changed++
		}
		if newAlias && !isNew {
			counts.aliases++
		}
	}
	if changed {
		a.cancelWorkPrefetch(existingID)
	}
	a.signal()
	return existingID, nil
}
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func (a *App) changedSource(ctx context.Context, al Alias, st os.FileInfo, force bool) error {
	root, e := a.getRoot(ctx, al.RootID)
	if e != nil {
		return e
	}
	j := Job{Type: "incremental", Origin: "observed_read", Created: now()}
	size, mtime := st.Size(), st.ModTime().UnixNano()
	if al.Kind == "directory" {
		rr, err := os.OpenRoot(root.Path)
		if err != nil {
			return err
		}
		size, mtime, _, err = galleryRevision(rr, al.Path)
		rr.Close()
		if err != nil {
			return err
		}
	}
	_, e = a.observe(ctx, root, al.Path, al.Kind, st, size, mtime, j, force)
	return e
}
func (a *App) stabilizeJob(ctx context.Context, j Job) error {
	al, e := a.AuthorizedAlias(ctx, User{Admin: true}, j.WorkID)
	if e != nil {
		_, _ = a.DB.ExecContext(ctx, "UPDATE jobs SET state='paused_source',error='Source unavailable; restore or reconcile explicitly',updated=? WHERE id=?", now(), j.ID)
		return errJobDeferred
	}
	var p struct {
		Version int64 `json:"version"`
		Size    int64 `json:"size"`
		Mtime   int64 `json:"mtime"`
	}
	if e = json.Unmarshal([]byte(j.Payload), &p); e != nil {
		return e
	}
	if al.Version != p.Version {
		return nil
	}
	root, e := a.getRoot(ctx, al.RootID)
	if e != nil {
		return e
	}
	r, e := os.OpenRoot(root.Path)
	if e != nil {
		return e
	}
	defer r.Close()
	st, e := r.Stat(al.Path)
	if e != nil {
		return e
	}
	size, mtime := st.Size(), st.ModTime().UnixNano()
	if al.Kind == "directory" {
		size, mtime, _, e = galleryRevision(r, al.Path)
		if e != nil {
			return e
		}
	}
	if size != p.Size || mtime != p.Mtime {
		_, e = a.observe(ctx, root, al.Path, al.Kind, st, size, mtime, j, false)
		return e
	}
	tx, e := a.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	_, e = tx.Exec("UPDATE works SET index_status='pending' WHERE id=? AND version=? AND index_status='pending_stability'", al.WorkID, al.Version)
	if e != nil {
		return e
	}
	if _, e = tx.Exec("UPDATE jobs SET state='queued',error='',not_before=0 WHERE work_id=? AND type='auto_crawl' AND state='paused_dependency'", al.WorkID); e != nil {
		return e
	}
	typ := "incremental"
	if j.Type == "stability_bulk" {
		typ = "index_bulk_item"
	}
	_, e = enqueueTx(tx, typ, root.ID, al.WorkID, j.Actor, j.Origin, fmt.Sprintf("index:%s:%d", al.WorkID, al.Version), nil)
	if e == nil {
		e = tx.Commit()
	}
	a.signal()
	return e
}
