package control

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const sessionCookieName = "suosi_control_session"

type AuthConfig struct {
	APIBaseURL    string
	SessionSecret string
	SessionTTL    time.Duration
}

type authUser struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

type authSession struct {
	User      authUser `json:"user"`
	ExpiresAt int64    `json:"expires_at"`
}

type AuthService struct {
	apiBaseURL string
	secret     []byte
	ttl        time.Duration
	client     *http.Client
}

func NewAuthService(config AuthConfig) (*AuthService, error) {
	baseURL, err := url.Parse(strings.TrimRight(strings.TrimSpace(config.APIBaseURL), "/"))
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" {
		return nil, errors.New("员工认证服务地址无效")
	}
	secret := []byte(config.SessionSecret)
	if len(secret) < 32 {
		return nil, errors.New("AUTH_SESSION_SECRET 至少需要 32 个字符")
	}
	ttl := config.SessionTTL
	if ttl <= 0 {
		ttl = 8 * time.Hour
	}
	return &AuthService{apiBaseURL: strings.TrimRight(baseURL.String(), "/"), secret: secret, ttl: ttl, client: &http.Client{Timeout: 12 * time.Second}}, nil
}

func (a *AuthService) authenticate(action string, request map[string]string, requireUser bool) (authUser, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return authUser{}, errors.New("无法编码认证请求")
	}
	httpRequest, err := http.NewRequest(http.MethodPost, a.apiBaseURL+"/"+action, bytes.NewReader(body))
	if err != nil {
		return authUser{}, errors.New("无法创建认证请求")
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := a.client.Do(httpRequest)
	if err != nil {
		return authUser{}, errors.New("员工认证服务暂时不可用")
	}
	defer response.Body.Close()
	var payload struct {
		Success bool     `json:"success"`
		Error   string   `json:"error"`
		Message string   `json:"message"`
		Code    string   `json:"code"`
		User    authUser `json:"user"`
		Data    authUser `json:"data"`
		ID      int64    `json:"id"`
		Name    string   `json:"name"`
		Type    string   `json:"type"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if err := decoder.Decode(&payload); err != nil {
		return authUser{}, errors.New("员工认证服务返回无效响应")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || !payload.Success {
		if message := authMessageForCode(payload.Code); message != "" {
			return authUser{}, errors.New(message)
		}
		return authUser{}, firstAuthError(payload.Error, payload.Message, "认证失败")
	}
	if payload.User.ID == 0 {
		payload.User = payload.Data
	}
	if payload.User.ID == 0 {
		payload.User = authUser{ID: payload.ID, Name: payload.Name, Type: payload.Type}
	}
	if requireUser && (payload.User.ID == 0 || strings.TrimSpace(payload.User.Name) == "") {
		return authUser{}, errors.New("员工认证服务未返回有效用户")
	}
	return payload.User, nil
}

func authMessageForCode(code string) string {
	return map[string]string{
		"NOT_FOUND":         "公司员工中未找到该姓名",
		"NOT_REGISTERED":    "您尚未注册，请先注册",
		"WRONG_PASSWORD":    "密码错误",
		"ACCOUNT_EXPIRED":   "您的账号已到期，请联系管理员续约",
		"ID_CARD_LENGTH":    "身份证号位数不正确",
		"ID_CARD_WRONG":     "身份证号错误",
		"PHONE_WRONG":       "手机号错误",
		"PASSWORD_MISMATCH": "两次密码不一致",
	}[code]
}

func (a *AuthService) checkRemoteSession(user authUser) error {
	body, err := json.Marshal(map[string]string{"name": user.Name})
	if err != nil {
		return errors.New("无法编码会话校验请求")
	}
	request, err := http.NewRequest(http.MethodPost, a.apiBaseURL+"/check-session", bytes.NewReader(body))
	if err != nil {
		return errors.New("无法创建会话校验请求")
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := a.client.Do(request)
	if err != nil {
		return errors.New("员工认证服务暂时不可用")
	}
	defer response.Body.Close()
	var payload struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return errors.New("员工认证服务返回无效响应")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || !payload.Success {
		return firstAuthError(payload.Error, payload.Message, "登录状态已失效")
	}
	return nil
}

func firstAuthError(values ...string) error {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return errors.New(value)
		}
	}
	return errors.New("认证失败")
}

func (a *AuthService) issueSession(w http.ResponseWriter, user authUser, secure bool) {
	session := authSession{User: user, ExpiresAt: time.Now().Add(a.ttl).Unix()}
	payload, _ := json.Marshal(session)
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	signature := a.sign(encoded)
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: encoded + "." + signature, Path: "/", MaxAge: int(a.ttl.Seconds()), HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure})
}

func (a *AuthService) session(r *http.Request) (authSession, error) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return authSession{}, errors.New("请先登录")
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 2 || !hmac.Equal([]byte(parts[1]), []byte(a.sign(parts[0]))) {
		return authSession{}, errors.New("登录状态无效，请重新登录")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return authSession{}, errors.New("登录状态无效，请重新登录")
	}
	var session authSession
	if err := json.Unmarshal(payload, &session); err != nil || session.User.ID == 0 || session.User.Name == "" || session.ExpiresAt <= time.Now().Unix() {
		return authSession{}, errors.New("登录已过期，请重新登录")
	}
	return session, nil
}

func (a *AuthService) sign(value string) string {
	hash := hmac.New(sha256.New, a.secret)
	_, _ = hash.Write([]byte(value))
	return base64.RawURLEncoding.EncodeToString(hash.Sum(nil))
}

func RandomSessionSecret() string {
	bytes := make([]byte, 48)
	if _, err := rand.Read(bytes); err != nil {
		return "development-session-secret-must-be-overridden-in-production"
	}
	return base64.RawURLEncoding.EncodeToString(bytes)
}
