package tbinventory

import "context"

type Config struct{ AppID, AppSecret, OrgID, OperatorID, APIBase string }

func (c Config) Validate() error {
	if c.AppID == "" || c.AppSecret == "" || c.OrgID == "" {
		return ErrConfiguration
	}
	return nil
}

type Project struct {
	ID, Name, URL string
	RootParentID  string
	Archived      bool
}
type Collection struct {
	ID, ParentID, Title, PrefixPath, CreatorID string
	Archived                                   bool
	Created, Updated                           string
}
type Work struct {
	ID, ParentID, PrefixPath, FileName, MIMEType, CreatorID string
	FileSize                                                *int64
	Archived                                                bool
	Created, Updated, SourcePageURL                         string
}
type Page struct {
	Collections   []Collection
	Works         []Work
	NextPageToken string
	Diagnostics   ResponseDiagnostics
}
type ResponseDiagnostics struct {
	HTTPStatus   int
	BusinessCode *float32
	ErrorMessage string
	RequestID    string
	RawResponse  string
}
type APIError struct {
	Status       int
	BusinessCode *float32
	RequestID    string
	Message      string
	RetryCount   int
}

func (e *APIError) Error() string { return e.Message }

type ListOptions struct {
	IncludeArchived bool
	PageSize        int
	OperatorID      string
}
type ProjectSource interface {
	ListProjects(ctx context.Context, pageSize int, operatorID string) ([]Project, error)
}
type FileSource interface {
	ListFiles(ctx context.Context, projectID, parentID, pageToken string, opts ListOptions) (Page, int, error)
}
