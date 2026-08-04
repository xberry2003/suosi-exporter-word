package tbinventory

import (
	"bufio"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
)

type ImportResource struct {
	SourceSystem     string         `json:"sourceSystem"`
	ResourceType     string         `json:"resourceType"`
	SourceKey        string         `json:"sourceKey"`
	SourceProjectKey string         `json:"sourceProjectKey"`
	TargetLibraryID  any            `json:"targetLibraryId"`
	TargetDocID      any            `json:"targetDocId"`
	Status           string         `json:"status"`
	OriginalURL      string         `json:"originalUrl"`
	Metadata         map[string]any `json:"metadata"`
}

func ExportAll(db *DB, out string) error {
	if err := os.MkdirAll(out, 0755); err != nil {
		return err
	}
	if err := exportJSONL(db, out); err != nil {
		return err
	}
	if err := exportProjects(db, out); err != nil {
		return err
	}
	if err := exportFolders(db, out); err != nil {
		return err
	}
	if err := exportErrors(db, out); err != nil {
		return err
	}
	s, e := BuildSummary(db)
	if e != nil {
		return e
	}
	if e = WriteSummaryJSON(filepath.Join(out, "tb_summary.json"), s); e != nil {
		return e
	}
	return writeFile(filepath.Join(out, "tb_summary.md"), []byte(SummaryMarkdown(s)))
}
func exportJSONL(db *DB, out string) error {
	rows, e := db.SQL.Query(`SELECT project_id,work_id,parent_id,prefix_path,file_name,mime_type,file_size_bytes,is_archived,created_at,updated_at,resource_id,source_page_url FROM tb_works ORDER BY project_id,work_id`)
	if e != nil {
		return e
	}
	defer rows.Close()
	files, e := os.Create(filepath.Join(out, "tb_works.jsonl"))
	if e != nil {
		return e
	}
	defer files.Close()
	imports, e := os.Create(filepath.Join(out, "import_resources.jsonl"))
	if e != nil {
		return e
	}
	defer imports.Close()
	iw := bufio.NewWriter(imports)
	for rows.Next() {
		var pid, wid, parent, path, name, mime, created, updated, res, url string
		var size sql.NullInt64
		var arch bool
		if e = rows.Scan(&pid, &wid, &parent, &path, &name, &mime, &size, &arch, &created, &updated, &res, &url); e != nil {
			return e
		}
		obj := map[string]any{"projectId": pid, "workId": wid, "parentId": parent, "prefixPath": path, "fileName": name, "mimeType": mime, "fileSizeBytes": nil, "isArchived": arch, "createdAt": created, "updatedAt": updated, "resourceId": res, "sourcePageUrl": url}
		if size.Valid {
			obj["fileSizeBytes"] = size.Int64
		}
		enc := json.NewEncoder(files)
		enc.SetEscapeHTML(false)
		if e = enc.Encode(obj); e != nil {
			return e
		}
		ir := ImportResource{"teambition", "work", wid, pid, nil, nil, "pending", url, map[string]any{"parentId": parent, "prefixPath": path, "fileName": name, "mimeType": mime, "fileSizeBytes": obj["fileSizeBytes"], "resourceId": res, "isArchived": arch, "createdAt": created, "updatedAt": updated}}
		enc2 := json.NewEncoder(iw)
		enc2.SetEscapeHTML(false)
		if e = enc2.Encode(ir); e != nil {
			return e
		}
	}
	if e = rows.Err(); e != nil {
		return e
	}
	if e = iw.Flush(); e != nil {
		return e
	}
	return exportCSV(db, out)
}
func exportCSV(db *DB, out string) error {
	rows, e := db.SQL.Query(`SELECT project_id,work_id,parent_id,prefix_path,file_name,file_extension,mime_type,file_size_bytes,is_archived,creator_id,created_at,updated_at,resource_id,source_page_url,crawl_status,last_error FROM tb_works ORDER BY project_id,work_id`)
	if e != nil {
		return e
	}
	defer rows.Close()
	f, e := os.Create(filepath.Join(out, "tb_works.csv"))
	if e != nil {
		return e
	}
	defer f.Close()
	c := csv.NewWriter(f)
	_ = c.Write([]string{"project_id", "work_id", "parent_id", "prefix_path", "file_name", "file_extension", "mime_type", "file_size_bytes", "is_archived", "creator_id", "created_at", "updated_at", "resource_id", "source_page_url", "crawl_status", "last_error"})
	for rows.Next() {
		var pid, wid, parent, path, name, ext, mime, creator, created, updated, res, url, status string
		var last sql.NullString
		var size sql.NullInt64
		var arch bool
		if e = rows.Scan(&pid, &wid, &parent, &path, &name, &ext, &mime, &size, &arch, &creator, &created, &updated, &res, &url, &status, &last); e != nil {
			return e
		}
		sz := ""
		if size.Valid {
			sz = strconv.FormatInt(size.Int64, 10)
		}
		_ = c.Write([]string{pid, wid, parent, path, name, ext, mime, sz, strconv.FormatBool(arch), creator, created, updated, res, url, status, last.String})
	}
	c.Flush()
	return c.Error()
}
func writeFile(path string, b []byte) error { return os.WriteFile(path, b, 0644) }

