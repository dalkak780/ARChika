package app

const scanSchema = `
CREATE TABLE IF NOT EXISTS scan_entries(job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,parent TEXT NOT NULL,name TEXT NOT NULL,done INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(job_id,parent,name));
CREATE INDEX IF NOT EXISTS scan_entries_pending ON scan_entries(job_id,done,parent,name);
CREATE TABLE IF NOT EXISTS watch_dirs(root_id TEXT NOT NULL REFERENCES roots(id) ON DELETE CASCADE,path TEXT NOT NULL,PRIMARY KEY(root_id,path));
CREATE TABLE IF NOT EXISTS page_observations(work_id TEXT NOT NULL REFERENCES works(id) ON DELETE CASCADE,version INTEGER NOT NULL,entry_id TEXT NOT NULL,size INTEGER NOT NULL,mtime INTEGER NOT NULL,PRIMARY KEY(work_id,version,entry_id));
CREATE TABLE IF NOT EXISTS move_candidates(id TEXT PRIMARY KEY,from_work TEXT NOT NULL,to_work TEXT NOT NULL,from_root TEXT NOT NULL,to_root TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'pending',created INTEGER NOT NULL);
`
