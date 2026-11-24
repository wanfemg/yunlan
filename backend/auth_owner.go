package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

const (
	ownerCookieName        = "siteadmin_owner"
	ownerSessionContext    = "siteadmin.owner_session"
	ownerSessionKeyContext = "siteadmin.owner_session_key"
	ownerSessionTTL        = 8 * time.Hour
	ownerSessionIdle       = 30 * time.Minute
	ownerAnonymousTTL      = 10 * time.Minute
	ownerCheckInterval     = 30 * time.Second
)

type ownerAuthUser struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Role        int    `json:"role"`
	Status      int    `json:"status"`
}

type ownerAuthSession struct {
	mu                                        sync.Mutex
	User                                      *ownerAuthUser
	CSRF                                      string
	CreatedAt, ExpiresAt, LastSeen, CheckedAt time.Time
	Upstream                                  *ownerNewAPIClient
	Challenge                                 *ownerAuthChallenge
}

type ownerAuthChallenge struct {
	ID        string
	FlowToken string
	Legacy    bool
	ExpiresAt time.Time
	Attempts  int
	Methods   []string
}

type ownerAuthRate struct {
	Started  time.Time
	Attempts int
}

type ownerAuthStore struct {
	service    *dashboardService
	mu         sync.Mutex
	sessions   map[string]*ownerAuthSession
	rates      map[string]ownerAuthRate
	secureMode string
	captchaMu  sync.Mutex
	captchaAt  time.Time
	captcha    ownerCaptchaStatus
}

type ownerAuthFailure struct {
	status  int
	message string
}

func (e *ownerAuthFailure) Error() string { return e.message }
func ownerFailure(status int, message string) error {
	return &ownerAuthFailure{status: status, message: message}
}
func ownerHTTPError(c *gin.Context, err error) {
	var failure *ownerAuthFailure
	if errors.As(err, &failure) {
		c.JSON(failure.status, gin.H{"success": false, "error": failure.message})
		return
	}
	c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "error": "登录服务暂时不可用，请稍后重试。"})
}

func newOwnerAuth(service *dashboardService) *ownerAuthStore {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("SITEADMIN_COOKIE_SECURE")))
	if mode != "true" && mode != "false" {
		mode = "auto"
	}
	return &ownerAuthStore{service: service, sessions: map[string]*ownerAuthSession{}, rates: map[string]ownerAuthRate{}, secureMode: mode}
}

func ownerRandomToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
func ownerTokenKey(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
func ownerEqual(left, right string) bool {
	a, b := sha256.Sum256([]byte(left)), sha256.Sum256([]byte(right))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}
func (a *ownerAuthStore) cookieSecure(c *gin.Context) bool {
	if a.secureMode == "true" {
		return true
	}
	if a.secureMode == "false" {
		return false
	}
	if c.Request.TLS != nil {
		return true
	}
	// Only the loopback reverse proxy may describe the outer HTTPS transport.
	host, _, err := net.SplitHostPort(c.Request.RemoteAddr)
	ip := net.ParseIP(host)
	return err == nil && ip != nil && ip.IsLoopback() && c.GetHeader("X-Forwarded-Proto") == "https"
}
func (a *ownerAuthStore) writeCookie(c *gin.Context, token string, expires time.Time) {
	http.SetCookie(c.Writer, &http.Cookie{Name: ownerCookieName, Value: token, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: a.cookieSecure(c), Expires: expires, MaxAge: int(time.Until(expires).Seconds())})
}
func (a *ownerAuthStore) clearCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{Name: ownerCookieName, Value: "", Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: a.cookieSecure(c), MaxAge: -1, Expires: time.Unix(1, 0)})
}
func (a *ownerAuthStore) pruneLocked(now time.Time) {
	for key, session := range a.sessions {
		if session.mu.TryLock() {
			expired := !now.Before(session.ExpiresAt) || session.User != nil && now.Sub(session.LastSeen) > ownerSessionIdle
			session.mu.Unlock()
			if expired {
				delete(a.sessions, key)
			}
		}
	}
	for key, rate := range a.rates {
		if now.Sub(rate.Started) > 15*time.Minute {
			delete(a.rates, key)
		}
	}
}
func (a *ownerAuthStore) newAnonymous(c *gin.Context) (*ownerAuthSession, string, error) {
	token, err := ownerRandomToken()
	if err != nil {
		return nil, "", err
	}
	csrf, err := ownerRandomToken()
	if err != nil {
		return nil, "", err
	}
	now := time.Now()
	session := &ownerAuthSession{CSRF: csrf, CreatedAt: now, ExpiresAt: now.Add(ownerAnonymousTTL), LastSeen: now}
	key := ownerTokenKey(token)
	a.mu.Lock()
	a.pruneLocked(now)
	if len(a.sessions) >= 4096 {
		a.mu.Unlock()
		return nil, "", ownerFailure(429, "登录请求过多，请稍后重试。")
	}
	a.sessions[key] = session
	a.mu.Unlock()
	a.writeCookie(c, token, session.ExpiresAt)
	return session, key, nil
}
func (a *ownerAuthStore) lookup(c *gin.Context) (*ownerAuthSession, string) {
	cookie, err := c.Cookie(ownerCookieName)
	if err != nil || len(cookie) != 43 {
		return nil, ""
	}
	if _, err = base64.RawURLEncoding.DecodeString(cookie); err != nil {
		return nil, ""
	}
	key := ownerTokenKey(cookie)
	a.mu.Lock()
	session := a.sessions[key]
	a.mu.Unlock()
	if session == nil {
		return nil, ""
	}
	now := time.Now()
	session.mu.Lock()
	expired := !now.Before(session.ExpiresAt) || session.User != nil && now.Sub(session.LastSeen) > ownerSessionIdle
	session.mu.Unlock()
	if expired {
		a.remove(key)
		a.clearCookie(c)
		return nil, ""
	}
	return session, key
}
func (a *ownerAuthStore) remove(key string) { a.mu.Lock(); delete(a.sessions, key); a.mu.Unlock() }
func (a *ownerAuthStore) rateAllowed(c *gin.Context) bool {
	host, _, err := net.SplitHostPort(c.Request.RemoteAddr)
	if err != nil {
		host = c.Request.RemoteAddr
	}
	if host == "" {
		host = "unknown"
	}
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.rates) > 4096 {
		a.pruneLocked(now)
	}
	rate := a.rates[host]
	if rate.Started.IsZero() || now.Sub(rate.Started) >= 10*time.Minute {
		rate = ownerAuthRate{Started: now}
	}
	if rate.Attempts >= 10 {
		c.Header("Retry-After", "600")
		return false
	}
	if len(a.rates) >= 8192 && rate.Attempts == 0 {
		return false
	}
	rate.Attempts++
	a.rates[host] = rate
	return true
}
func ownerCSRFMatches(c *gin.Context, session *ownerAuthSession) bool {
	if strings.EqualFold(c.GetHeader("Sec-Fetch-Site"), "cross-site") {
		return false
	}
	token := c.GetHeader("X-CSRF-Token")
	return len(token) >= 32 && len(token) <= 128 && ownerEqual(token, session.CSRF)
}

