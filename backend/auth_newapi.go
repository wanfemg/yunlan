package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"time"
)

type ownerNewAPIClient struct {
	origin      *url.URL
	http        *http.Client
	accessToken string
	userID      int64
	modern      bool
}
type ownerNewAPIEnvelope struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
}
type ownerUpstreamResult struct {
	AccessToken         string         `json:"access_token"`
	ID                  int64          `json:"id"`
	User                *ownerAuthUser `json:"user"`
	RequireVerification bool           `json:"require_verification"`
	Require2FA          bool           `json:"require_2fa"`
	Need2FA             bool           `json:"need_2fa"`
	FlowToken           string         `json:"flow_token"`
	ExpiresAt           int64          `json:"expires_at"`
	Methods             []struct {
		Method    string `json:"method"`
		Available bool   `json:"available"`
	} `json:"methods"`
}

func newOwnerNewAPIClient(origin *url.URL) (*ownerNewAPIClient, error) {
	if origin == nil || origin.Host == "" || origin.User != nil ||
		origin.Scheme != "http" && origin.Scheme != "https" || origin.RawQuery != "" || origin.Fragment != "" {
		return nil, ownerFailure(503, "NewAPI 登录地址无效。")
	}
	copy := *origin
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 6 * time.Second, IdleConnTimeout: 45 * time.Second}
	return &ownerNewAPIClient{origin: &copy, http: &http.Client{Jar: jar, Transport: transport, Timeout: 7 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (client *ownerNewAPIClient) request(ctx context.Context, method, path string, query url.Values, value any) (ownerNewAPIEnvelope, int, error) {
	var envelope ownerNewAPIEnvelope
	target := *client.origin
	target.Path = path
	target.RawPath = ""
	target.RawQuery = query.Encode()
	var body []byte
	if value != nil {
		var err error
		body, err = json.Marshal(value)
		if err != nil {
			return envelope, 0, err
		}
		defer func() {
			for index := range body {
				body[index] = 0
			}
		}()
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(body))
	if err != nil {
		return envelope, 0, ownerFailure(503, "无法创建 NewAPI 登录请求。")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "SiteAdmin-OwnerLogin/1.0")
	if value != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet {
		// Refresh/logout use NewAPI's own, configured browser origin. This is a
		// server-to-server client with its own jar, never incoming browser cookies.
		request.Header.Set("Origin", client.origin.Scheme+"://"+client.origin.Host)
	}
	if client.accessToken != "" && path != "/api/user/auth/refresh" {
		request.Header.Set("Authorization", "Bearer "+client.accessToken)
	}
	if client.userID > 0 && path != "/api/user/auth/refresh" {
		request.Header.Set("New-Api-User", strconv.FormatInt(client.userID, 10))
	}
	response, err := client.http.Do(request)
	if err != nil {
		return envelope, 0, ownerFailure(503, "暂时无法连接 NewAPI 登录服务。")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusTooManyRequests {
		return envelope, response.StatusCode, ownerFailure(429, "NewAPI 登录服务请求过多，请稍后重试。")
	}
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusMethodNotAllowed {
		return envelope, response.StatusCode, nil
	}
	encoded, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(encoded) > 1<<20 || json.Unmarshal(encoded, &envelope) != nil {
		return envelope, response.StatusCode, ownerFailure(502, "NewAPI 返回了无法识别的登录响应。")
	}
	if response.StatusCode >= 500 {
		return envelope, response.StatusCode, ownerFailure(503, "NewAPI 登录服务暂时不可用。")
	}
	return envelope, response.StatusCode, nil
}

func ownerEncryptedPassword(password, keyID, publicPEM string) (string, error) {
	block, rest := pem.Decode([]byte(publicPEM))
	if block == nil || block.Type != "PUBLIC KEY" || len(bytes.TrimSpace(rest)) != 0 {
		return "", ownerFailure(502, "NewAPI 登录加密公钥无效。")
	}
	public, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return "", ownerFailure(502, "NewAPI 登录加密公钥无效。")
	}
	key, ok := public.(*rsa.PublicKey)
	if !ok || key.N.BitLen() < 2048 || key.N.BitLen() > 8192 || key.E < 3 {
		return "", ownerFailure(502, "NewAPI 登录加密公钥不受支持。")
	}
	plain := []byte(password)
	defer func() {
		for index := range plain {
			plain[index] = 0
		}
	}()
	if len(plain) <= key.Size()-2*sha256.Size-2 {
		encrypted, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, key, plain, nil)
		if err != nil {
			return "", ownerFailure(502, "无法完成 NewAPI 密码加密。")
		}
		return base64.StdEncoding.EncodeToString(encrypted), nil
	}
	// Upstream's v2 envelope also supports passwords longer than OAEP's direct
	// payload bound. Use the exact upstream labels and authenticated key ID.
	aesKey := make([]byte, 32)
	if _, err = rand.Read(aesKey); err != nil {
		return "", err
	}
	defer func() {
		for index := range aesKey {
			aesKey[index] = 0
		}
	}()
	wrapped, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, key, aesKey, []byte("password-v2"))
	if err != nil {
		return "", err
	}
	blockCipher, err := aes.NewCipher(aesKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(blockCipher)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nil, nonce, plain, []byte("password-v2:"+keyID))
	return "v2." + base64.StdEncoding.EncodeToString(wrapped) + "." + base64.StdEncoding.EncodeToString(nonce) + "." + base64.StdEncoding.EncodeToString(ciphertext), nil
}

