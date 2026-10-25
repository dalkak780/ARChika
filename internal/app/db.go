package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS settings(key TEXT PRIMARY KEY,value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS users(id TEXT PRIMARY KEY,name TEXT NOT NULL UNIQUE,password TEXT NOT NULL,admin INTEGER NOT NULL DEFAULT 0,disabled INTEGER NOT NULL DEFAULT 0,preferences TEXT NOT NULL DEFAULT '{}',must_change_password INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS sessions(token TEXT PRIMARY KEY,user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,csrf TEXT NOT NULL,expires INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS sessions_user ON sessions(user_id);
CREATE TABLE IF NOT EXISTS roots(id TEXT PRIMARY KEY,bootstrap_key TEXT UNIQUE,name TEXT NOT NULL,path TEXT NOT NULL UNIQUE,watch INTEGER NOT NULL DEFAULT 0,auto_crawl INTEGER NOT NULL DEFAULT 0,providers TEXT NOT NULL DEFAULT '[]',quiet_seconds INTEGER NOT NULL DEFAULT 3,parser_enabled INTEGER NOT NULL DEFAULT 1,parser_regex TEXT NOT NULL DEFAULT '',status TEXT NOT NULL DEFAULT 'online',pending_path TEXT NOT NULL DEFAULT '',warning TEXT NOT NULL DEFAULT '',checked_at INTEGER NOT NULL DEFAULT 0,trust_identity INTEGER NOT NULL DEFAULT 1);
CREATE TABLE IF NOT EXISTS grants(user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,root_id TEXT NOT NULL REFERENCES roots(id) ON DELETE CASCADE,PRIMARY KEY(user_id,root_id));
 CREATE TABLE IF NOT EXISTS works(id TEXT PRIMARY KEY,physical_key TEXT,kind TEXT NOT NULL,size INTEGER NOT NULL,mtime INTEGER NOT NULL,modified_at INTEGER NOT NULL DEFAULT 0,version INTEGER NOT NULL DEFAULT 1,registered_at INTEGER NOT NULL,index_status TEXT NOT NULL DEFAULT 'pending_stability',error_code TEXT NOT NULL DEFAULT '',error_message TEXT NOT NULL DEFAULT '',error_at INTEGER NOT NULL DEFAULT 0,cover TEXT NOT NULL DEFAULT '',cover_unresolved INTEGER NOT NULL DEFAULT 0,capabilities TEXT NOT NULL DEFAULT '{}');
CREATE INDEX IF NOT EXISTS works_physical ON works(physical_key);
CREATE INDEX IF NOT EXISTS works_recent ON works(registered_at DESC,id DESC);
 CREATE INDEX IF NOT EXISTS works_modified ON works(COALESCE(NULLIF(modified_at,0),mtime) DESC,id DESC);
CREATE TABLE IF NOT EXISTS aliases(id INTEGER PRIMARY KEY,work_id TEXT NOT NULL REFERENCES works(id) ON DELETE CASCADE,root_id TEXT NOT NULL REFERENCES roots(id) ON DELETE CASCADE,path TEXT NOT NULL,original_name TEXT NOT NULL,title TEXT NOT NULL,title_sort TEXT NOT NULL,parent TEXT NOT NULL,status TEXT NOT NULL DEFAULT 'online',physical_key TEXT,size INTEGER NOT NULL,mtime INTEGER NOT NULL,seen INTEGER NOT NULL DEFAULT 0,filename_tags TEXT NOT NULL DEFAULT '[]',parser_version TEXT NOT NULL DEFAULT '',UNIQUE(root_id,path));
CREATE INDEX IF NOT EXISTS aliases_work ON aliases(work_id,root_id,status);
CREATE INDEX IF NOT EXISTS aliases_title ON aliases(title_sort,work_id);
CREATE INDEX IF NOT EXISTS aliases_parent ON aliases(root_id,parent);
CREATE INDEX IF NOT EXISTS aliases_root ON aliases(root_id,status,work_id);
CREATE VIRTUAL TABLE IF NOT EXISTS alias_words USING fts5(title,path,tags,tokenize='unicode61',prefix='1 2 3');
CREATE VIRTUAL TABLE IF NOT EXISTS alias_substrings USING fts5(title,path,tags,tokenize='trigram');
CREATE TRIGGER IF NOT EXISTS alias_insert AFTER INSERT ON aliases BEGIN
 INSERT INTO alias_words(rowid,title,path,tags) VALUES(new.id,archika_search(new.title),archika_search(new.parent),new.filename_tags);
 INSERT INTO alias_substrings(rowid,title,path,tags) VALUES(new.id,archika_search(new.title),archika_search(new.parent),new.filename_tags);
END;
CREATE TRIGGER IF NOT EXISTS alias_delete AFTER DELETE ON aliases BEGIN
 DELETE FROM alias_words WHERE rowid=old.id;DELETE FROM alias_substrings WHERE rowid=old.id;
END;
 CREATE TRIGGER IF NOT EXISTS alias_update AFTER UPDATE OF title,parent,filename_tags ON aliases WHEN old.title IS NOT new.title OR old.parent IS NOT new.parent OR old.filename_tags IS NOT new.filename_tags BEGIN
 DELETE FROM alias_words WHERE rowid=old.id;DELETE FROM alias_substrings WHERE rowid=old.id;
 INSERT INTO alias_words(rowid,title,path,tags) VALUES(new.id,archika_search(new.title),archika_search(new.parent),new.filename_tags);
 INSERT INTO alias_substrings(rowid,title,path,tags) VALUES(new.id,archika_search(new.title),archika_search(new.parent),new.filename_tags);
END;
CREATE TABLE IF NOT EXISTS pages(work_id TEXT NOT NULL REFERENCES works(id) ON DELETE CASCADE,version INTEGER NOT NULL,ordinal INTEGER NOT NULL,entry TEXT NOT NULL,entry_id TEXT NOT NULL,size INTEGER NOT NULL,mime TEXT NOT NULL,locator INTEGER NOT NULL DEFAULT 0,width INTEGER NOT NULL DEFAULT 0,height INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(work_id,version,ordinal),UNIQUE(work_id,version,entry_id));
CREATE TABLE IF NOT EXISTS thumbnails(work_id TEXT PRIMARY KEY REFERENCES works(id) ON DELETE CASCADE,key TEXT NOT NULL,path TEXT NOT NULL DEFAULT '',status TEXT NOT NULL DEFAULT 'pending',error TEXT NOT NULL DEFAULT '',retry_at INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS saved_searches(id TEXT PRIMARY KEY,user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,name TEXT NOT NULL,query TEXT NOT NULL,sort TEXT NOT NULL DEFAULT 'recent');
CREATE TABLE IF NOT EXISTS jobs(id TEXT PRIMARY KEY,type TEXT NOT NULL,root_id TEXT NOT NULL DEFAULT '',work_id TEXT NOT NULL DEFAULT '',provider TEXT NOT NULL DEFAULT '',actor TEXT NOT NULL DEFAULT '',state TEXT NOT NULL DEFAULT 'queued',origin TEXT NOT NULL DEFAULT 'manual',payload TEXT NOT NULL DEFAULT '{}',checkpoint TEXT NOT NULL DEFAULT '{}',done INTEGER NOT NULL DEFAULT 0,failed INTEGER NOT NULL DEFAULT 0,total INTEGER NOT NULL DEFAULT 0,error TEXT NOT NULL DEFAULT '',dedup TEXT UNIQUE,created INTEGER NOT NULL,updated INTEGER NOT NULL,not_before INTEGER NOT NULL DEFAULT 0,attempts INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS jobs_queue ON jobs(state,not_before,created);
CREATE TABLE IF NOT EXISTS scan_dirs(job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,path TEXT NOT NULL,after_name TEXT NOT NULL DEFAULT '',done INTEGER NOT NULL DEFAULT 0,physical_key TEXT NOT NULL DEFAULT '',PRIMARY KEY(job_id,path));
CREATE TABLE IF NOT EXISTS scan_seen(job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,physical_key TEXT NOT NULL,path TEXT NOT NULL,PRIMARY KEY(job_id,physical_key));
CREATE TABLE IF NOT EXISTS watch_events(root_id TEXT NOT NULL,path TEXT NOT NULL,kind TEXT NOT NULL DEFAULT 'change',observed INTEGER NOT NULL,PRIMARY KEY(root_id,path));
CREATE TABLE IF NOT EXISTS audit(id INTEGER PRIMARY KEY,actor TEXT NOT NULL,action TEXT NOT NULL,root_id TEXT NOT NULL DEFAULT '',detail TEXT NOT NULL,created INTEGER NOT NULL);
 PRAGMA user_version=4;
`

func OpenDB(c Config) (*sql.DB, error) {
	if e := os.MkdirAll(c.DataDir, 0700); e != nil {
		return nil, e
	}
	created, createErr := os.OpenFile(filepath.Join(c.DataDir, "archika.sqlite"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if createErr == nil {
		if e := created.Close(); e != nil {
			return nil, e
		}
	} else if !errors.Is(createErr, os.ErrExist) {
		return nil, createErr
	}
	db, e := sql.Open("sqlite", filepath.Join(c.DataDir, "archika.sqlite")+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(10000)")
	if e != nil {
		return nil, e
	}
	success := false
	defer func() {
		if !success {
			db.Close()
		}
	}()
	db.SetMaxOpenConns(1)
	var version int
	if e = db.QueryRow("PRAGMA user_version").Scan(&version); e != nil {
		return nil, e
	}
	if version > 4 {
		return nil, fmt.Errorf("database schema %d is newer than supported", version)
	}
	if version == 1 {
		tx, err := db.Begin()
		if err != nil {
			return nil, err
		}
		_, err = tx.Exec("ALTER TABLE works ADD COLUMN modified_at INTEGER NOT NULL DEFAULT 0; DROP TRIGGER IF EXISTS alias_update; DROP TRIGGER IF EXISTS alias_tags_update; PRAGMA user_version=2")
		if err == nil {
			err = tx.Commit()
		} else {
			tx.Rollback()
		}
		if err != nil {
			return nil, err
		}
		version = 2
	}
	if version == 2 {
		tx, err := db.Begin()
		if err != nil {
			return nil, err
		}
		_, err = tx.Exec("ALTER TABLE users ADD COLUMN must_change_password INTEGER NOT NULL DEFAULT 0; PRAGMA user_version=3")
		if err == nil {
			err = tx.Commit()
		} else {
			tx.Rollback()
		}
		if err != nil {
			return nil, err
		}
		version = 3
	}
	if version == 3 {
		tx, err := db.Begin()
		if err != nil {
			return nil, err
		}
		_, err = tx.Exec("UPDATE works SET index_status='stale',error_code='',error_message='' WHERE index_status='error' AND error_code IN ('corrupt','encrypted_or_password_required','unknown'); PRAGMA user_version=4")
		if err == nil {
			err = tx.Commit()
		} else {
			tx.Rollback()
		}
		if err != nil {
			return nil, err
		}
	}
	if _, e = db.Exec(schema); e != nil {
		return nil, e
	}
	if _, e = db.Exec(scanSchema); e != nil {
		return nil, e
	}
	if _, e = db.Exec(searchSchema); e != nil {
		return nil, e
	}
	if _, e = db.Exec(favoriteSchema); e != nil {
		return nil, e
	}
	if _, e = db.Exec(failureSchema); e != nil {
		return nil, e
	}
	tx, e := db.Begin()
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	var initialized string
	e = tx.QueryRow("SELECT value FROM settings WHERE key='initialized'").Scan(&initialized)
	if e == sql.ErrNoRows {
		for _, r := range c.Roots {
			p, e := c.RootPath(r.Path)
			if e != nil {
				return nil, e
			}
			q := r.QuietSeconds
			if q < 1 {
				q = c.QuietSeconds
			}
			if r.Key == "" || r.Name == "" {
				return nil, fmt.Errorf("bootstrap roots need unique key and name")
			}
			if _, e = tx.Exec("INSERT INTO roots(id,bootstrap_key,name,path,watch,auto_crawl,quiet_seconds) VALUES(?,?,?,?,?,?,?)", id(), r.Key, r.Name, p, r.Watch, r.AutoCrawl, q); e != nil {
				return nil, e
			}
		}
		_, e = tx.Exec("INSERT INTO settings VALUES('initialized','1'),('catalog_generation','1')")
		if e != nil {
			return nil, e
		}
	} else if e != nil {
		return nil, e
	}
	if _, e = tx.Exec("UPDATE jobs SET state=CASE WHEN type IN ('incremental','incremental_scan','auto_crawl','stability') THEN 'queued' ELSE 'paused_restart' END,updated=? WHERE state IN ('running','queued')", now()); e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	success = true
	return db, nil
}
func bump(tx *sql.Tx) error {
	_, e := tx.Exec("UPDATE settings SET value=lower(hex(randomblob(16))) WHERE key='catalog_generation'")
	return e
}
func audit(tx *sql.Tx, actor, action, root string, detail any) error {
	b, e := json.Marshal(detail)
	if e != nil {
		return e
	}
	_, e = tx.Exec("INSERT INTO audit(actor,action,root_id,detail,created) VALUES(?,?,?,?,?)", actor, action, root, string(b), now())
	return e
}
func (a *App) Generation(ctx context.Context) string {
	var g string
	_ = a.DB.QueryRowContext(ctx, "SELECT value FROM settings WHERE key='catalog_generation'").Scan(&g)
	return g
}