func ownerAuthMiddleware(service *dashboardService) gin.HandlerFunc {
	if service.ownerAuth == nil {
		service.ownerAuth = newOwnerAuth(service)
	}
	auth := service.ownerAuth
	return func(c *gin.Context) {
		if trusted, _ := c.Request.Context().Value(internalTaskAuthKey{}).(bool); trusted {
			c.Set("siteadmin.owner_authenticated", true)
			c.Next()
			return
		}
		if !strings.HasPrefix(c.Request.URL.Path, "/api/") && !strings.HasPrefix(c.Request.URL.Path, financeBasePath) {
			c.Next()
			return
		}
		// Machine API callers retain the existing, explicitly configured key.
		if service.config.AdminToken != "" && adminTokenValidator(service.config.AdminToken)(c.GetHeader("Authorization")) {
			c.Next()
			return
		}
		session, key := auth.lookup(c)
		if session == nil {
			c.Next()
			return
		}
		c.Set(ownerSessionContext, session)
		c.Set(ownerSessionKeyContext, key)
		path := c.Request.URL.Path
		if path == "/api/health" || path == "/api/meta" || path == "/api/auth/login" || path == "/api/auth/verify" || path == "/api/auth/logout" {
			c.Next()
			return
		}
		session.mu.Lock()
		if session.User == nil {
			session.mu.Unlock()
			c.Next()
			return
		}
		if service.config.Mode != "demo" && time.Since(session.CheckedAt) >= ownerCheckInterval {
			ctx, cancel := context.WithTimeout(c.Request.Context(), 6*time.Second)
			user, err := session.Upstream.currentUser(ctx)
			cancel()
			if err != nil {
				session.mu.Unlock()
				var failure *ownerAuthFailure
				if errors.As(err, &failure) && (failure.status == 401 || failure.status == 403) {
					auth.remove(key)
					auth.clearCookie(c)
					c.Next()
					return
				}
				c.AbortWithStatusJSON(503, gin.H{"success": false, "error": "暂时无法验证 NewAPI 站长会话，请稍后重试。"})
				return
			}
			session.User = &user
			session.CheckedAt = time.Now()
		}
		mutating := c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead && c.Request.Method != http.MethodOptions
		if mutating && !ownerCSRFMatches(c, session) {
			session.mu.Unlock()
			c.AbortWithStatusJSON(403, gin.H{"success": false, "error": "会话校验失败，请刷新页面后重试。"})
			return
		}
		user := *session.User
		session.LastSeen = time.Now()
		session.mu.Unlock()
		c.Set("siteadmin.owner_authenticated", true)
		c.Set("siteadmin.owner_user", user)
		c.Next()
	}
}