func (client *ownerNewAPIClient) login(ctx context.Context, username, password, captcha string) (ownerAuthUser, *ownerAuthChallenge, error) {
	var user ownerAuthUser
	request := map[string]string{"username": username}
	encrypted, status, err := client.request(ctx, http.MethodGet, "/api/user/login/encryption-key", nil, nil)
	if err != nil {
		return user, nil, err
	}
	switch status {
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		request["password"] = password
	case http.StatusOK:
		var key struct {
			Enabled   *bool  `json:"enabled"`
			ID        string `json:"kid"`
			PublicKey string `json:"public_key"`
		}
		if !encrypted.Success || json.Unmarshal(encrypted.Data, &key) != nil || key.Enabled == nil {
			return user, nil, ownerFailure(502, "无法确认 NewAPI 的密码加密设置。")
		}
		if *key.Enabled {
			if key.ID == "" || len(key.ID) > 128 || len(key.PublicKey) > 16<<10 {
				return user, nil, ownerFailure(502, "NewAPI 登录加密参数无效。")
			}
			value, err := ownerEncryptedPassword(password, key.ID, key.PublicKey)
			if err != nil {
				return user, nil, err
			}
			request["password_encrypted"], request["encryption_key_id"] = value, key.ID
		} else {
			request["password"] = password
		}
	default:
		return user, nil, ownerFailure(503, "暂时无法读取 NewAPI 登录加密设置。")
	}
	query := url.Values{}
	if captcha != "" {
		query.Set("turnstile", captcha)
	}
	envelope, status, err := client.request(ctx, http.MethodPost, "/api/user/login", query, request)
	delete(request, "password")
	delete(request, "password_encrypted")
	if err != nil {
		return user, nil, err
	}
	if status != 200 || !envelope.Success {
		message := "账号或密码不正确，或该账号暂不可登录。"
		if captcha != "" {
			message = "账号密码或站点验证码未通过，请重新完成验证后重试。"
		}
		return user, nil, ownerFailure(401, message)
	}
	return client.finishLogin(ctx, envelope.Data, true)
}

