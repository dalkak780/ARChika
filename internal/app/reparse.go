package app

import (
	"context"
	"encoding/json"
	"fmt"
)

func (a *App) reparseJob(ctx context.Context, j Job) error {
	root, e := a.getRoot(ctx, j.RootID)
	if e != nil {
		return e
	}
	var cp struct {
		After int64 `json:"after"`
	}
	_ = json.Unmarshal([]byte(j.Checkpoint), &cp)
	for {
		if e = a.jobAlive(ctx, j.ID); e != nil {
			return e
		}
		rows, e := a.DB.QueryContext(ctx, "SELECT id,title FROM aliases WHERE root_id=? AND id>? ORDER BY id LIMIT 128", root.ID, cp.After)
		if e != nil {
			return e
		}
		type item struct {
			id    int64
			title string
		}
		items := []item{}
		for rows.Next() {
			var v item
			if e = rows.Scan(&v.id, &v.title); e != nil {
				rows.Close()
				return e
			}
			items = append(items, v)
		}
		rows.Close()
		if len(items) == 0 {
			return nil
		}
		tx, e := a.DB.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		for _, v := range items {
			tags, _ := json.Marshal(filenameTags(v.title, root.ParserRegex, root.ParserEnabled))
			_, e = tx.Exec("UPDATE aliases SET filename_tags=?,parser_version=? WHERE id=?", string(tags), filenameParserVersion+":"+root.ParserRegex+fmt.Sprint(root.ParserEnabled), v.id)
			if e != nil {
				break
			}
			cp.After = v.id
		}
		b, _ := json.Marshal(cp)
		if e == nil {
			_, e = tx.Exec("UPDATE jobs SET checkpoint=?,done=done+?,updated=? WHERE id=?", string(b), len(items), now(), j.ID)
		}
		if e == nil {
			e = bump(tx)
		}
		if e == nil {
			e = tx.Commit()
		} else {
			tx.Rollback()
		}
		if e != nil {
			return e
		}
	}
}