func registerOwnerAuthRoutes(router *gin.Engine, service *dashboardService) {
	if service.ownerAuth == nil {
		service.ownerAuth = newOwnerAuth(service)
	}
	group := router.Group("/api/auth")
	group.GET("/session", service.ownerAuth.sessionHTTP)
	group.POST("/login", service.ownerAuth.loginHTTP)
	group.POST("/verify", service.ownerAuth.verifyHTTP)
	group.POST("/logout", service.ownerAuth.logoutHTTP)
}
func (a *ownerAuthStore) viewLocked(session *ownerAuthSession) gin.H {
	source := "newapi"
	if a.service.config.Mode == "demo" {
		source = "demo"
	}
	data := gin.H{"authenticated": session.User != nil, "csrf_token": session.CSRF, "mode": a.service.config.Mode,
		"login_source": source, "login_configured": a.service.config.Mode == "demo" || a.service.config.NewAPIUpstream != nil}
	a.captchaMu.Lock()
	captcha := a.captcha
	a.captchaMu.Unlock()
	if source == "demo" {
		captcha = ownerCaptchaStatus{Ready: true}
	}
	data["login_ready"] = captcha.Ready
	data["captcha_required"] = captcha.Required
	data["captcha_site_key"] = captcha.SiteKey
	if captcha.Error != "" {
		data["message"] = captcha.Error
	}
	if session.User != nil {
		data["user"] = *session.User
	}
	if session.Challenge != nil && time.Now().Before(session.Challenge.ExpiresAt) {
		data["requires_verification"] = true
		data["challenge_id"] = session.Challenge.ID
		data["methods"] = session.Challenge.Methods
		data["message"] = "请输入 NewAPI 的动态验证码或备用恢复码。"
	}
	return data
}
func (a *ownerAuthStore) sessionHTTP(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	_ = a.captchaStatus(c.Request.Context())
	session, _ := a.lookup(c)
	if session == nil {
		var err error
		session, _, err = a.newAnonymous(c)
		if err != nil {
			ownerHTTPError(c, err)
			return
		}
	}
	session.mu.Lock()
	data := a.viewLocked(session)
	session.mu.Unlock()
	c.JSON(200, gin.H{"success": true, "data": data})
}
func ownerDecode(c *gin.Context, value any) error {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 12<<10)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil {
		return ownerFailure(400, "登录信息格式无效。")
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return ownerFailure(400, "登录信息格式无效。")
	}
	return nil
}
func (a *ownerAuthStore) formSession(c *gin.Context) (*ownerAuthSession, string, error) {
	session, key := a.lookup(c)
	if session == nil {
		return nil, "", ownerFailure(401, "登录页面已过期，请刷新后重试。")
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if !ownerCSRFMatches(c, session) {
		return nil, "", ownerFailure(403, "会话校验失败，请刷新登录页面。")
	}
	return session, key, nil
}
func (a *ownerAuthStore) completeLocked(c *gin.Context, session *ownerAuthSession, oldKey string, user ownerAuthUser) error {
	if user.ID <= 0 || user.Role < 100 || user.Status != 1 {
		return ownerFailure(403, "仅启用状态的 NewAPI 站长账号可登录此后台。")
	}
	token, err := ownerRandomToken()
	if err != nil {
		return err
	}
	csrf, err := ownerRandomToken()
	if err != nil {
		return err
	}
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sessions[oldKey] != session {
		return ownerFailure(401, "登录会话已退出或过期，请重新打开登录页面。")
	}
	session.User = &user
	session.CSRF = csrf
	session.CreatedAt = now
	session.LastSeen = now
	session.CheckedAt = now
	session.ExpiresAt = now.Add(ownerSessionTTL)
	session.Challenge = nil
	newKey := ownerTokenKey(token)
	delete(a.sessions, oldKey)
	a.sessions[newKey] = session
	a.writeCookie(c, token, session.ExpiresAt)
	return nil
}
func (a *ownerAuthStore) loginHTTP(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	session, key, err := a.formSession(c)
	if err != nil {
		ownerHTTPError(c, err)
		return
	}
	if !a.rateAllowed(c) {
		ownerHTTPError(c, ownerFailure(429, "登录尝试过多，请十分钟后重试。"))
		return
	}
	var request struct {
		Username     string `json:"username"`
		Password     string `json:"password"`
		CaptchaToken string `json:"captcha_token"`
	}
	if err = ownerDecode(c, &request); err != nil {
		ownerHTTPError(c, err)
		return
	}
	request.Username = strings.TrimSpace(request.Username)
	if request.Username == "" || len(request.Username) > 128 || len(request.Password) == 0 || len(request.Password) > 4096 ||
		len(request.CaptchaToken) > 4096 ||
		!utf8.ValidString(request.Username) || !utf8.ValidString(request.Password) || strings.ContainsRune(request.Username, 0) || strings.ContainsRune(request.Password, 0) {
		ownerHTTPError(c, ownerFailure(400, "请输入有效账号和密码。"))
		return
	}
	captcha := a.captchaStatus(c.Request.Context())
	if !captcha.Ready {
		ownerHTTPError(c, ownerFailure(503, captcha.Error))
		return
	}
	if captcha.Required && strings.TrimSpace(request.CaptchaToken) == "" {
		ownerHTTPError(c, ownerFailure(400, "请先完成站点验证码；验证码不可用时需将站长后台域名加入验证码允许域名。"))
		return
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.User != nil {
		ownerHTTPError(c, ownerFailure(409, "当前已登录，请先退出后切换账号。"))
		return
	}
	session.Challenge = nil
	session.Upstream = nil
	if a.service.config.Mode == "demo" {
		if !ownerEqual(request.Username, "root") || !ownerEqual(request.Password, "123456") {
			ownerHTTPError(c, ownerFailure(401, "账号或密码不正确。"))
			return
		}
		err = a.completeLocked(c, session, key, ownerAuthUser{ID: 1, Username: "root", DisplayName: "本地演示站长", Role: 100, Status: 1})
		if err != nil {
			ownerHTTPError(c, err)
			return
		}
		c.JSON(200, gin.H{"success": true, "data": a.viewLocked(session)})
		return
	}
	if a.service.config.NewAPIUpstream == nil {
		ownerHTTPError(c, ownerFailure(503, "尚未配置 NewAPI 登录地址，请先在部署配置中填写 NEWAPI_UPSTREAM。"))
		return
	}
	client, err := newOwnerNewAPIClient(a.service.config.NewAPIUpstream)
	if err != nil {
		ownerHTTPError(c, err)
		return
	}
	defer client.logoutTransport()
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	user, challenge, err := client.login(ctx, request.Username, request.Password, request.CaptchaToken)
	request.Password = ""
	if err != nil {
		ownerHTTPError(c, err)
		return
	}
	session.Upstream = client
	if challenge != nil {
		session.Challenge = challenge
		c.JSON(200, gin.H{"success": true, "data": a.viewLocked(session)})
		return
	}
	if err = a.completeLocked(c, session, key, user); err != nil {
		client.logout(ctx)
		session.Upstream = nil
		ownerHTTPError(c, err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": a.viewLocked(session)})
}
func (a *ownerAuthStore) verifyHTTP(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	session, key, err := a.formSession(c)
	if err != nil {
		ownerHTTPError(c, err)
		return
	}
	if !a.rateAllowed(c) {
		ownerHTTPError(c, ownerFailure(429, "验证尝试过多，请十分钟后重试。"))
		return
	}
	var request struct {
		ChallengeID string `json:"challenge_id"`
		Method      string `json:"method"`
		Code        string `json:"code"`
	}
	if err = ownerDecode(c, &request); err != nil {
		ownerHTTPError(c, err)
		return
	}
	request.Code = strings.TrimSpace(request.Code)
	if request.Method != "2fa" || len(request.Code) == 0 || len(request.Code) > 128 || !utf8.ValidString(request.Code) {
		ownerHTTPError(c, ownerFailure(400, "请输入动态验证码或备用恢复码。"))
		return
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	challenge := session.Challenge
	if challenge == nil || session.Upstream == nil || !ownerEqual(request.ChallengeID, challenge.ID) || !time.Now().Before(challenge.ExpiresAt) || challenge.Attempts >= 10 {
		session.Challenge = nil
		ownerHTTPError(c, ownerFailure(401, "二次验证已过期，请重新输入账号密码。"))
		return
	}
	challenge.Attempts++
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	user, err := session.Upstream.verify(ctx, challenge, request.Code)
	request.Code = ""
	if err != nil {
		ownerHTTPError(c, err)
		return
	}
	if err = a.completeLocked(c, session, key, user); err != nil {
		session.Upstream.logout(ctx)
		session.Upstream = nil
		session.Challenge = nil
		ownerHTTPError(c, err)
		return
	}
	c.JSON(200, gin.H{"success": true, "data": a.viewLocked(session)})
}
func (a *ownerAuthStore) logoutHTTP(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	session, key, err := a.formSession(c)
	if err != nil {
		ownerHTTPError(c, err)
		return
	}
	a.remove(key)
	a.clearCookie(c)
	session.mu.Lock()
	client := session.Upstream
	session.User = nil
	session.Challenge = nil
	session.Upstream = nil
	session.mu.Unlock()
	if client != nil {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
		client.logout(ctx)
		cancel()
	}
	c.JSON(200, gin.H{"success": true, "data": gin.H{"authenticated": false}})
}
