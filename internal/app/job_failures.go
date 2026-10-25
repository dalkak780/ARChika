package app

import (
	"context"
	"database/sql"
)

const failureSchema = `CREATE TABLE IF NOT EXISTS job_failures(job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,work_id TEXT NOT NULL REFERENCES works(id) ON DELETE CASCADE,error TEXT NOT NULL,PRIMARY KEY(job_id,work_id));`

func (a *App) failedJobIDs(ctx context.Context, job, after string) ([]string, error) {
	rows, e := a.DB.QueryContext(ctx, "SELECT work_id FROM job_failures WHERE job_id=? AND work_id>? ORDER BY work_id LIMIT 64", job, after)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func recordFailure(tx *sql.Tx, job, work string, failure error) error {
	if failure == nil {
		_, e := tx.Exec("DELETE FROM job_failures WHERE job_id=? AND work_id=?", job, work)
		return e
	}
	_, e := tx.Exec("INSERT INTO job_failures VALUES(?,?,?) ON CONFLICT(job_id,work_id) DO UPDATE SET error=excluded.error", job, work, safeError(failure))
	return e
}
