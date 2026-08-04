package tbweb

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"thoughtsexport/libs/logic"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

type Session struct {
	CookieHeader string
	Referer      string
}

func AcquireSession(ctx context.Context, projectURL string, timeout time.Duration) (Session, error) {
	execPath := logic.FindExecPath()
	if execPath == "" {
		return Session{}, errors.New("Chrome or Edge was not found")
	}
	profileDir, err := os.MkdirTemp("", "tb-web-inventory-profile-")
	if err != nil {
		return Session{}, err
	}
	defer os.RemoveAll(profileDir)

	allocatorOptions := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(execPath),
		chromedp.UserDataDir(profileDir),
		chromedp.Flag("headless", false),
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("no-default-browser-check", true),
	)
	allocatorCtx, cancelAllocator := chromedp.NewExecAllocator(ctx, allocatorOptions...)
	defer cancelAllocator()
	browserCtx, cancelBrowser := chromedp.NewContext(allocatorCtx, chromedp.WithErrorf(func(format string, args ...interface{}) {
		message := fmt.Sprintf(format, args...)
		if strings.Contains(message, "unknown ClientNavigationReason value") {
			return
		}
		fmt.Fprintln(os.Stderr, message)
	}))
	defer cancelBrowser()
	if err := chromedp.Run(browserCtx, chromedp.Navigate(projectURL)); err != nil {
		return Session{}, fmt.Errorf("open Teambition login page: %w", err)
	}

	waitCtx := browserCtx
	var cancelWait context.CancelFunc
	if timeout > 0 {
		waitCtx, cancelWait = context.WithTimeout(browserCtx, timeout)
		defer cancelWait()
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		cookies, err := allCookies(waitCtx)
		if err != nil {
			return Session{}, fmt.Errorf("read browser session: %w", err)
		}
		if hasCookie(cookies, "TB_ACCESS_TOKEN") {
			return Session{CookieHeader: cookieHeader(cookies), Referer: projectURL}, nil
		}
		select {
		case <-waitCtx.Done():
			return Session{}, fmt.Errorf("waiting for Teambition login: %w", waitCtx.Err())
		case <-ticker.C:
		}
	}
}

func allCookies(ctx context.Context) ([]*network.Cookie, error) {
	var cookies []*network.Cookie
	err := chromedp.Run(ctx, chromedp.ActionFunc(func(actionCtx context.Context) error {
		var err error
		cookies, err = network.GetAllCookies().Do(actionCtx)
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
