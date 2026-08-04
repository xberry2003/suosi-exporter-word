package tbinventory

import (
	"fmt"
	"net/url"
	"strings"
)

type ProjectFilesRef struct {
	ProjectID string
	ParentID  string
}

// ParseProjectFilesURL treats the segment after /works/ as the file library's
// starting collection. A nested /work/{workId} suffix is ignored for inventory.
func ParseProjectFilesURL(raw string) (ProjectFilesRef, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ProjectFilesRef{}, fmt.Errorf("invalid Teambition project files URL")
	}
	h := strings.ToLower(u.Hostname())
	if h != "www.teambition.com" && h != "teambition.com" {
		return ProjectFilesRef{}, fmt.Errorf("not a Teambition URL")
	}
	p := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(p) < 3 || p[0] != "project" || p[1] == "" || p[2] != "works" {
		return ProjectFilesRef{}, fmt.Errorf("URL must match /project/{projectId}/works/{collectionId}")
	}
	ref := ProjectFilesRef{ProjectID: p[1]}
	if len(p) >= 4 {
		ref.ParentID = p[3]
	}
	if len(p) > 4 && (len(p) < 6 || p[4] != "work" || p[5] == "") {
		return ProjectFilesRef{}, fmt.Errorf("unsupported Teambition project files URL path")
	}
	return ref, nil
}

func ParseWorkURL(raw string) (projectID, workID string, ok bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", "", false
	}
	h := strings.ToLower(u.Hostname())
	if h != "www.teambition.com" && h != "teambition.com" {
		return "", "", false
	}
	p := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(p) < 4 || p[0] != "project" || p[2] != "works" {
		return "", "", false
	}
	projectID = p[1]
	if len(p) == 4 {
		workID = p[3]
	} else if len(p) >= 6 && p[4] == "work" {
		workID = p[5]
	}
	return projectID, workID, projectID != "" && workID != ""
}
func WorkURL(projectID, collectionID, workID string) string {
	if collectionID != "" {
		return fmt.Sprintf("https://www.teambition.com/project/%s/works/%s/work/%s", projectID, collectionID, workID)
	}
	return fmt.Sprintf("https://www.teambition.com/project/%s/works/%s", projectID, workID)
}
