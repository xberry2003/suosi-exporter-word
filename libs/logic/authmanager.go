package logic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
)

type browserAuthResponse struct {
	OK            bool   `json:"ok"`
	Authenticated bool   `json:"authenticated"`
	Reused        bool   `json:"reused"`
	Error         string `json:"error"`
}

func TeambitionAuthManagerConfigured() bool {
	return strings.TrimSpace(os.Getenv("TEAMBITION_AUTH_URL")) != ""
}

// EnsureTeambitionBrowserSession asks the loopback Python service to restore
// the shared browser session. Cookie values remain inside Chromium.
func EnsureTeambitionBrowserSession(ctx context.Context, targetURL string) error {
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("TEAMBITION_AUTH_URL")), "/")
	if baseURL == "" {
		return errors.New("Teambition authentication service is not configured")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return fmt.Errorf("invalid Teambition authentication service URL: %w", err)
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return errors.New("Teambition authentication service must use a loopback address")
	}
	payload, err := json.Marshal(map[string]string{"target_url": targetURL})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/session/ensure", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("call Teambition authentication service: %w", err)
	}
	defer resp.Body.Close()
	var result browserAuthResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode Teambition authentication response: %w", err)
	}
	if resp.StatusCode != http.StatusOK || !result.OK || !result.Authenticated {
		message := strings.TrimSpace(result.Error)
		if message == "" {
			message = resp.Status
		}
		return fmt.Errorf("Teambition authentication is unavailable: %s", message)
	}
	return nil
}
