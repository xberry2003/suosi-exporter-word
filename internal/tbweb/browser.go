package tbweb

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"thoughtsexport/libs/logic"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/storage"
	"github.com/chromedp/chromedp"
)

type Session struct {
	CookieHeader string
	Referer      string
}

// BrowserSession owns one browser tab for the whole batch. When
// TEAMBITION_CDP_URL is set it attaches to the shared persistent browser;
// otherwise it starts a local browser with the supplied profile directory.
type BrowserSession struct {
	ctx             context.Context
	cancelBrowser   context.CancelFunc
	cancelAllocator context.CancelFunc
}

func OpenBrowserSession(ctx context.Context, startURL, profileDir string, loginTimeout time.Duration) (*BrowserSession, Session, error) {
	if profileDir == "" {
		return nil, Session{}, errors.New("browser profile directory is required")
	}
	absProfile, err := filepath.Abs(profileDir)
	if err != nil {
		return nil, Session{}, err
	}
	if err := os.MkdirAll(absProfile, 0700); err != nil {
		return nil, Session{}, fmt.Errorf("create browser profile: %w", err)
	}

	var allocatorCtx context.Context
	var cancelAllocator context.CancelFunc
	if remoteURL := strings.TrimSpace(os.Getenv("TEAMBITION_CDP_URL")); remoteURL != "" {
		if logic.TeambitionAuthManagerConfigured() {
			authCtx := ctx
			var cancel context.CancelFunc
			if loginTimeout > 0 {
				authCtx, cancel = context.WithTimeout(ctx, loginTimeout)
				defer cancel()
			}
			if err := logic.EnsureTeambitionBrowserSession(authCtx, startURL); err != nil {
				return nil, Session{}, err
			}
		}
		allocatorCtx, cancelAllocator = chromedp.NewRemoteAllocator(ctx, remoteURL)
	} else {
		execPath := logic.FindExecPath()
		if execPath == "" {
			return nil, Session{}, errors.New("Chrome or Edge was not found")
		}
		allocatorOptions := append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.ExecPath(execPath),
			chromedp.UserDataDir(absProfile),
			chromedp.Flag("headless", true),
			chromedp.Flag("disable-gpu", true),
			chromedp.Flag("disable-dev-shm-usage", true),
			chromedp.Flag("no-sandbox", true),
			chromedp.Flag("no-first-run", true),
			chromedp.Flag("no-default-browser-check", true),
		)
		allocatorCtx, cancelAllocator = chromedp.NewExecAllocator(ctx, allocatorOptions...)
	}
	browserCtx, cancelBrowser := chromedp.NewContext(allocatorCtx, chromedp.WithErrorf(browserErrorLogger))
	browser := &BrowserSession{ctx: browserCtx, cancelBrowser: cancelBrowser, cancelAllocator: cancelAllocator}
	if err := chromedp.Run(browserCtx, chromedp.Navigate(startURL)); err != nil {
		browser.Close()
		return nil, Session{}, fmt.Errorf("open Teambition page: %w", err)
	}
	session, err := browser.waitForLogin(loginTimeout, startURL)
	if err != nil {
		browser.Close()
		return nil, Session{}, err
	}
	return browser, session, nil
}

func (b *BrowserSession) Navigate(ctx context.Context, projectURL string) (Session, error) {
	if err := ctx.Err(); err != nil {
		return Session{}, err
	}
	if err := chromedp.Run(b.ctx,
		chromedp.Navigate(projectURL),
		chromedp.WaitReady("body", chromedp.ByQuery),
	); err != nil {
		return Session{}, fmt.Errorf("navigate to %s: %w", projectURL, err)
	}
	cookies, err := allCookies(b.ctx)
	if err != nil {
		return Session{}, fmt.Errorf("refresh browser session: %w", err)
	}
	if !hasCookie(cookies, "TB_ACCESS_TOKEN") {
		return Session{}, errors.New("Teambition login expired; log in again in the open browser")
	}
	return Session{CookieHeader: cookieHeader(cookies), Referer: projectURL}, nil
}

func (b *BrowserSession) Close() {
	if b == nil {
		return
	}
	if b.cancelBrowser != nil {
		b.cancelBrowser()
	}
	if b.cancelAllocator != nil {
		b.cancelAllocator()
	}
}

func (b *BrowserSession) waitForLogin(timeout time.Duration, referer string) (Session, error) {
	waitCtx := b.ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		waitCtx, cancel = context.WithTimeout(b.ctx, timeout)
		defer cancel()
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		cookies, err := allCookies(waitCtx)
		if err != nil {
			return Session{}, fmt.Errorf("read browser session: %w", err)
		}
		if hasCookie(cookies, "TB_ACCESS_TOKEN") {
			return Session{CookieHeader: cookieHeader(cookies), Referer: referer}, nil
		}
		select {
		case <-waitCtx.Done():
			return Session{}, fmt.Errorf("waiting for Teambition login: %w", waitCtx.Err())
		case <-ticker.C:
		}
	}
}

func browserErrorLogger(format string, args ...interface{}) {
	message := fmt.Sprintf(format, args...)
	if strings.Contains(message, "unknown ClientNavigationReason value") {
		return
	}
	fmt.Fprintln(os.Stderr, message)
}

func allCookies(ctx context.Context) ([]*network.Cookie, error) {
	var cookies []*network.Cookie
	err := chromedp.Run(ctx, chromedp.ActionFunc(func(actionCtx context.Context) error {
		var err error
		cookies, err = storage.GetCookies().Do(actionCtx)
		return err
	}))
	return cookies, err
}

func hasCookie(cookies []*network.Cookie, name string) bool {
	for _, cookie := range cookies {
		if cookie.Name == name && cookie.Value != "" {
			return true
		}
	}
	return false
}

func cookieHeader(cookies []*network.Cookie) string {
	values := make(map[string]string)
	for _, cookie := range cookies {
		domain := strings.TrimPrefix(strings.ToLower(cookie.Domain), ".")
		if (domain == "teambition.com" || strings.HasSuffix(domain, ".teambition.com")) && cookie.Value != "" {
			values[cookie.Name] = cookie.Value
		}
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+"="+values[name])
	}
	return strings.Join(parts, "; ")
}