func (client *ownerNewAPIClient) finishLogin(ctx context.Context, data json.RawMessage, allowChallenge bool) (ownerAuthUser, *ownerAuthChallenge, error) {
	var user ownerAuthUser
	var result ownerUpstreamResult
	if json.Unmarshal(data, &result) != nil {
		return user, nil, ownerFailure(502, "NewAPI 返回了无法识别的登录结果。")
	}
	if result.RequireVerification || result.Require2FA || result.Need2FA {
		if !allowChallenge {
			return user, nil, ownerFailure(401, "二次验证未完成，请重新登录。")
		}
		supported := !result.RequireVerification
		for _, method := range result.Methods {
			if method.Method == "2fa" && method.Available {
				supported = true
			}
		}
		if !supported {
			return user, nil, ownerFailure(400, "该账号仅允许 Passkey 等当前入口不支持的验证方式；请在 NewAPI 原站配置动态验证码后登录。")
		}
		if result.RequireVerification && (result.FlowToken == "" || len(result.FlowToken) > 4096) {
			return user, nil, ownerFailure(502, "NewAPI 二次验证挑战无效。")
		}
		id, err := ownerRandomToken()
		if err != nil {
			return user, nil, err
		}
		expires := time.Now().Add(5 * time.Minute)
		if result.ExpiresAt > 0 && time.Unix(result.ExpiresAt, 0).Before(expires) {
			expires = time.Unix(result.ExpiresAt, 0)
		}
		if !time.Now().Before(expires) {
			return user, nil, ownerFailure(401, "NewAPI 二次验证已过期，请重新登录。")
		}
		return user, &ownerAuthChallenge{ID: id, FlowToken: result.FlowToken, Legacy: !result.RequireVerification, ExpiresAt: expires, Methods: []string{"2fa"}}, nil
	}
	if result.AccessToken != "" {
		if result.User == nil || result.User.ID <= 0 || len(result.AccessToken) > 16<<10 {
			return user, nil, ownerFailure(502, "NewAPI 未提供完整的站长会话。")
		}
		client.accessToken = result.AccessToken
		client.userID = result.User.ID
		client.modern = true
	} else {
		client.userID = result.ID
		if result.User != nil {
			client.userID = result.User.ID
		}
		if client.userID <= 0 {
			return user, nil, ownerFailure(502, "NewAPI 未确认已登录账号。")
		}
	}
	user, err := client.currentUser(ctx)
	if err != nil {
		client.logout(ctx)
		return user, nil, err
	}
	return user, nil, nil
}
func (client *ownerNewAPIClient) verify(ctx context.Context, challenge *ownerAuthChallenge, code string) (ownerAuthUser, error) {
	path := "/api/user/login/verify"
	body := map[string]string{"flow_token": challenge.FlowToken, "method": "2fa", "code": code}
	if challenge.Legacy {
		path = "/api/user/login/2fa"
		body = map[string]string{"code": code}
	}
	envelope, status, err := client.request(ctx, http.MethodPost, path, nil, body)
	delete(body, "code")
	if err != nil {
		return ownerAuthUser{}, err
	}
	if status != 200 || !envelope.Success {
		return ownerAuthUser{}, ownerFailure(401, "动态验证码或备用恢复码未通过。")
	}
	user, _, err := client.finishLogin(ctx, envelope.Data, false)
	return user, err
}
func (client *ownerNewAPIClient) currentUser(ctx context.Context) (ownerAuthUser, error) {
	if client == nil || client.userID <= 0 {
		return ownerAuthUser{}, ownerFailure(401, "站长会话已失效，请重新登录。")
	}
	envelope, status, err := client.request(ctx, http.MethodGet, "/api/user/self", nil, nil)
	if err != nil {
		return ownerAuthUser{}, err
	}
	if (!envelope.Success || status == 401) && client.modern {
		refreshed, refreshStatus, refreshErr := client.request(ctx, http.MethodPost, "/api/user/auth/refresh", nil, map[string]string{})
		if refreshErr != nil {
			return ownerAuthUser{}, refreshErr
		}
		if refreshStatus == 200 && refreshed.Success {
			var result ownerUpstreamResult
			if json.Unmarshal(refreshed.Data, &result) != nil || result.AccessToken == "" ||
				len(result.AccessToken) > 16<<10 || result.User == nil || result.User.ID != client.userID {
				return ownerAuthUser{}, ownerFailure(401, "NewAPI 站长会话无法刷新，请重新登录。")
			}
			client.accessToken = result.AccessToken
			envelope, status, err = client.request(ctx, http.MethodGet, "/api/user/self", nil, nil)
			if err != nil {
				return ownerAuthUser{}, err
			}
		} else {
			return ownerAuthUser{}, ownerFailure(401, "NewAPI 站长会话已失效，请重新登录。")
		}
	}
	if status != 200 || !envelope.Success {
		return ownerAuthUser{}, ownerFailure(401, "NewAPI 站长会话已失效，请重新登录。")
	}
	var user ownerAuthUser
	if json.Unmarshal(envelope.Data, &user) != nil || user.ID != client.userID || user.ID <= 0 || user.Username == "" || len(user.Username) > 128 {
		return user, ownerFailure(502, "NewAPI 未确认当前站长身份。")
	}
	if user.Role < 100 || user.Status != 1 {
		return user, ownerFailure(403, "仅启用状态的 NewAPI 站长账号可登录此后台。")
	}
	return user, nil
}
func (client *ownerNewAPIClient) logout(ctx context.Context) {
	if client == nil {
		return
	}
	if client.modern {
		_, _, _ = client.request(ctx, http.MethodPost, "/api/user/auth/logout", nil, map[string]string{})
	} else {
		_, _, _ = client.request(ctx, http.MethodGet, "/api/user/logout", nil, nil)
	}
	client.accessToken = ""
	if transport, ok := client.http.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
}

