package logic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

const defaultLoginTimeout = 10 * time.Minute

var chromeNetworkErrorPattern = regexp.MustCompile(`ERR_[A-Z_]+`)

type BrowserLoginCredentials struct {
	Username string
	Password string
}

func BrowserLoginCredentialsFromEnv() BrowserLoginCredentials {
	return BrowserLoginCredentials{Username: strings.TrimSpace(os.Getenv("TEAMBITION_LOGIN_USERNAME")), Password: os.Getenv("TEAMBITION_LOGIN_PASSWORD")}
}

func (c BrowserLoginCredentials) Configured() bool {
	return c.Username != "" && c.Password != ""
}

func GetLoginCookieString(loginURL, waitCookieKey string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultLoginTimeout)
	defer cancel()
	return GetLoginCookieStringContext(ctx, loginURL, waitCookieKey, "")
}

func GetLoginCookieStringContext(ctx context.Context, loginURL, waitCookieKey, profileDir string) (string, error) {
	return getLoginCookieString(ctx, loginURL, waitCookieKey, profileDir, BrowserLoginCredentials{}, false)
}

func GetLoginCookieStringWithCredentialsContext(ctx context.Context, loginURL, waitCookieKey, profileDir string, credentials BrowserLoginCredentials, forceLogin bool) (string, error) {
	return getLoginCookieString(ctx, loginURL, waitCookieKey, profileDir, credentials, forceLogin)
}

func getLoginCookieString(ctx context.Context, loginURL, waitCookieKey, profileDir string, credentials BrowserLoginCredentials, forceLogin bool) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	remoteBrowserURL := strings.TrimSpace(os.Getenv("TEAMBITION_CDP_URL"))
	ownedBrowser := remoteBrowserURL == ""
	cleanupProfile := false
	if strings.TrimSpace(profileDir) == "" {
		temporary, err := os.MkdirTemp("", "thoughtsexport-browser-")
		if err != nil {
			return "", fmt.Errorf("create browser profile: %w", err)
		}
		profileDir = temporary
		cleanupProfile = true
	}
	if cleanupProfile {
		defer os.RemoveAll(profileDir)
	}
	absProfile, err := filepath.Abs(profileDir)
	if err != nil {
		return "", fmt.Errorf("resolve browser profile: %w", err)
	}
	if err := os.MkdirAll(absProfile, 0700); err != nil {
		return "", fmt.Errorf("create browser profile: %w", err)
	}

	var procCancel context.CancelFunc
	var processDone chan error
	var cmd *exec.Cmd
	browserClosed := false
	wsURL := remoteBrowserURL
	if ownedBrowser {
		execPath := FindExecPath()
		if execPath == "" {
			return "", errors.New("Chrome or Edge was not found")
		}
		procCtx, cancelProcess := context.WithCancel(ctx)
		procCancel = cancelProcess
		cmd = exec.CommandContext(procCtx, execPath,
			"--no-first-run",
			"--no-default-browser-check",
			"--disable-gpu",
			"--disable-dev-shm-usage",
			"--no-sandbox",
			"--headless=new",
			"--lang=zh-CN",
			"--user-data-dir="+absProfile,
			"--remote-debugging-port=0",
			"--remote-debugging-address=127.0.0.1",
		)
		stderr, pipeErr := cmd.StderrPipe()
		if pipeErr != nil {
			procCancel()
			return "", fmt.Errorf("open browser diagnostics: %w", pipeErr)
		}
		if startErr := cmd.Start(); startErr != nil {
			procCancel()
			return "", fmt.Errorf("start browser: %w", startErr)
		}
		processDone = make(chan error, 1)
		go func() { processDone <- cmd.Wait() }()
		defer func() {
			if !browserClosed {
				procCancel()
			}
			select {
			case <-processDone:
			case <-time.After(5 * time.Second):
				procCancel()
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
				<-processDone
			}
		}()
		wsURL, err = ReadOutput(stderr, io.Discard, nil)
		if err != nil {
			return "", err
		}
		log.Printf("login browser ready profile=%s", absProfile)
	} else {
		log.Printf("login browser connected to persistent CDP endpoint")
	}
	allocCtx, allocCancel := chromedp.NewRemoteAllocator(ctx, wsURL)
	defer allocCancel()
	taskCtx, taskCancel := chromedp.NewContext(allocCtx)
	defer taskCancel()
	if forceLogin {
		if !credentials.Configured() {
			return "", errors.New("登录态已失效，未配置 TEAMBITION_LOGIN_USERNAME 和 TEAMBITION_LOGIN_PASSWORD")
		}
		if err := chromedp.Run(taskCtx, network.ClearBrowserCookies()); err != nil {
			return "", fmt.Errorf("clear expired browser cookies: %w", err)
		}
	}
	if !forceLogin {
		var pageText string
		if err := chromedp.Run(taskCtx,
			chromedp.Navigate(loginURL),
			chromedp.WaitReady("body", chromedp.ByQuery),
			chromedp.Evaluate(`document.body ? document.body.innerText : ""`, &pageText),
		); err != nil {
			return "", fmt.Errorf("open source page: %w", err)
		}
		log.Printf("login source page ready")
		if code := chromeNetworkErrorPattern.FindString(pageText); code != "" {
			return "", fmt.Errorf("source page failed to load: %s", code)
		}
		cookie, found, err := readLoginCookie(taskCtx, waitCookieKey)
		if err != nil {
			return "", err
		}
		if found {
			if ownedBrowser {
				browserClosed = closeBrowser(taskCtx) == nil
			}
			return cookie, nil
		}
	}
	if credentials.Configured() {
		log.Printf("login form submission begin force=%t", forceLogin)
		if attempted, err := submitPasswordLogin(taskCtx, credentials); err != nil {
			return "", err
		} else if attempted {
			log.Printf("login form submission clicked")
			time.Sleep(750 * time.Millisecond)
		} else {
			return "", errors.New("未找到 Teambition 账号密码登录表单")
		}
	}
	log.Printf("login cookie wait begin key=%s", waitCookieKey)
	cookie, waitErr := WaitLoginReturnCookieString(taskCtx, waitCookieKey)
	// Locally launched Chrome exits normally so session cookies are committed.
	// A shared CDP browser must stay alive for later jobs and manual recovery.
	if ownedBrowser {
		browserClosed = closeBrowser(taskCtx) == nil
	}
	return cookie, waitErr
}

