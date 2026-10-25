package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	ar "archika/internal/archive"
)

func (a *App) workDetail(w http.ResponseWriter, r *http.Request, u User) {
	select {
	case a.delivery <- struct{}{}:
		defer func() { <-a.delivery }()
	case <-r.Context().Done():
		return
	default:
		apiError(w, 429, "busy", "Manifest request budget exhausted")
		return
	}
	al, e := a.AuthorizedAlias(r.Context(), u, r.PathValue("id"))
	if e != nil {
		apiError(w, 404, "not_found", "Work unavailable")
		return
	}
	if e = a.ensureIndex(r.Context(), al); e != nil {
		code := "index_failed"
		status := 422
		if errors.Is(e, errStabilizing) {
			code = "pending_stability"
			status = 409
		}
		apiError(w, status, code, "Work is not readable yet; inspect its index status or retry explicitly")
		return
	}
	al, e = a.AuthorizedAlias(r.Context(), u, al.WorkID)
	if e != nil {
		apiError(w, 404, "not_found", "Work unavailable")
		return
	}
	pages, e := a.pageList(r.Context(), al)
	if e != nil {
		apiError(w, 503, "database", "Cannot load pages")
		return
	}
	var caps string
	_ = a.DB.QueryRowContext(r.Context(), "SELECT capabilities FROM works WHERE id=?", al.WorkID).Scan(&caps)
	var capabilities any
	_ = json.Unmarshal([]byte(caps), &capabilities)
	metadata, _ := a.crawler.Metadata(r.Context(), u, al.WorkID)
	if e = a.DB.QueryRowContext(r.Context(), "SELECT EXISTS(SELECT 1 FROM favorites WHERE user_id=? AND work_id=?)", u.ID, al.WorkID).Scan(&al.Favorite); e != nil {
		apiError(w, 503, "database", "Cannot load favorite state")
		return
	}
	jsonResponse(w, 200, map[string]any{"work": al, "pages": pages, "capabilities": capabilities, "metadata": metadata, "cover_unresolved": al.CoverUnresolved})
}
func (a *App) loadPage(ctx context.Context, u User, al Alias, p ar.Entry, t Transform, speculative bool) (PageValue, error) {
	actual, e := a.readableAlias(ctx, u, al.WorkID, al.Version)
	if e != nil {
		return PageValue{}, e
	}
	al = actual
	if e = a.verifyDirectoryPage(ctx, al, p); e != nil {
		return PageValue{}, e
	}
	originalKey := fmt.Sprintf("original-page/%s/%d/%s", al.WorkID, al.Version, p.ID)
	cacheContext := context.WithValue(ctx, backgroundKey{}, speculative || background(ctx))
	v, e := a.cache.Load(cacheContext, originalKey, al.Kind == "rar" || al.Kind == "7z", func(shared context.Context) (PageValue, error) {
		if e := a.acquire(shared, speculative || background(ctx)); e != nil {
			return PageValue{}, e
		}
		defer a.release()
		start := time.Now()
		var value PageValue
		e := a.sessions.With(shared, al, func(engine ar.Adapter) error {
			stream, e := engine.Open(shared, p)
			if e != nil {
				return e
			}
			defer stream.Close()
			var b []byte
			if raw, ok := stream.(*sliceReader); ok {
				b = raw.b
				raw.b = nil
			} else {
				b, e = boundedRead(shared, stream, a.Config.PageBytes)
				if e != nil {
					return e
				}
			}
			value = PageValue{Bytes: b, MIME: sniffMIME(b, p.MIME)}
			return nil
		})
		a.costsMu.Lock()
		if len(a.costs) > 4096 {
			clear(a.costs)
		}
		old := a.costs[al.WorkID]
		seconds := time.Since(start).Seconds()
		if old == 0 {
			old = seconds
		}
		a.costs[al.WorkID] = old*.7 + seconds*.3
		a.costsMu.Unlock()
		return value, e
	})
	if e != nil {
		return PageValue{}, e
	}
	if t.Format != "" || t.Width > 0 {
		key := fmt.Sprintf("resized-page/%s/%d/%s/%s/%d/%d/%s/%t", al.WorkID, al.Version, p.ID, newImageProcessor().Version(), t.Width, t.Quality, t.Format, t.Thumbnail)
		v, e = a.cache.Load(cacheContext, key, true, func(shared context.Context) (PageValue, error) {
			select {
			case a.transform <- struct{}{}:
			case <-shared.Done():
				return PageValue{}, shared.Err()
			}
			defer func() { <-a.transform }()
			return processImage(shared, v, t, a.Config.MaxPixels, a.Config.PageBytes, false)
		})
	}
	if e != nil {
		return PageValue{}, e
	}
	if !a.authorized(ctx, u, al.WorkID) {
		return PageValue{}, errors.New("forbidden")
	}
	var version int64
	_ = a.DB.QueryRowContext(ctx, "SELECT version FROM works WHERE id=?", al.WorkID).Scan(&version)
	if version != al.Version {
		return PageValue{}, errors.New("source_changed")
	}
	verified, e := a.checkSource(ctx, al)
	if e != nil {
		return PageValue{}, e
	}
	verified.Close()
	if e = a.verifyDirectoryPage(ctx, al, p); e != nil {
		return PageValue{}, e
	}
	return v, nil
}
func (a *App) pageHTTP(w http.ResponseWriter, r *http.Request, u User) {
	select {
	case a.delivery <- struct{}{}:
		defer func() { <-a.delivery }()
	case <-r.Context().Done():
		return
	default:
		apiError(w, 429, "busy", "Page delivery budget exhausted; retry later")
		return
	}
	al, e := a.AuthorizedAlias(r.Context(), u, r.PathValue("id"))
	if e != nil {
		apiError(w, 404, "not_found", "Work unavailable")
		return
	}
	if e = a.ensureIndex(r.Context(), al); e != nil {
		apiError(w, 409, "index_unavailable", "Page index is unavailable")
		return
	}
	ordinal, e := strconv.Atoi(r.PathValue("page"))
	if e != nil || ordinal < 0 {
		apiError(w, 404, "not_found", "Page unavailable")
		return
	}
	p, e := a.page(r.Context(), al, ordinal)
	if e != nil {
		apiError(w, 404, "not_found", "Page unavailable")
		return
	}
	width, e := optionalInt(r, "width", 0)
	if e != nil {
		apiError(w, 400, "invalid_transform", "Invalid width")
		return
	}
	quality, e := optionalInt(r, "quality", 85)
	if e != nil {
		apiError(w, 400, "invalid_transform", "Invalid quality")
		return
	}
	t, e := validateTransform(r.URL.Query().Get("format"), width, quality)
	if e != nil {
		apiError(w, 400, "invalid_transform", e.Error())
		return
	}
	value, e := a.loadPage(r.Context(), u, al, p, t, false)
	if e != nil {
		status := 422
		code := "page_failed"
		if errors.Is(e, errUnsupportedTransform) {
			status = 415
			code = "unsupported_transform"
		}
		apiError(w, status, code, "Page is unreadable or the requested representation is unsupported")
		return
	}
	a.writeImage(w, r, value, fmt.Sprintf("%s-%d-%s-%v", al.WorkID, al.Version, p.ID, t))
}
func optionalInt(r *http.Request, key string, defaultValue int) (int, error) {
	v := r.URL.Query().Get(key)
	if v == "" {
		return defaultValue, nil
	}
	return strconv.Atoi(v)
}
func (a *App) writeImage(w http.ResponseWriter, r *http.Request, v PageValue, key string) {
	sum := sha256.Sum256([]byte(key))
	etag := "\"" + hex.EncodeToString(sum[:]) + "\""
	w.Header().Set("Content-Type", v.MIME)
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("ETag", etag)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if v.Still {
		w.Header().Set("X-ARChika-Still-Fallback", "true")
	}
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(304)
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(v.Bytes)))
	_, _ = w.Write(v.Bytes)
}
func (a *App) thumbnail(ctx context.Context, u User, al Alias) (PageValue, string, error) {
	actual, e := a.readableAlias(ctx, u, al.WorkID, al.Version)
	if e != nil {
		return PageValue{}, "", e
	}
	al = actual
	if e = a.ensureIndex(ctx, al); e != nil {
		return PageValue{}, "", e
	}
	var p ar.Entry
	e = a.DB.QueryRowContext(ctx, "SELECT ordinal,entry,entry_id,size,mime,locator,width,height FROM pages WHERE work_id=? AND version=? ORDER BY CASE WHEN entry_id=? THEN 0 ELSE 1 END,ordinal LIMIT 1", al.WorkID, al.Version, al.Cover).Scan(&p.Ordinal, &p.Path, &p.ID, &p.Size, &p.MIME, &p.Locator, &p.Width, &p.Height)
	if e != nil {
		return PageValue{}, "", e
	}
	key := fmt.Sprintf("thumb/%s/%d/%s/%s/320", al.WorkID, al.Version, p.ID, newImageProcessor().Version())
	sum := sha256.Sum256([]byte(key))
	name := al.WorkID + "-" + hex.EncodeToString(sum[:12]) + ".png"
	file := filepath.Join(a.Config.DataDir, "thumbnails", name)
	var storedKey, status, storedPath string
	e = a.DB.QueryRowContext(ctx, "SELECT key,status,path FROM thumbnails WHERE work_id=?", al.WorkID).Scan(&storedKey, &status, &storedPath)
	if e == nil && storedKey == key {
		if status == "failed" {
			return PageValue{}, key, errUnsupportedTransform
		}
		if status == "ready" {
			b, e := os.ReadFile(file)
			if e == nil && int64(len(b)) <= a.Config.PageBytes {
				return PageValue{Bytes: b, MIME: "image/png"}, key, nil
			}
		}
	}
	value, e := a.cache.Load(ctx, key, false, func(shared context.Context) (PageValue, error) {
		v, e := a.loadPage(shared, u, al, p, Transform{Width: 320, Quality: 80, Format: "png", Thumbnail: true}, false)
		if e != nil {
			if shared.Err() == nil && permanentThumbnailError(e) {
				_, _ = a.DB.ExecContext(shared, "INSERT INTO thumbnails(work_id,key,status,error) VALUES(?,?,'failed','unsupported or unreadable thumbnail') ON CONFLICT(work_id) DO UPDATE SET key=excluded.key,status=excluded.status,error=excluded.error", al.WorkID, key)
			}
			return PageValue{}, e
		}
		dir := filepath.Dir(file)
		if e = os.MkdirAll(dir, 0700); e != nil {
			return PageValue{}, e
		}
		tmp, e := os.CreateTemp(dir, ".thumb-")
		if e != nil {
			return PageValue{}, e
		}
		defer os.Remove(tmp.Name())
		_, e = tmp.Write(v.Bytes)
		ce := tmp.Close()
		if e == nil {
			e = ce
		}
		if e == nil {
			e = os.Rename(tmp.Name(), file)
		}
		if e != nil {
			return PageValue{}, e
		}
		_, e = a.DB.ExecContext(shared, "INSERT INTO thumbnails(work_id,key,path,status,error) VALUES(?,?,?,'ready','') ON CONFLICT(work_id) DO UPDATE SET key=excluded.key,path=excluded.path,status='ready',error=''", al.WorkID, key, file)
		if e != nil {
			return PageValue{}, e
		}
		if storedPath != "" && storedPath != file && within(dir, storedPath) {
			_ = os.Remove(storedPath)
		}
		return v, nil
	})
	return value, key, e
}
func permanentThumbnailError(e error) bool {
	if errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(e, errUnsupportedTransform) {
		return true
	}
	switch decodeErrorCode(e) {
	case "unsupported_format", "corrupt", "encrypted_or_password_required":
		return true
	}
	return false
}
func (a *App) thumbnailHTTP(w http.ResponseWriter, r *http.Request, u User) {
	select {
	case a.thumbnailQueue <- struct{}{}:
		defer func() { <-a.thumbnailQueue }()
	default:
		apiError(w, 429, "busy", "Thumbnail queue is full")
		return
	}
	select {
	case a.thumbnails <- struct{}{}:
		defer func() { <-a.thumbnails }()
	case <-r.Context().Done():
		return
	}
	al, e := a.AuthorizedAlias(r.Context(), u, r.PathValue("id"))
	if e != nil {
		apiError(w, 404, "not_found", "Work unavailable")
		return
	}
	v, key, e := a.thumbnail(r.Context(), u, al)
	if e != nil {
		status := 415
		if errors.Is(e, errStabilizing) {
			status = 409
		}
		apiError(w, status, "thumbnail_unavailable", "Thumbnail is unavailable")
		return
	}
	if !a.authorized(r.Context(), u, al.WorkID) {
		apiError(w, 404, "not_found", "Work unavailable")
		return
	}
	a.writeImage(w, r, v, key)
}
func (a *App) coverHTTP(w http.ResponseWriter, r *http.Request, u User) {
	al, e := a.AuthorizedAlias(r.Context(), u, r.PathValue("id"))
	if e != nil {
		apiError(w, 404, "not_found", "Work unavailable")
		return
	}
	var in struct {
		EntryID string `json:"entry_id"`
	}
	if decodeJSON(w, r, &in) != nil {
		apiError(w, 400, "invalid_request", "Invalid cover selection")
		return
	}
	if in.EntryID == "" && !u.Admin {
		apiError(w, 403, "forbidden", "Administrator required to reset the shared cover")
		return
	}
	tx, e := a.DB.BeginTx(r.Context(), nil)
	if e != nil {
		apiError(w, 503, "database", "Cannot update cover")
		return
	}
	defer tx.Rollback()
	if in.EntryID != "" {
		var n int
		_ = tx.QueryRow("SELECT count(*) FROM pages WHERE work_id=? AND version=? AND entry_id=?", al.WorkID, al.Version, in.EntryID).Scan(&n)
		if n != 1 {
			apiError(w, 400, "invalid_cover", "Select a page from the indexed work")
			return
		}
	}
	_, e = tx.Exec("UPDATE works SET cover=?,cover_unresolved=0 WHERE id=?", in.EntryID, al.WorkID)
	if e == nil {
		_, e = tx.Exec("UPDATE thumbnails SET status='stale' WHERE work_id=?", al.WorkID)
	}
	if e == nil {
		e = audit(tx, u.ID, "cover.select", al.RootID, map[string]string{"work": al.WorkID, "entry": in.EntryID})
	}
	if e == nil {
		e = tx.Commit()
	}
	if e != nil {
		apiError(w, 503, "database", "Cannot update cover")
		return
	}
	jsonResponse(w, 200, map[string]bool{"ok": true})
}
