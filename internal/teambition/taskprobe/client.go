package taskprobe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Config struct {
	Host, Token string
	HTTP        *http.Client
	Retries     int
}

func LoadConfig() Config {
	return Config{Host: os.Getenv("TEAMBITION_MCP_HOST"), Token: os.Getenv("TEAMBITION_MCP_TOKEN"), HTTP: &http.Client{Timeout: 45 * time.Second}, Retries: 3}
}
func (c Config) Validate() error {
	if strings.TrimSpace(c.Host) == "" {
		return errors.New("TEAMBITION_MCP_HOST is required")
	}
	if strings.TrimSpace(c.Token) == "" {
		return errors.New("TEAMBITION_MCP_TOKEN is required")
	}
	return nil
}

type Client struct {
	cfg         Config
	initialized bool
	nextID      int
}

func NewClient(cfg Config) *Client {
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 45 * time.Second}
	}
	return &Client{cfg: cfg, nextID: 1}
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   json.RawMessage `json:"error"`
}

func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, int, error) {
	if err := c.cfg.Validate(); err != nil {
		return nil, 0, err
	}
	id := c.nextID
	c.nextID++
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return nil, 0, err
	}
	max := c.cfg.Retries
	if max < 0 {
		max = 0
	}
	var last error
	status := 0
	for attempt := 0; attempt <= max; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.cfg.Host, "/")+"/mcp", bytes.NewReader(body))
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.cfg.HTTP.Do(req)
		if err != nil {
			last = err
			status = 0
		} else {
			status = resp.StatusCode
			data, readErr := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
			resp.Body.Close()
			if readErr != nil {
				last = readErr
			} else if status >= 200 && status < 300 {
				data = extractSSE(data)
				var out rpcResponse
				if err = json.Unmarshal(data, &out); err != nil {
					last = fmt.Errorf("invalid MCP response: %w", err)
				} else if len(out.Error) > 0 {
					last = fmt.Errorf("MCP %s failed: %s", method, string(out.Error))
				} else {
					return data, status, nil
				}
			} else {
				last = fmt.Errorf("MCP HTTP %d", status)
			}
		}
		if !retryable(status) || attempt == max {
			break
		}
		delay := time.Duration(float64(250*time.Millisecond) * math.Pow(2, float64(attempt)))
		select {
		case <-ctx.Done():
			return nil, status, ctx.Err()
		case <-time.After(delay):
		}
	}
	return nil, status, last
}
func extractSSE(data []byte) []byte {
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "data:") {
			v := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if strings.HasPrefix(v, "{") {
				return []byte(v)
			}
		}
	}
	return data
}
func retryable(s int) bool { return s == 429 || s == 502 || s == 503 || s == 504 || s == 0 }
func (c *Client) Initialize(ctx context.Context) error {
	if c.initialized {
		return nil
	}
	_, s, e := c.call(ctx, "initialize", map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "thoughtsexport-task-probe", "version": "1"}})
	if e != nil {
		return fmt.Errorf("initialize (status %d): %w", s, e)
	}
	c.initialized = true
	return nil
}
func (c *Client) Call(ctx context.Context, name string, args any) (json.RawMessage, int, error) {
	if err := c.Initialize(ctx); err != nil {
		return nil, 0, err
	}
	return c.call(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
}

func (c *Client) DownloadURL(ctx context.Context, rawURL, destination string) (int64, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, "", err
	}
	resp, err := c.cfg.HTTP.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, "", fmt.Errorf("download HTTP %d", resp.StatusCode)
	}
	tmp := destination + ".part"
	if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return 0, "", err
	}
	f, err := os.Create(tmp)
	if err != nil {
		return 0, "", err
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), resp.Body)
	closeErr := f.Close()
	if copyErr == nil {
		copyErr = closeErr
	}
	if copyErr == nil {
		copyErr = os.Rename(tmp, destination)
	} else {
		_ = os.Remove(tmp)
	}
	if copyErr != nil {
		return 0, "", copyErr
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}