func closeBrowser(ctx context.Context) error {
	chromedpContext := chromedp.FromContext(ctx)
	if chromedpContext == nil || chromedpContext.Browser == nil {
		return errors.New("browser connection is unavailable")
	}
	return cdpbrowser.Close().Do(cdp.WithExecutor(ctx, chromedpContext.Browser))
}

func submitPasswordLogin(ctx context.Context, credentials BrowserLoginCredentials) (bool, error) {
	// The account page exposes a stable password-login route. Going there
	// directly avoids relying on the asynchronously rendered QR-code menu.
	if err := chromedp.Run(ctx,
		chromedp.Navigate("https://account.teambition.com/login/password"),
		chromedp.WaitReady("body", chromedp.ByQuery),
	); err != nil {
		return false, fmt.Errorf("open Teambition password login: %w", err)
	}

	username, err := json.Marshal(credentials.Username)
	if err != nil {
		return false, errors.New("encode login username")
	}
	password, err := json.Marshal(credentials.Password)
	if err != nil {
		return false, errors.New("encode login password")
	}
	script := `(function(username, password) {
  function find(selectors) { for (const selector of selectors) { const element = document.querySelector(selector); if (element && !element.disabled) return element; } return null; }
  function setValue(element, value) { const setter = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(element), "value"); if (!setter) return false; setter.set.call(element, value); element.dispatchEvent(new Event("input", { bubbles: true })); element.dispatchEvent(new Event("change", { bubbles: true })); return true; }
  const user = find(["input[autocomplete='username']", "input[type='email']", "input[type='tel']", "input[type='text']", "input[placeholder='Phone/Email']", "input.linear-input-instance:not([type='password'])", "input:not([type='password'])", "input[name*='email' i]", "input[name*='phone' i]", "input[name*='account' i]", "input[name*='username' i]"]);
  const pass = find(["input[autocomplete='current-password']", "input[type='password']"]);
  if (!user || !pass || !setValue(user, username) || !setValue(pass, password)) return false;
  const agreementControl = find(["input[type='checkbox']", "[role='checkbox']", "input[type='radio']", "[role='radio']"]);
  if (agreementControl) {
    const checked = agreementControl.checked === true || agreementControl.getAttribute("aria-checked") === "true";
    if (!checked) agreementControl.click();
  } else {
    const agreement = Array.from(document.querySelectorAll("label, span, div")).find(element => {
      const text = (element.textContent || "").replace(/\s+/g, "").trim();
      return ((text.startsWith("同意") && text.includes("服务条款")) || /^(Ihave)?readandagree/i.test(text)) && element.getClientRects().length;
    });
    if (agreement) agreement.click();
  }
  if (!window.__suosiLoginReadyAt) {
    window.__suosiLoginReadyAt = Date.now();
    return false;
  }
  if (Date.now() - window.__suosiLoginReadyAt < 1500) return false;
  const submit = find(["button[type='submit']", "input[type='submit']", "button[class*='login' i]", "button[class*='submit' i]", "button.account-btn", "button.next-btn-primary"]);
  if (!submit) return false;
  submit.click();
  return true;
})(` + string(username) + `, ` + string(password) + `)`
	deadline := time.Now().Add(15 * time.Second)
	for {
		var attempted bool
		if err := chromedp.Run(ctx, chromedp.Evaluate(script, &attempted)); err != nil {
			return false, fmt.Errorf("submit automatic Teambition login: %w", err)
		}
		if attempted {
			return true, nil
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func clickLoginTexts(ctx context.Context, labels []string, timeout time.Duration) (bool, error) {
	encoded, err := json.Marshal(labels)
	if err != nil {
		return false, err
	}
	deadline := time.Now().Add(timeout)
	for {
		script := `(function(labels) {
  const normalize = value => (value || "").replace(/\s+/g, "").trim().toLowerCase();
  const wanted = labels.map(normalize);
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
  let node;
  while ((node = walker.nextNode())) {
    if (!wanted.includes(normalize(node.nodeValue))) continue;
    const element = node.parentElement;
    if (element && element.getClientRects().length) {
      (element.closest("button, a, [role='button']") || element).click();
      return true;
    }
  }
  const candidates = Array.from(document.querySelectorAll("button, a, [role='button'], div, span"))
    .filter(element => element.getClientRects().length && wanted.some(label => normalize(element.textContent).startsWith(label)))
    .sort((left, right) => left.children.length - right.children.length);
  if (!candidates.length) return false;
  (candidates[0].closest("button, a, [role='button']") || candidates[0]).click();
  return true;
})(` + string(encoded) + `)`
		var clicked bool
		if err := chromedp.Run(ctx, chromedp.Evaluate(script, &clicked)); err != nil {
			return false, err
		}
		if clicked {
			return true, nil
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func WaitLoginReturnCookieString(ctx context.Context, waitCookieKey string) (string, error) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("waiting for %s login: %w", waitCookieKey, ctx.Err())
		default:
		}
		cookie, found, err := readLoginCookie(ctx, waitCookieKey)
		if err != nil {
			return "", err
		}
		if found {
			return cookie, nil
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("waiting for %s login: %w", waitCookieKey, ctx.Err())
		case <-ticker.C:
		}
	}
}

func readLoginCookie(ctx context.Context, waitCookieKey string) (string, bool, error) {
	var cookies []*network.Cookie
	if err := chromedp.Run(ctx, chromedp.ActionFunc(func(actionCtx context.Context) error {
		var result struct {
			Cookies []*network.Cookie `json:"cookies"`
		}
		if err := cdp.Execute(actionCtx, "Network.getAllCookies", nil, &result); err != nil {
			return err
		}
		cookies = result.Cookies
		return nil
	})); err != nil {
		return "", false, fmt.Errorf("read browser cookies: %w", err)
	}
	cookieNames := make([]string, 0, len(cookies))
	for _, cookie := range cookies {
		cookieNames = append(cookieNames, cookie.Name)
	}
	sort.Strings(cookieNames)
	log.Printf("login cookies read count=%d key=%s names=%s", len(cookies), waitCookieKey, strings.Join(cookieNames, ","))
	values := map[string]string{}
	found := false
	for _, cookie := range cookies {
		domain := strings.TrimPrefix(strings.ToLower(cookie.Domain), ".")
		if domain != "teambition.com" && !strings.HasSuffix(domain, ".teambition.com") {
			continue
		}
		values[cookie.Name] = cookie.Value
		if cookie.Name == waitCookieKey && cookie.Value != "" {
			found = true
		}
	}
	if !found {
		return "", false, nil
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
	return strings.Join(parts, "; "), true, nil
}
