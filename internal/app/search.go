package app

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

type searchCursor struct {
	Generation string `json:"g"`
	Query      string `json:"q"`
	ID         string `json:"id"`
	Number     int64  `json:"n,omitempty"`
	Title      string `json:"t,omitempty"`
}

func visibleSQL(alias, root string, u User) (string, []any) {
	return alias + ".status='online' AND " + root + ".status='online' AND (? OR EXISTS(SELECT 1 FROM grants g WHERE g.user_id=? AND g.root_id=" + alias + ".root_id))", []any{u.Admin, u.ID}
}
func catalogFilter(u User, root, q string, ids []string) (string, []any, error) {
	n, e := parseQuery(q)
	if e != nil {
		return "", nil, e
	}
	expr, args, e := compileQuery(n, "sa", u.ID)
	if e != nil {
		return "", nil, e
	}
	visible, acl := visibleSQL("sa", "sr", u)
	s := "w.id IN (SELECT sa.work_id FROM aliases sa JOIN roots sr ON sr.id=sa.root_id WHERE " + visible + " AND (" + expr + ")"
	params := append(acl, args...)
	if root != "" {
		s += " AND sa.root_id=?"
		params = append(params, root)
	}
	s += ")"
	if len(ids) > 0 {
		if len(ids) > 1000 {
			return "", nil, errors.New("too many selected works")
		}
		s += " AND w.id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + ")"
		for _, id := range ids {
			params = append(params, id)
		}
	}
	return s, params, nil
}
func (a *App) targetCount(ctx context.Context, u User, root string, ids []string, q string) (int, error) {
	where, args, e := catalogFilter(u, root, q, ids)
	if e != nil {
		return 0, e
	}
	var n int
	e = a.DB.QueryRowContext(ctx, "SELECT count(*) FROM works w WHERE "+where, args...).Scan(&n)
	return n, e
}
func (a *App) targetIDs(ctx context.Context, u User, root string, ids []string, q, after string, before int64, limit int) ([]string, error) {
	where, args, e := catalogFilter(u, root, q, ids)
	if e != nil {
		return nil, e
	}
	args = append(args, after, before, limit)
	rows, e := a.DB.QueryContext(ctx, "SELECT w.id FROM works w WHERE "+where+" AND w.id>? AND w.registered_at<=? ORDER BY w.id LIMIT ?", args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
func (a *App) works(w http.ResponseWriter, r *http.Request, u User) {
	q := r.URL.Query().Get("q")
	root := r.URL.Query().Get("root")
	sort, column, direction, ok := catalogOrder(w, r)
	if !ok {
		return
	}
	limit, e := optionalInt(r, "limit", 40)
	if e != nil || limit < 1 || limit > 100 {
		apiError(w, 400, "invalid_limit", "Page size must be 1–100")
		return
	}
	page, e := optionalInt(r, "page", 1)
	if e != nil || page < 1 || page > 10000 {
		apiError(w, 400, "invalid_page", "Use cursor navigation beyond page 10000")
		return
	}
	where, args, e := catalogFilter(u, root, q, nil)
	if e != nil {
		apiError(w, 400, "invalid_query", e.Error())
		return
	}
	generation := a.Generation(r.Context())
	sum := sha256.Sum256([]byte(u.ID + "\x00" + q + "\x00" + root + "\x00" + sort + "\x00" + direction))
	queryKey := hex.EncodeToString(sum[:16])
	cursor := searchCursor{}
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		b, e := base64.RawURLEncoding.DecodeString(raw)
		if e != nil || len(b) > 8192 || json.Unmarshal(b, &cursor) != nil || cursor.Query != queryKey {
			apiError(w, 400, "invalid_cursor", "Invalid search cursor")
			return
		}
		if cursor.Generation != generation {
			apiError(w, 409, "cursor_stale", "Library changed; refresh the listing")
			return
		}
	}
	var total int
	e = a.DB.QueryRowContext(r.Context(), "SELECT count(*) FROM works w WHERE "+where, args...).Scan(&total)
	if e != nil {
		apiError(w, 503, "database", "Cannot count search results")
		return
	}
	from, params := catalogFrom(u, where, args)
	query := "SELECT " + aliasColumns + ",al.title_sort" + from
	if cursor.ID != "" {
		operator := ">"
		if direction == "desc" {
			operator = "<"
		}
		query += " AND (" + column + ",w.id) " + operator + " (?,?)"
		if sort == "title" {
			params = append(params, cursor.Title, cursor.ID)
		} else {
			params = append(params, cursor.Number, cursor.ID)
		}
	}
	query += " ORDER BY " + column + " " + direction + ",w.id " + direction + " LIMIT ?"
	params = append(params, limit+1)
	if cursor.ID == "" && page > 1 {
		query += " OFFSET ?"
		params = append(params, (page-1)*limit)
	}
	rows, e := a.DB.QueryContext(r.Context(), query, params...)
	if e != nil {
		apiError(w, 503, "database", "Search failed")
		return
	}
	items := []Alias{}
	keys := []string{}
	for rows.Next() {
		var v Alias
		var titleKey string
		e = rows.Scan(&v.ID, &v.WorkID, &v.RootID, &v.RootName, &v.Path, &v.RootPath, &v.OriginalName, &v.Title, &v.Kind, &v.Size, &v.Mtime, &v.Version, &v.RegisteredAt, &v.IndexStatus, &v.ErrorCode, &v.PhysicalKey, &v.Cover, &v.CoverUnresolved, &v.ModifiedAt, &titleKey)
		if e != nil {
			break
		}
		items = append(items, v)
		keys = append(keys, titleKey)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		apiError(w, 503, "database", "Cannot read search results")
		return
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		c := searchCursor{Generation: generation, Query: queryKey, ID: last.WorkID, Title: keys[limit-1], Number: last.RegisteredAt}
		if sort == "mtime" {
			c.Number = last.ModifiedAt
		}
		b, _ := json.Marshal(c)
		next = base64.RawURLEncoding.EncodeToString(b)
	}
	if e = a.markFavorites(r.Context(), u, items); e != nil {
		apiError(w, 503, "database", "Cannot load favorite state")
		return
	}
	if a.Generation(r.Context()) != generation {
		apiError(w, 409, "cursor_stale", "Library changed; refresh the listing")
		return
	}
	jsonResponse(w, 200, map[string]any{"items": items, "total": total, "next_cursor": next, "generation": generation, "page": page, "limit": limit})
}
func (a *App) savedSearches(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method == "GET" {
		rows, e := a.DB.QueryContext(r.Context(), "SELECT id,name,query,sort FROM saved_searches WHERE user_id=? ORDER BY name LIMIT 200", u.ID)
		if e != nil {
			apiError(w, 503, "database", "Cannot load saved searches")
			return
		}
		defer rows.Close()
		out := []map[string]string{}
		for rows.Next() {
			var id, n, q, s string
			_ = rows.Scan(&id, &n, &q, &s)
			out = append(out, map[string]string{"id": id, "name": n, "query": q, "sort": s})
		}
		jsonResponse(w, 200, out)
		return
	}
	if r.Method == "DELETE" {
		_, e := a.DB.ExecContext(r.Context(), "DELETE FROM saved_searches WHERE id=? AND user_id=?", r.PathValue("id"), u.ID)
		if e != nil {
			apiError(w, 503, "database", "Cannot delete saved search")
			return
		}
		jsonResponse(w, 200, map[string]bool{"ok": true})
		return
	}
	var in struct {
		Name  string `json:"name"`
		Query string `json:"query"`
		Sort  string `json:"sort"`
	}
	if decodeJSON(w, r, &in) != nil || !validName(in.Name) {
		apiError(w, 400, "invalid_request", "A saved search needs a name")
		return
	}
	if _, e := parseQuery(in.Query); e != nil {
		apiError(w, 400, "invalid_query", e.Error())
		return
	}
	var count int
	_ = a.DB.QueryRowContext(r.Context(), "SELECT count(*) FROM saved_searches WHERE user_id=?", u.ID).Scan(&count)
	if count >= 200 {
		apiError(w, 400, "limit", "Saved-search limit reached")
		return
	}
	sid := id()
	_, e := a.DB.ExecContext(r.Context(), "INSERT INTO saved_searches VALUES(?,?,?,?,?)", sid, u.ID, in.Name, in.Query, in.Sort)
	if e != nil {
		apiError(w, 503, "database", "Cannot save search")
		return
	}
	jsonResponse(w, 201, map[string]string{"id": sid})
}
func (a *App) offline(w http.ResponseWriter, r *http.Request, u User) {
	rows, e := a.DB.QueryContext(r.Context(), "SELECT w.id,al.title,al.root_id,al.path,w.error_code,w.index_status,r.status,al.status,w.size,w.mtime,r.checked_at FROM aliases al JOIN roots r ON r.id=al.root_id JOIN works w ON w.id=al.work_id WHERE al.status!='online' OR r.status!='online' ORDER BY r.id,al.path LIMIT 500")
	if e != nil {
		apiError(w, 503, "database", "Cannot list offline works")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, title, root, p, code, status, rs, als string
		var size, mtime, checked int64
		_ = rows.Scan(&id, &title, &root, &p, &code, &status, &rs, &als, &size, &mtime, &checked)
		out = append(out, map[string]any{"id": id, "title": title, "root_id": root, "relative_path": p, "error_code": code, "index_status": status, "root_status": rs, "alias_status": als, "size": size, "mtime": mtime, "checked_at": checked})
	}
	jsonResponse(w, 200, map[string]any{"items": out, "total": len(out)})
}

func catalogOrder(w http.ResponseWriter, r *http.Request) (string, string, string, bool) {
	sort := r.URL.Query().Get("sort")
	if sort == "" {
		sort = "recent"
	}
	column := "w.registered_at"
	switch sort {
	case "recent":
	case "title":
		column = "al.title_sort"
	case "mtime":
		column = "COALESCE(NULLIF(w.modified_at,0),w.mtime)"
	default:
		apiError(w, 400, "invalid_sort", "Choose recent, title or mtime")
		return "", "", "", false
	}
	direction := r.URL.Query().Get("direction")
	if direction == "" {
		direction = "desc"
		if sort == "title" {
			direction = "asc"
		}
	}
	if direction != "asc" && direction != "desc" {
		apiError(w, 400, "invalid_direction", "Choose asc or desc")
		return "", "", "", false
	}
	return sort, column, direction, true
}

func catalogFrom(u User, where string, args []any) (string, []any) {
	canonical, canonicalArgs := visibleSQL("x", "xr", u)
	return " FROM works w JOIN aliases al ON al.id=(SELECT x.id FROM aliases x JOIN roots xr ON xr.id=x.root_id WHERE x.work_id=w.id AND " + canonical + " ORDER BY x.root_id,x.path LIMIT 1) JOIN roots r ON r.id=al.root_id WHERE " + where, append(canonicalArgs, args...)
}

type neighborWork struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

func (a *App) neighbors(w http.ResponseWriter, r *http.Request, u User) {
	_, column, direction, ok := catalogOrder(w, r)
	if !ok {
		return
	}
	where, args, e := catalogFilter(u, r.URL.Query().Get("root"), r.URL.Query().Get("q"), nil)
	if e != nil {
		apiError(w, 400, "invalid_query", e.Error())
		return
	}
	from, params := catalogFrom(u, where, args)
	out := struct {
		Previous *neighborWork `json:"previous"`
		Next     *neighborWork `json:"next"`
	}{}
	tx, e := a.DB.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if e != nil {
		apiError(w, 503, "database", "Cannot load neighbors")
		return
	}
	defer tx.Rollback()
	var key any
	id := r.PathValue("id")
	e = tx.QueryRowContext(r.Context(), "SELECT "+column+from+" AND w.id=?", append(append([]any{}, params...), id)...).Scan(&key)
	if errors.Is(e, sql.ErrNoRows) {
		jsonResponse(w, 200, out)
		return
	}
	if e != nil {
		apiError(w, 503, "database", "Cannot load neighbors")
		return
	}
	for i, target := range []**neighborWork{&out.Previous, &out.Next} {
		order, operator := direction, ">"
		if i == 0 {
			order = "desc"
			if direction == "desc" {
				order = "asc"
			}
		}
		if order == "desc" {
			operator = "<"
		}
		query := "SELECT w.id,al.title" + from + " AND (" + column + ",w.id) " + operator + " (?,?) ORDER BY " + column + " " + order + ",w.id " + order + " LIMIT 1"
		v := &neighborWork{}
		e = tx.QueryRowContext(r.Context(), query, append(append([]any{}, params...), key, id)...).Scan(&v.ID, &v.Title)
		if errors.Is(e, sql.ErrNoRows) {
			continue
		}
		if e != nil {
			apiError(w, 503, "database", "Cannot load neighbors")
			return
		}
		*target = v
	}
	jsonResponse(w, 200, out)
}
