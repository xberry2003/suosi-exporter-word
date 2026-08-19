package control

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"thoughtsexport/libs/logic"
)

// thoughtsSession serializes access to the dedicated browser profile and keeps
// a validated Cookie header in memory so ordinary jobs do not launch Chrome.
type thoughtsSession struct {
	profileDir string
	mu         sync.Mutex
	cookie     string
}

func newThoughtsSession(dataRoot string) *thoughtsSession {
	return &thoughtsSession{profileDir: filepath.Join(dataRoot, "browser-profiles", "thoughts")}
}

func (s *thoughtsSession) Acquire(ctx context.Context, workspaceURL string, progress func(string, string)) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cookie != "" {
		progress("authentication", "正在复用已验证的所思登录态")
		if err := logic.ValidateThoughtsCookie(s.cookie, workspaceURL); err == nil {
			return s.cookie, nil
		}
		s.cookie = ""
	}
	if logic.TeambitionAuthManagerConfigured() {
		progress("authentication", "正在检查服务器持久登录态")
		authCtx, cancel := context.WithTimeout(ctx, 75*time.Second)
		err := logic.EnsureTeambitionBrowserSession(authCtx, workspaceURL)
		cancel()
		if err != nil {
			return "", fmt.Errorf("所思登录态恢复失败：%w", err)
		}
		progress("authentication", "已复用服务器浏览器登录态")
		loginCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		cookie, err := logic.GetLoginCookieStringContext(loginCtx, workspaceURL, "TB_ACCESS_TOKEN", s.profileDir)
		cancel()
		if err != nil {
			return "", fmt.Errorf("读取所思登录态失败：%w", err)
		}
		if err := logic.ValidateThoughtsCookie(cookie, workspaceURL); err != nil {
			return "", fmt.Errorf("所思登录态验证失败：%w", err)
		}
		s.cookie = cookie
		return cookie, nil
	}

	credentials := logic.BrowserLoginCredentialsFromEnv()
	progress("authentication", "正在读取持久所思登录态")
	loginCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	cookie, err := logic.GetLoginCookieStringWithCredentialsContext(loginCtx, workspaceURL, "TB_ACCESS_TOKEN", s.profileDir, credentials, false)
	cancel()
	if err == nil && logic.ValidateThoughtsCookie(cookie, workspaceURL) == nil {
		s.cookie = cookie
		return cookie, nil
	}
	if !credentials.Configured() {
		if err != nil {
			return "", fmt.Errorf("所思登录态不可用：%w", err)
		}
		return "", fmt.Errorf("所思登录态已失效；请配置 TEAMBITION_LOGIN_USERNAME 和 TEAMBITION_LOGIN_PASSWORD，或在持久浏览器 Profile 中重新登录")
	}

	progress("authentication", "登录态已失效，正在使用服务端账号自动登录")
	loginCtx, cancel = context.WithTimeout(ctx, 90*time.Second)
	cookie, err = logic.GetLoginCookieStringWithCredentialsContext(loginCtx, workspaceURL, "TB_ACCESS_TOKEN", s.profileDir, credentials, true)
	cancel()
	if err != nil {
		return "", fmt.Errorf("所思自动登录失败：%w", err)
	}
	if err := logic.ValidateThoughtsCookie(cookie, workspaceURL); err != nil {
		return "", fmt.Errorf("所思自动登录未获得有效会话：%w", err)
	}
	s.cookie = cookie
	return cookie, nil
}