type ownerCaptchaStatus struct {
	Ready    bool
	Required bool
	SiteKey  string
	Error    string
}

func (a *ownerAuthStore) captchaStatus(ctx context.Context) ownerCaptchaStatus {
	if a.service.config.Mode == "demo" {
		return ownerCaptchaStatus{Ready: true}
	}
	if a.service.config.NewAPIUpstream == nil {
		return ownerCaptchaStatus{Error: "尚未配置 NewAPI 登录地址。"}
	}
	a.captchaMu.Lock()
	defer a.captchaMu.Unlock()
	ttl := 30 * time.Second
	if !a.captcha.Ready {
		ttl = 5 * time.Second
	}
	if !a.captchaAt.IsZero() && time.Since(a.captchaAt) < ttl {
		return a.captcha
	}
	a.captchaAt = time.Now()
	result := ownerCaptchaStatus{Error: "暂时无法读取 NewAPI 的站点验证设置，请稍后刷新登录页面。"}
	client, err := newOwnerNewAPIClient(a.service.config.NewAPIUpstream)
	if err != nil {
		a.captcha = result
		return result
	}
	defer client.logoutTransport()
	limited, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	envelope, status, err := client.request(limited, http.MethodGet, "/api/status", nil, nil)
	if err == nil && status == 200 && envelope.Success {
		var settings struct {
			Required *bool  `json:"turnstile_check"`
			SiteKey  string `json:"turnstile_site_key"`
		}
		if json.Unmarshal(envelope.Data, &settings) == nil && settings.Required != nil &&
			(!*settings.Required || settings.SiteKey != "" && len(settings.SiteKey) < 512) {
			result = ownerCaptchaStatus{Ready: true, Required: *settings.Required, SiteKey: settings.SiteKey}
		}
	}
	a.captcha = result
	return result
}
func (client *ownerNewAPIClient) logoutTransport() {
	if client != nil {
		if transport, ok := client.http.Transport.(*http.Transport); ok {
			transport.CloseIdleConnections()
		}
	}
}