func exportProjects(db *DB, out string) error {
	rows, err := db.SQL.Query(`SELECT project_id,project_name,project_url,is_archived,crawl_status,last_crawled_at,last_error FROM tb_projects ORDER BY project_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	f, err := os.Create(filepath.Join(out, "tb_projects.jsonl"))
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	for rows.Next() {
		var id, name, url, status, last, problem string
		var archived bool
		if err = rows.Scan(&id, &name, &url, &archived, &status, &last, &problem); err != nil {
			return err
		}
		if err = enc.Encode(map[string]any{"projectId": id, "projectName": name, "projectUrl": url, "isArchived": archived, "crawlStatus": status, "lastCrawledAt": last, "lastError": problem}); err != nil {
			return err
		}
	}
	return rows.Err()
}
func exportFolders(db *DB, out string) error {
	rows, err := db.SQL.Query(`SELECT project_id,collection_id,parent_id,title,prefix_path,is_archived,created_at,updated_at,crawl_status,last_crawled_at FROM tb_folders ORDER BY project_id,collection_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	f, err := os.Create(filepath.Join(out, "tb_folders.jsonl"))
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	for rows.Next() {
		var p, id, parent, title, path, created, updated, status, last string
		var archived bool
		if err = rows.Scan(&p, &id, &parent, &title, &path, &archived, &created, &updated, &status, &last); err != nil {
			return err
		}
		if err = enc.Encode(map[string]any{"projectId": p, "collectionId": id, "parentId": parent, "title": title, "prefixPath": path, "isArchived": archived, "createdAt": created, "updatedAt": updated, "crawlStatus": status, "lastCrawledAt": last}); err != nil {
			return err
		}
	}
	return rows.Err()
}
func exportErrors(db *DB, out string) error {
	rows, err := db.SQL.Query(`SELECT run_id,project_id,parent_id,resource_type,resource_id,operation,http_status,error_message,retry_count,created_at FROM tb_crawl_errors ORDER BY created_at`)
	if err != nil {
		return err
	}
	defer rows.Close()
	f, err := os.Create(filepath.Join(out, "tb_errors.csv"))
	if err != nil {
		return err
	}
	defer f.Close()
	c := csv.NewWriter(f)
	_ = c.Write([]string{"run_id", "project_id", "parent_id", "resource_type", "resource_id", "operation", "http_status", "error_message", "retry_count", "created_at"})
	for rows.Next() {
		var run, p, parent, typ, id, op, msg, created string
		var status, retry int
		if err = rows.Scan(&run, &p, &parent, &typ, &id, &op, &status, &msg, &retry, &created); err != nil {
			return err
		}
		_ = c.Write([]string{run, p, parent, typ, id, op, strconv.Itoa(status), msg, strconv.Itoa(retry), created})
	}
	c.Flush()
	return c.Error()
}
