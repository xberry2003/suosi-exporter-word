package logic

import (
	"errors"
	"net/url"
	"strings"
)

// ValidateThoughtsCookie verifies a session with the lightest workspace API
// request used by the exporter. It never writes the supplied cookie anywhere.
func ValidateThoughtsCookie(cookie, workspaceURL string) error {
	if strings.TrimSpace(cookie) == "" {
		return errors.New("empty Teambition session")
	}
	parsed, err := url.Parse(workspaceURL)
	if err != nil || !strings.EqualFold(parsed.Hostname(), "thoughts.teambition.com") {
		return errors.New("invalid Thoughts workspace URL")
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 2 || parts[0] != "workspaces" || parts[1] == "" {
		return errors.New("invalid Thoughts workspace URL")
	}
	_, err = NewRequest(cookie, parts[1]).GetWorkspace(parts[1])
	return err
}
