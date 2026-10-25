package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

type Job struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	RootID     string `json:"root_id"`
	WorkID     string `json:"work_id"`
	Provider   string `json:"provider"`
	Actor      string `json:"actor"`
	State      string `json:"state"`
	Origin     string `json:"origin"`
	Payload    string `json:"payload"`
	Checkpoint string `json:"checkpoint"`
	Done       int    `json:"done"`
	Failed     int    `json:"failed"`
	Total      int    `json:"total"`
	Error      string `json:"error"`
	Created    int64  `json:"created"`
	Updated    int64  `json:"updated"`
	Attempts   int    `json:"attempts"`
}

const jobColumns = "id,type,root_id,work_id,provider,actor,state,origin,payload,checkpoint,done,failed,total,error,created,updated,attempts"

var errJobStopped = errors.New("job paused or cancelled")
var errJobDeferred = errors.New("job deferred")

func enqueueTx(tx *sql.Tx, kind, root, work, actor, origin, dedup string, payload any) (string, error) {
	b, e := json.Marshal(payload)
	if e != nil {
		return "", e
	}
	jid := id()
	var key any
	if dedup != "" {
		key = dedup
	}
	_, e = tx.Exec("INSERT INTO jobs(id,type,root_id,work_id,actor,origin,payload,dedup,created,updated) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(dedup) DO NOTHING", jid, kind, root, work, actor, origin, string(b), key, now(), now())
	if e != nil {
		return "", e
	}
	if dedup != "" {
		e = tx.QueryRow("SELECT id FROM jobs WHERE dedup=?", dedup).Scan(&jid)
	}
	return jid, e
}
func scanJob(s interface{ Scan(...any) error }) (Job, error) {
	var j Job
	e := s.Scan(&j.ID, &j.Type, &j.RootID, &j.WorkID, &j.Provider, &j.Actor, &j.State, &j.Origin, &j.Payload, &j.Checkpoint, &j.Done, &j.Failed, &j.Total, &j.Error, &j.Created, &j.Updated, &j.Attempts)
	return j, e
}
func (a *App) jobLoop() {
	t := time.NewTicker(300 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-t.C:
		case <-a.jobWake:
		}
		var paused string
		_ = a.DB.QueryRowContext(a.ctx, "SELECT value FROM settings WHERE key='pause_jobs'").Scan(&paused)
		if paused == "1" {
			continue
		}
		if e := a.processWatchEvents(a.ctx); e != nil && a.ctx.Err() == nil {
			a.Log.Warn("watch event processing failed", "error_type", fmt.Sprintf("%T", e))
		}
		j, e := scanJob(a.DB.QueryRowContext(a.ctx, "SELECT "+jobColumns+" FROM jobs WHERE state='queued' AND not_before<=? ORDER BY CASE WHEN type='incremental' THEN 0 WHEN type='stability' THEN 1 WHEN type='auto_crawl' THEN 3 ELSE 2 END,created LIMIT 1", now()))
		if e != nil {
			continue
		}
		res, e := a.DB.ExecContext(a.ctx, "UPDATE jobs SET state='running',updated=?,attempts=attempts+1 WHERE id=? AND state='queued'", now(), j.ID)
		if e != nil {
			continue
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			continue
		}
		ctx, cancel := context.WithCancel(a.ctx)
		monitorDone := make(chan struct{})
		go func() {
			ticker := time.NewTicker(250 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-monitorDone:
					return
				case <-ticker.C:
					if aliveErr := a.jobAlive(ctx, j.ID); aliveErr != nil {
						if errors.Is(aliveErr, errJobStopped) || errors.Is(aliveErr, sql.ErrNoRows) {
							cancel()
							return
						}
						if ctx.Err() == nil {
							a.Log.Warn("background job monitor failed", "job", j.ID, "error_type", fmt.Sprintf("%T", aliveErr))
						}
					}
				}
			}
		}()
		if aliveErr := a.jobAlive(ctx, j.ID); aliveErr != nil {
			e = aliveErr
		} else {
			e = a.runJob(context.WithValue(ctx, backgroundKey{}, true), j)
		}
		close(monitorDone)
		cancel()
		a.signal()
		if a.ctx.Err() != nil {
			return
		}
		if errors.Is(e, errJobStopped) || errors.Is(e, errJobDeferred) {
			continue
		}
		state := "completed"
		msg := ""
		if e != nil {
			state = "failed"
			msg = safeError(e)
			a.Log.Warn("background job failed", "job", j.ID, "type", j.Type, "error_type", fmt.Sprintf("%T", e))
		}
		single := j.WorkID != ""
		failedAttempt := 0
		if single && e != nil {
			failedAttempt = 1
		}
		_, _ = a.DB.ExecContext(a.ctx, "UPDATE jobs SET state=?,error=CASE WHEN ?!='' THEN ? ELSE error END,done=CASE WHEN ? THEN 1 ELSE done END,total=CASE WHEN ? THEN 1 WHEN ?='completed' THEN done ELSE total END,failed=failed+?,updated=? WHERE id=? AND state='running'", state, msg, msg, single, single, state, failedAttempt, now(), j.ID)
	}
}
func (a *App) jobAlive(ctx context.Context, jid string) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	var state string
	var authorized bool
	e := a.DB.QueryRowContext(ctx, "SELECT j.state,CASE WHEN j.origin='manual' AND j.actor!='' THEN EXISTS(SELECT 1 FROM users u WHERE u.id=j.actor AND u.admin=1 AND u.disabled=0) ELSE 1 END FROM jobs j WHERE j.id=?", jid).Scan(&state, &authorized)
	if e != nil {
		return e
	}
	if state != "running" {
		return errJobStopped
	}
	if !authorized {
		_, _ = a.DB.ExecContext(ctx, "UPDATE jobs SET state='paused_auth',error='Initiating administrator no longer authorized',updated=? WHERE id=?", now(), jid)
		return errJobStopped
	}
	var paused string
	_ = a.DB.QueryRowContext(ctx, "SELECT value FROM settings WHERE key='pause_jobs'").Scan(&paused)
	if paused == "1" {
		_, _ = a.DB.ExecContext(ctx, "UPDATE jobs SET state='queued',updated=? WHERE id=? AND state='running'", now(), jid)
		return errJobStopped
	}
	return nil
}
func (a *App) runJob(ctx context.Context, j Job) error {
	switch j.Type {
	case "scan", "reconcile", "incremental_scan":
		return a.scanJob(ctx, j)
	case "filename_reparse":
		return a.reparseJob(ctx, j)
	case "metadata_remerge":
		return a.remergeJob(ctx, j)
	case "stability", "stability_bulk":
		return a.stabilizeJob(ctx, j)
	case "incremental", "index_bulk_item":
		al, e := a.AuthorizedAlias(ctx, User{Admin: true}, j.WorkID)
		if e != nil {
			return e
		}
		return a.ensureIndex(ctx, al)
	case "index_bulk", "thumbnail", "crawl_bulk":
		return a.bulkJob(ctx, j)
	case "auto_crawl", "crawl":
		return a.crawler.RunJob(ctx, j)
	default:
		return errors.New("unsupported job type")
	}
}
func (a *App) jobs(w http.ResponseWriter, r *http.Request, u User) {
	if r.Method == "GET" {
		rows, e := a.DB.QueryContext(r.Context(), "SELECT "+jobColumns+" FROM jobs ORDER BY created DESC LIMIT 250")
		if e != nil {
			apiError(w, 503, "database", "Cannot list jobs")
			return
		}
		defer rows.Close()
		out := []Job{}
		for rows.Next() {
			j, e := scanJob(rows)
			if e == nil {
				j.Payload = ""
				j.Checkpoint = ""
				out = append(out, j)
			}
		}
		jsonResponse(w, 200, out)
		return
	}
	var in struct {
		Type     string   `json:"type"`
		RootID   string   `json:"root_id"`
		WorkIDs  []string `json:"work_ids"`
		Query    string   `json:"query"`
		Provider string   `json:"provider"`
		Preview  bool     `json:"preview"`
	}
	if decodeJSON(w, r, &in) != nil {
		apiError(w, 400, "invalid_request", "Invalid job request")
		return
	}
	switch in.Type {
	case "scan", "reconcile", "index_bulk", "thumbnail", "crawl_bulk":
	default:
		apiError(w, 400, "invalid_type", "Unsupported job type")
		return
	}
	if len(in.WorkIDs) > 1000 || len(in.Query) > 4096 {
		apiError(w, 400, "too_many_targets", "Too many targets")
		return
	}
	count, e := a.targetCount(r.Context(), u, in.RootID, in.WorkIDs, in.Query)
	if e != nil {
		apiError(w, 400, "invalid_query", e.Error())
		return
	}
	if in.Preview {
		jsonResponse(w, 200, map[string]int{"count": count})
		return
	}
	if in.RootID != "" {
		if _, e = a.getRoot(r.Context(), in.RootID); e != nil {
			apiError(w, 404, "not_found", "Root not found")
			return
		}
	}
	if (in.Type == "scan" || in.Type == "reconcile") && in.RootID == "" {
		rows, e := a.DB.QueryContext(r.Context(), "SELECT id FROM roots ORDER BY id")
		if e != nil {
			apiError(w, 503, "database", "Cannot enumerate root records")
			return
		}
		ids := []string{}
		for rows.Next() {
			var id string
			_ = rows.Scan(&id)
			ids = append(ids, id)
		}
		rows.Close()
		tx, e := a.DB.BeginTx(r.Context(), nil)
		if e != nil {
			apiError(w, 503, "database", "Cannot schedule scans")
			return
		}
		defer tx.Rollback()
		jobs := []string{}
		for _, rid := range ids {
			jid, err := enqueueTx(tx, in.Type, rid, "", u.ID, "manual", "", nil)
			if err != nil {
				apiError(w, 503, "database", "Cannot schedule scans")
				return
			}
			jobs = append(jobs, jid)
		}
		if e = tx.Commit(); e != nil {
			apiError(w, 503, "database", "Cannot schedule scans")
			return
		}
		first := ""
		if len(jobs) > 0 {
			first = jobs[0]
		}
		a.signal()
		jsonResponse(w, 202, map[string]any{"id": first, "ids": jobs})
		return
	}
	tx, e := a.DB.BeginTx(r.Context(), nil)
	if e != nil {
		apiError(w, 503, "database", "Cannot schedule job")
		return
	}
	defer tx.Rollback()
	jid, e := enqueueTx(tx, in.Type, in.RootID, "", u.ID, "manual", "", map[string]any{"work_ids": in.WorkIDs, "query": in.Query, "provider": in.Provider})
	if e == nil {
		_, e = tx.Exec("UPDATE jobs SET total=?,provider=? WHERE id=?", count, in.Provider, jid)
	}
	if e == nil {
		e = tx.Commit()
	}
	if e != nil {
		apiError(w, 503, "database", "Cannot schedule job")
		return
	}
	a.signal()
	jsonResponse(w, 202, map[string]string{"id": jid})
}
func (a *App) jobAction(w http.ResponseWriter, r *http.Request, u User) {
	var in struct {
		Action string `json:"action"`
	}
	if decodeJSON(w, r, &in) != nil {
		apiError(w, 400, "invalid_request", "Invalid action")
		return
	}
	jid := r.PathValue("id")
	j, e := scanJob(a.DB.QueryRowContext(r.Context(), "SELECT "+jobColumns+" FROM jobs WHERE id=?", jid))
	if e != nil {
		apiError(w, 404, "not_found", "Job not found")
		return
	}
	state := ""
	switch in.Action {
	case "pause":
		state = "paused"
	case "resume", "retry":
		state = "queued"
	case "cancel":
		state = "cancelled"
	default:
		apiError(w, 400, "invalid_action", "Choose pause, resume, retry or cancel")
		return
	}
	if (j.State == "completed" && in.Action != "retry") || (in.Action == "retry" && (j.State == "running" || j.State == "queued")) {
		apiError(w, 409, "invalid_transition", "Job state does not permit this action")
		return
	}
	payload := map[string]any{}
	if j.Payload != "null" {
		if e = json.Unmarshal([]byte(j.Payload), &payload); e != nil {
			apiError(w, 422, "invalid_job", "Stored job payload is invalid")
			return
		}
	}
	tx, e := a.DB.BeginTx(r.Context(), nil)
	if e != nil {
		apiError(w, 503, "database", "Cannot update job")
		return
	}
	defer tx.Rollback()
	actor := j.Actor
	checkpoint := j.Checkpoint
	done, total := j.Done, j.Total
	if in.Action == "retry" {
		var failures int
		if e = tx.QueryRow("SELECT count(*) FROM job_failures WHERE job_id=?", jid).Scan(&failures); e == nil && failures > 0 {
			payload["retry_failures"] = true
			checkpoint = "{}"
			done = 0
			total = failures
		} else if e == nil && j.State == "completed" {
			apiError(w, 409, "nothing_to_retry", "No failed work items remain")
			return
		}
		if e == nil {
			_, e = tx.Exec("UPDATE scan_dirs SET done=0 WHERE job_id=? AND done=2", jid)
		}
		if e == nil && j.WorkID != "" && (j.Type == "incremental" || j.Type == "index_bulk_item") {
			_, e = tx.Exec("UPDATE works SET index_status='stale',error_code='',error_message='' WHERE id=? AND index_status='error'", j.WorkID)
		}
	}
	if (in.Action == "resume" || in.Action == "retry") && j.Origin == "manual" {
		actor = u.ID
	}
	encoded, _ := json.Marshal(payload)
	if e == nil {
		_, e = tx.Exec("UPDATE jobs SET state=?,error='',updated=?,not_before=0,actor=?,payload=?,checkpoint=?,done=?,total=? WHERE id=?", state, now(), actor, string(encoded), checkpoint, done, total, jid)
	}
	if e == nil {
		e = audit(tx, u.ID, "job."+in.Action, j.RootID, map[string]string{"job": jid, "previous_actor": j.Actor})
	}
	if e == nil {
		e = tx.Commit()
	}
	if e != nil {
		apiError(w, 503, "database", "Cannot update job")
		return
	}
	a.signal()
	jsonResponse(w, 200, map[string]bool{"ok": true})
}
func (a *App) pauseJobs(w http.ResponseWriter, r *http.Request, u User) {
	var in struct {
		Paused bool `json:"paused"`
	}
	if decodeJSON(w, r, &in) != nil {
		apiError(w, 400, "invalid_request", "Invalid pause request")
		return
	}
	value := "0"
	if in.Paused {
		value = "1"
	}
	_, e := a.DB.ExecContext(r.Context(), "INSERT INTO settings(key,value) VALUES('pause_jobs',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", value)
	if e != nil {
		apiError(w, 503, "database", "Cannot change job policy")
		return
	}
	a.signal()
	jsonResponse(w, 200, map[string]bool{"ok": true})
}
func (a *App) bulkJob(ctx context.Context, j Job) error {
	var p struct {
		WorkIDs       []string `json:"work_ids"`
		Query         string   `json:"query"`
		Provider      string   `json:"provider"`
		RetryFailures bool     `json:"retry_failures"`
	}
	if e := json.Unmarshal([]byte(j.Payload), &p); e != nil {
		return e
	}
	var cp struct {
		After string `json:"after"`
	}
	_ = json.Unmarshal([]byte(j.Checkpoint), &cp)
	u := User{ID: j.Actor, scope: j.RootID}
	if e := a.DB.QueryRowContext(ctx, "SELECT admin FROM users WHERE id=? AND disabled=0", u.ID).Scan(&u.Admin); e != nil {
		return errors.New("initiating account no longer authorized")
	}
	for {
		if e := a.jobAlive(ctx, j.ID); e != nil {
			return e
		}
		var ids []string
		var e error
		if p.RetryFailures {
			ids, e = a.failedJobIDs(ctx, j.ID, cp.After)
		} else {
			ids, e = a.targetIDs(ctx, u, j.RootID, p.WorkIDs, p.Query, cp.After, j.Created, 64)
		}
		if e != nil {
			return e
		}
		if len(ids) == 0 {
			return nil
		}
		for _, wid := range ids {
			if e = a.jobAlive(ctx, j.ID); e != nil {
				return e
			}
			al, opErr := a.AuthorizedAlias(ctx, u, wid)
			if opErr == nil {
				switch j.Type {
				case "index_bulk":
					_, opErr = a.DB.ExecContext(ctx, "UPDATE works SET index_status='stale',error_code='',error_message='' WHERE id=? AND index_status='error'", wid)
					if opErr == nil {
						opErr = a.ensureIndex(ctx, al)
					}
				case "thumbnail":
					_, opErr = a.DB.ExecContext(ctx, "UPDATE thumbnails SET status='stale' WHERE work_id=? AND status='failed'", wid)
					if opErr == nil {
						_, _, opErr = a.thumbnail(ctx, u, al)
					}
				case "crawl_bulk":
					single := j
					single.WorkID = wid
					single.Provider = p.Provider
					single.Type = "crawl"
					opErr = a.crawler.RunJob(ctx, single)
				}
			}
			if errors.Is(opErr, errStabilizing) {
				_, _ = a.DB.ExecContext(ctx, "UPDATE jobs SET state='queued',not_before=? WHERE id=?", now()+1000, j.ID)
				return errJobDeferred
			}
			cp.After = wid
			b, _ := json.Marshal(cp)
			failed := 0
			if opErr != nil {
				failed = 1
			}
			tx, err := a.DB.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			e = recordFailure(tx, j.ID, wid, opErr)
			if e == nil {
				_, e = tx.Exec("UPDATE jobs SET done=done+1,failed=failed+?,checkpoint=?,updated=? WHERE id=? AND state='running'", failed, string(b), now(), j.ID)
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
}
func (a *App) retryWork(w http.ResponseWriter, r *http.Request, u User) {
	wid := r.PathValue("id")
	al, e := a.AuthorizedAlias(r.Context(), u, wid)
	if e != nil {
		apiError(w, 404, "not_found", "Work unavailable")
		return
	}
	_, e = a.DB.ExecContext(r.Context(), "UPDATE works SET index_status='stale',error_code='',error_message='' WHERE id=?", wid)
	if e != nil {
		apiError(w, 503, "database", "Cannot retry work")
		return
	}
	_, _ = a.DB.ExecContext(r.Context(), "UPDATE thumbnails SET status='stale' WHERE work_id=? AND status='failed'", wid)
	e = a.ensureIndex(r.Context(), al)
	if e != nil {
		apiError(w, 422, "index_failed", "Indexing failed; inspect work status")
		return
	}
	jsonResponse(w, 200, map[string]bool{"ok": true})
}
