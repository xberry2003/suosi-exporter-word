package tbinventory

import (
	"database/sql"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type DB struct{ SQL *sql.DB }

func OpenDB(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	s, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	d := &DB{s}
	if err = d.init(); err != nil {
		s.Close()
		return nil, err
	}
	return d, nil
}
func (d *DB) Close() error { return d.SQL.Close() }
func (d *DB) init() error {
	_, err := d.SQL.Exec(`PRAGMA busy_timeout=5000; PRAGMA journal_mode=WAL;
CREATE TABLE IF NOT EXISTS tb_crawl_runs (run_id TEXT PRIMARY KEY,started_at TEXT NOT NULL,finished_at TEXT,status TEXT NOT NULL,org_id TEXT NOT NULL,include_archived INTEGER NOT NULL,project_count INTEGER DEFAULT 0,folder_count INTEGER DEFAULT 0,file_count INTEGER DEFAULT 0,total_size_bytes INTEGER DEFAULT 0,error_count INTEGER DEFAULT 0,config_json TEXT);
CREATE TABLE IF NOT EXISTS tb_projects (project_id TEXT PRIMARY KEY,project_name TEXT,project_url TEXT,is_archived INTEGER DEFAULT 0,crawl_status TEXT,last_crawled_at TEXT,last_error TEXT);
CREATE TABLE IF NOT EXISTS tb_folders (project_id TEXT NOT NULL,collection_id TEXT NOT NULL,parent_id TEXT,title TEXT,prefix_path TEXT,is_archived INTEGER DEFAULT 0,created_at TEXT,updated_at TEXT,crawl_status TEXT,last_crawled_at TEXT,PRIMARY KEY(project_id,collection_id));
CREATE TABLE IF NOT EXISTS tb_works (project_id TEXT NOT NULL,work_id TEXT NOT NULL,parent_id TEXT,prefix_path TEXT,file_name TEXT,file_extension TEXT,mime_type TEXT,file_size_bytes INTEGER,is_archived INTEGER DEFAULT 0,creator_id TEXT,created_at TEXT,updated_at TEXT,resource_id TEXT NOT NULL,source_page_url TEXT,crawl_status TEXT,last_crawled_at TEXT,last_error TEXT,PRIMARY KEY(project_id,work_id));
CREATE TABLE IF NOT EXISTS tb_crawl_errors (run_id TEXT NOT NULL,project_id TEXT NOT NULL,parent_id TEXT,resource_type TEXT,resource_id TEXT,operation TEXT,http_status INTEGER,error_message TEXT,retry_count INTEGER,created_at TEXT,PRIMARY KEY(run_id,project_id,resource_type,resource_id,operation));`)
	return err
}
func nowText() string { return time.Now().UTC().Format(time.RFC3339) }
func extension(n string) string {
	i := strings.LastIndex(n, ".")
	if i < 0 || i == len(n)-1 {
		return ""
	}
	return strings.ToLower(n[i+1:])
}
func (d *DB) UpsertProject(p Project, status, lastErr string) error {
	_, e := d.SQL.Exec(`INSERT INTO tb_projects VALUES(?,?,?,?,?,?,?) ON CONFLICT(project_id) DO UPDATE SET project_name=excluded.project_name,project_url=excluded.project_url,is_archived=excluded.is_archived,crawl_status=excluded.crawl_status,last_crawled_at=excluded.last_crawled_at,last_error=excluded.last_error`, p.ID, p.Name, p.URL, p.Archived, status, nowText(), lastErr)
	return e
}
func (d *DB) UpsertFolder(pid string, f Collection) error {
	_, e := d.SQL.Exec(`INSERT INTO tb_folders VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(project_id,collection_id) DO UPDATE SET parent_id=excluded.parent_id,title=excluded.title,prefix_path=excluded.prefix_path,is_archived=excluded.is_archived,created_at=excluded.created_at,updated_at=excluded.updated_at,crawl_status='success',last_crawled_at=excluded.last_crawled_at`, pid, f.ID, f.ParentID, f.Title, f.PrefixPath, f.Archived, f.Created, f.Updated, "success", nowText())
	return e
}
func (d *DB) UpsertWork(pid string, w Work) error {
	var size any
	if w.FileSize != nil {
		size = *w.FileSize
	}
	_, e := d.SQL.Exec(`INSERT INTO tb_works VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(project_id,work_id) DO UPDATE SET parent_id=excluded.parent_id,prefix_path=excluded.prefix_path,file_name=excluded.file_name,file_extension=excluded.file_extension,mime_type=excluded.mime_type,file_size_bytes=excluded.file_size_bytes,is_archived=excluded.is_archived,creator_id=excluded.creator_id,created_at=excluded.created_at,updated_at=excluded.updated_at,resource_id=excluded.resource_id,source_page_url=excluded.source_page_url,crawl_status='success',last_crawled_at=excluded.last_crawled_at,last_error=NULL`, pid, w.ID, w.ParentID, w.PrefixPath, w.FileName, extension(w.FileName), w.MIMEType, size, w.Archived, w.CreatorID, w.Created, w.Updated, "work:"+w.ID, w.SourcePageURL, "success", nowText(), nil)
	return e
}
func (d *DB) AddError(runID, pid, parent, typ, rid, op string, status int, msg string, retries int) error {
	_, e := d.SQL.Exec(`INSERT INTO tb_crawl_errors VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(run_id,project_id,resource_type,resource_id,operation) DO UPDATE SET http_status=excluded.http_status,error_message=excluded.error_message,retry_count=excluded.retry_count,created_at=excluded.created_at`, runID, pid, parent, typ, rid, op, status, msg, retries, nowText())
	return e
}
