package app

import (
	"context"
	"net/http"
	"strings"
)

const favoriteSchema = `CREATE TABLE IF NOT EXISTS favorites(user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,work_id TEXT NOT NULL REFERENCES works(id) ON DELETE CASCADE,PRIMARY KEY(user_id,work_id));`

func (a *App) favoriteHTTP(w http.ResponseWriter, r *http.Request, u User) {
	work := r.PathValue("id")
	if !a.authorized(r.Context(), u, work) {
		apiError(w, 404, "not_found", "Work unavailable")
		return
	}
	var in struct {
		Favorite bool `json:"favorite"`
	}
	if decodeJSON(w, r, &in) != nil {
		apiError(w, 400, "invalid_request", "Invalid favorite selection")
		return
	}
	tx, e := a.DB.BeginTx(r.Context(), nil)
	if e != nil {
		apiError(w, 503, "database", "Cannot update favorite")
		return
	}
	defer tx.Rollback()
	if in.Favorite {
		_, e = tx.Exec("INSERT OR IGNORE INTO favorites VALUES(?,?)", u.ID, work)
	} else {
		_, e = tx.Exec("DELETE FROM favorites WHERE user_id=? AND work_id=?", u.ID, work)
	}
	if e == nil {
		e = bump(tx)
	}
	if e == nil {
		e = tx.Commit()
	}
	if e != nil {
		apiError(w, 503, "database", "Cannot update favorite")
		return
	}
	jsonResponse(w, 200, map[string]bool{"ok": true, "favorite": in.Favorite})
}
func (a *App) markFavorites(ctx context.Context, u User, items []Alias) error {
	if len(items) == 0 {
		return nil
	}
	args := []any{u.ID}
	indices := make(map[string]int, len(items))
	for i, item := range items {
		args = append(args, item.WorkID)
		indices[item.WorkID] = i
	}
	rows, e := a.DB.QueryContext(ctx, "SELECT work_id FROM favorites WHERE user_id=? AND work_id IN ("+strings.TrimSuffix(strings.Repeat("?,", len(items)), ",")+")", args...)
	if e != nil {
		return e
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			return e
		}
		if index, ok := indices[id]; ok {
			items[index].Favorite = true
		}
	}
	return rows.Err()
}
