package tbinventory

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
)

type Summary struct {
	ProjectCount       int             `json:"projectCount"`
	FolderCount        int             `json:"folderCount"`
	FileCount          int             `json:"fileCount"`
	ActiveFileCount    int             `json:"activeFileCount"`
	ArchivedFileCount  int             `json:"archivedFileCount"`
	TotalSizeBytes     int64           `json:"totalSizeBytes"`
	TotalSizeGiB       float64         `json:"totalSizeGiB"`
	TotalSizeTiB       float64         `json:"totalSizeTiB"`
	ActiveSizeBytes    int64           `json:"activeSizeBytes"`
	ArchivedSizeBytes  int64           `json:"archivedSizeBytes"`
	MissingSizeCount   int             `json:"missingSizeCount"`
	CurrentVersionOnly bool            `json:"currentVersionOnly"`
	StorageScenarios   Scenarios       `json:"storageScenarios"`
	ByProject          []ProjectStat   `json:"byProject"`
	ByExtension        []ExtensionStat `json:"byExtension"`
	LargestFiles       []FileStat      `json:"largestFiles"`
}
type Scenarios struct {
	Raw1xBytes                  int64 `json:"raw1xBytes"`
	RawPlus30PercentBytes       int64 `json:"rawPlus30PercentBytes"`
	Replica2xBytes              int64 `json:"replica2xBytes"`
	Replica2xPlus30PercentBytes int64 `json:"replica2xPlus30PercentBytes"`
}
type ProjectStat struct {
	ProjectID      string `json:"projectId"`
	ProjectName    string `json:"projectName"`
	FileCount      int    `json:"fileCount"`
	TotalSizeBytes int64  `json:"totalSizeBytes"`
}
type ExtensionStat struct {
	Extension      string `json:"extension"`
	FileCount      int    `json:"fileCount"`
	TotalSizeBytes int64  `json:"totalSizeBytes"`
}
type FileStat struct {
	ProjectID     string `json:"projectId"`
	WorkID        string `json:"workId"`
	FileName      string `json:"fileName"`
	FileSizeBytes int64  `json:"fileSizeBytes"`
	PrefixPath    string `json:"prefixPath"`
	SourcePageURL string `json:"sourcePageUrl"`
}

func BuildSummary(db *DB) (Summary, error) {
	var s Summary
	s.CurrentVersionOnly = true
	s.ByProject = []ProjectStat{}
	s.ByExtension = []ExtensionStat{}
	s.LargestFiles = []FileStat{}
	err := db.SQL.QueryRow(`SELECT COUNT(*) FROM tb_projects`).Scan(&s.ProjectCount)
	if err != nil {
		return s, err
	}
	if err = db.SQL.QueryRow(`SELECT COUNT(*) FROM tb_folders`).Scan(&s.FolderCount); err != nil {
		return s, err
	}
	pm := map[string]*ProjectStat{}
	projectRows, err := db.SQL.Query(`SELECT project_id,project_name FROM tb_projects`)
	if err != nil {
		return s, err
	}
	for projectRows.Next() {
		var p ProjectStat
		if err = projectRows.Scan(&p.ProjectID, &p.ProjectName); err != nil {
			projectRows.Close()
			return s, err
		}
		pm[p.ProjectID] = &p
	}
	if err = projectRows.Close(); err != nil {
		return s, err
	}
	rows, err := db.SQL.Query(`SELECT project_id,work_id,file_name,file_size_bytes,is_archived,prefix_path,source_page_url FROM tb_works`)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	em := map[string]*ExtensionStat{}
	for rows.Next() {
		var pid, wid, name, path, url string
		var size sql.NullInt64
		var arch bool
		if err := rows.Scan(&pid, &wid, &name, &size, &arch, &path, &url); err != nil {
			return s, err
		}
		s.FileCount++
		if arch {
			s.ArchivedFileCount++
		} else {
			s.ActiveFileCount++
		}
		p := pm[pid]
		if p == nil {
			p = &ProjectStat{ProjectID: pid}
			pm[pid] = p
		}
		p.FileCount++
		ext := extension(name)
		e := em[ext]
		if e == nil {
			e = &ExtensionStat{Extension: ext}
			em[ext] = e
		}
		e.FileCount++
		if !size.Valid || size.Int64 < 0 {
			s.MissingSizeCount++
			continue
		}
		n := size.Int64
		s.TotalSizeBytes += n
		if arch {
			s.ArchivedSizeBytes += n
		} else {
			s.ActiveSizeBytes += n
		}
		p.TotalSizeBytes += n
		e.TotalSizeBytes += n
	}
	for _, p := range pm {
		s.ByProject = append(s.ByProject, *p)
	}
	for _, e := range em {
		s.ByExtension = append(s.ByExtension, *e)
	}
	sort.Slice(s.ByProject, func(i, j int) bool { return s.ByProject[i].TotalSizeBytes > s.ByProject[j].TotalSizeBytes })
	sort.Slice(s.ByExtension, func(i, j int) bool { return s.ByExtension[i].TotalSizeBytes > s.ByExtension[j].TotalSizeBytes })
	s.TotalSizeGiB = float64(s.TotalSizeBytes) / (1 << 30)
	s.TotalSizeTiB = float64(s.TotalSizeBytes) / (1 << 40)
	s.StorageScenarios = Scenarios{s.TotalSizeBytes, s.TotalSizeBytes * 130 / 100, s.TotalSizeBytes * 2, s.TotalSizeBytes * 260 / 100}
	largest, err := db.SQL.Query(`SELECT project_id,work_id,file_name,file_size_bytes,prefix_path,source_page_url FROM tb_works WHERE file_size_bytes IS NOT NULL AND file_size_bytes>=0 ORDER BY file_size_bytes DESC LIMIT 100`)
	if err != nil {
		return s, err
	}
	defer largest.Close()
	for largest.Next() {
		var f FileStat
		if err = largest.Scan(&f.ProjectID, &f.WorkID, &f.FileName, &f.FileSizeBytes, &f.PrefixPath, &f.SourcePageURL); err != nil {
			return s, err
		}
		s.LargestFiles = append(s.LargestFiles, f)
	}
	return s, nil
}
func WriteSummaryJSON(path string, s Summary) error {
	b, e := json.MarshalIndent(s, "", "  ")
	if e != nil {
		return e
	}
	return writeFile(path, append(b, '\n'))
}
func SummaryMarkdown(s Summary) string {
	return fmt.Sprintf("# Teambition inventory summary\n\n- Projects: %d\n- Folders: %d\n- Files: %d (active %d, archived %d)\n- Total size: %d bytes (%.3f GiB, %.3f TiB)\n- Missing/invalid file size: %d\n\nCurrent API file sizes only; historical versions are excluded.\n", s.ProjectCount, s.FolderCount, s.FileCount, s.ActiveFileCount, s.ArchivedFileCount, s.TotalSizeBytes, s.TotalSizeGiB, s.TotalSizeTiB, s.MissingSizeCount)
}
