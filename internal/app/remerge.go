package app

import (
	"context"
	"encoding/json"
)

func (a *App) remergeJob(ctx context.Context, j Job) error {
	var p struct {
		Provider string `json:"provider"`
	}
	if e := json.Unmarshal([]byte(j.Payload), &p); e != nil {
		return e
	}
	var cp struct {
		After string `json:"after"`
	}
	_ = json.Unmarshal([]byte(j.Checkpoint), &cp)
	for {
		if e := a.jobAlive(ctx, j.ID); e != nil {
			return e
		}
		rows, e := a.DB.QueryContext(ctx, "SELECT work_id FROM provider_snapshots WHERE provider=? AND work_id>? ORDER BY work_id LIMIT 64", p.Provider, cp.After)
		if e != nil {
			return e
		}
		ids := []string{}
		for rows.Next() {
			var id string
			_ = rows.Scan(&id)
			ids = append(ids, id)
		}
		rows.Close()
		if len(ids) == 0 {
			return nil
		}
		tx, e := a.DB.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		for _, id := range ids {
			if e = mergeMetadata(tx, id); e != nil {
				break
			}
			cp.After = id
		}
		if e == nil {
			checkpoint, _ := json.Marshal(cp)
			_, e = tx.Exec("UPDATE jobs SET done=done+?,checkpoint=?,updated=? WHERE id=?", len(ids), string(checkpoint), now(), j.ID)
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
