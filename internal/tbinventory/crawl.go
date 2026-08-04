package tbinventory

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

type CrawlOptions struct {
	IncludeArchived      bool
	SkipForbiddenFolders bool
	PageSize, Retries    int
	RetryDelay           time.Duration
	Resume, ForceRefresh bool
	Concurrency          int
}
type Crawler struct {
	Files     FileSource
	DB        *DB
	Options   CrawlOptions
	RunID     string
	mu        sync.Mutex
	SeenWorks map[string]bool
}

func (c *Crawler) CrawlProject(ctx context.Context, p Project) error {
	c.mu.Lock()
	if c.SeenWorks == nil {
		c.SeenWorks = map[string]bool{}
	}
	c.mu.Unlock()
	if err := c.DB.UpsertProject(p, "crawling", ""); err != nil {
		return err
	}
	rootParent := p.RootParentID
	seenFolders := map[string]bool{rootParent: true}
	queue := []string{rootParent}
	skippedFolders := 0

parentLoop:
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		token := ""
		seenTokens := map[string]bool{}
		for {
			if seenTokens[token] {
				break
			}
			seenTokens[token] = true
			page, status, err := c.listWithRetry(ctx, p.ID, parent, token)
			if err != nil {
				retries := 0
				var apiErr *APIError
				if errors.As(err, &apiErr) {
					retries = apiErr.RetryCount
				}
				_ = c.DB.AddError(c.RunID, p.ID, parent, "listing", parent, "ListFilesV3", status, err.Error(), retries)
				if c.Options.SkipForbiddenFolders && parent != rootParent && isForbidden(status, err) {
					skippedFolders++
					continue parentLoop
				}
				_ = c.DB.UpsertProject(p, "failed", err.Error())
				return err
			}
			for _, f := range page.Collections {
				if f.ID == "" || seenFolders[f.ID] {
					continue
				}
				seenFolders[f.ID] = true
				if err := c.DB.UpsertFolder(p.ID, f); err != nil {
					return err
				}
				queue = append(queue, f.ID)
			}
			for _, w := range page.Works {
				if w.ID == "" {
					continue
				}
				key := p.ID + "\x00" + w.ID
				c.mu.Lock()
				dup := c.SeenWorks[key]
				c.SeenWorks[key] = true
				c.mu.Unlock()
				if dup {
					continue
				}
				if w.SourcePageURL == "" {
					w.SourcePageURL = WorkURL(p.ID, parent, w.ID)
				}
				if err := c.DB.UpsertWork(p.ID, w); err != nil {
					return err
				}
			}
			token = page.NextPageToken
			if token == "" {
				break
			}
		}
	}
	if skippedFolders > 0 {
		partialErr := &PartialCrawlError{SkippedFolders: skippedFolders}
		if err := c.DB.UpsertProject(p, "partial", partialErr.Error()); err != nil {
			return err
		}
		return partialErr
	}
	return c.DB.UpsertProject(p, "success", "")
}
func (c *Crawler) listWithRetry(ctx context.Context, pid, parent, token string) (Page, int, error) {
	max := c.Options.Retries
	if max < 0 {
		max = 0
	}
	delay := c.Options.RetryDelay
	if delay == 0 {
		delay = 250 * time.Millisecond
	}
	var last error
	status := 0
	attempts := 0
	for n := 0; n <= max; n++ {
		attempts = n
		page, s, err := c.Files.ListFiles(ctx, pid, parent, token, ListOptions{c.Options.IncludeArchived, c.Options.PageSize, ""})
		status = s
		if err == nil {
			return page, status, nil
		}
		last = err
		if !retryable(s) {
			break
		}
		if n < max {
			d := delay * time.Duration(math.Pow(2, float64(n)))
			select {
			case <-ctx.Done():
				return Page{}, status, ctx.Err()
			case <-time.After(d):
			}
		}
	}
	wrapped := &APIError{Status: status, Message: fmt.Sprintf("%v", last), RetryCount: attempts}
	var apiErr *APIError
	if errors.As(last, &apiErr) {
		wrapped.BusinessCode = apiErr.BusinessCode
		wrapped.RequestID = apiErr.RequestID
	}
	return Page{}, status, wrapped
}

func isForbidden(status int, err error) bool {
	if status == 403 {
		return true
	}
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.BusinessCode != nil && *apiErr.BusinessCode == 403
}

func retryable(status int) bool {
	return status == 0 || status == 408 || status == 429 || status >= 500
}
