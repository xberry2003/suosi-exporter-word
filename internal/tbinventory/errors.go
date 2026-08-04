package tbinventory

import "errors"

var (
	ErrConfiguration = errors.New("TB_APP_ID, TB_APP_SECRET and TB_ORG_ID are required")
	ErrNoProjects    = errors.New("no projects specified; use --project-id, --projects-file or --discover-projects")
)

type PartialCrawlError struct {
	SkippedFolders int
}

func (e *PartialCrawlError) Error() string {
	return "crawl completed with inaccessible folders skipped"
}
