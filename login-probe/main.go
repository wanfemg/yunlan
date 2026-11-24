package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

//go:embed probe.js
var browserScript []byte

const probePrefix = "/_siteadmin_probe/"
const browserCookie = "__siteadmin_browser"

type probeConfig struct {
	Listen, Secret, DataDir string
	Upstream, Collector     *url.URL
	TrustedProxies          []netip.Prefix
}

type authEvent struct {
	ID             string            `json:"id"`
	Source         string            `json:"source"`
	OccurredAt     int64             `json:"occurred_at"`
	UserID         int64             `json:"user_id"`
	TokenID        int64             `json:"token_id"`
	IP             string            `json:"ip"`
	PeerIP         string            `json:"peer_ip"`
	Result         string            `json:"result"`
	BrowserID      string            `json:"browser_id"`
	SessionID      string            `json:"session_id"`
	Method         string            `json:"method"`
	ClaimedAccount string            `json:"claimed_account"`
	UserAgent      string            `json:"user_agent"`
	Device         map[string]string `json:"device"`
	Metadata       map[string]string `json:"metadata"`
}

type observation struct {
	Source, Method, BrowserID, Cookie, ClaimedAccount string
	IP, PeerIP, IPSource                              string
	Device                                            map[string]string
}

type observationKey struct{}

type deviceRecord struct {
	Values map[string]string
	At     time.Time
}

type loginProbe struct {
	config  probeConfig
	proxy   *httputil.ReverseProxy
	client  *http.Client
	mu      sync.Mutex
	devices map[string]deviceRecord
	now     func() time.Time
}

func parseOrigin(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("an HTTP(S) origin without credentials or path is required")
	}
	u.Path = ""
	return u, nil
}

func loadProbeConfig(getenv func(string) string) (probeConfig, error) {
	c := probeConfig{Listen: strings.TrimSpace(getenv("PROBE_LISTEN")), Secret: strings.TrimSpace(getenv("PROBE_SHARED_SECRET")),
		DataDir: strings.TrimSpace(getenv("PROBE_DATA_DIR"))}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:18081"
	}
	if c.DataDir == "" {
		c.DataDir = "./data/login-probe"
	}
	if len(c.Secret) < 32 || len(c.Secret) > 128 || strings.ContainsAny(c.Secret, "\r\n \t") {
		return c, errors.New("PROBE_SHARED_SECRET must contain 32–128 URL-safe characters")
	}
	for _, ch := range c.Secret {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_') {
			return c, errors.New("PROBE_SHARED_SECRET must be URL-safe")
		}
	}
	var err error
	c.Upstream, err = parseOrigin(strings.TrimSpace(getenv("NEWAPI_UPSTREAM")))
	if err != nil {
		return c, fmt.Errorf("NEWAPI_UPSTREAM: %w", err)
	}
	c.Collector, err = parseOrigin(strings.TrimSpace(getenv("SITEADMIN_ORIGIN")))
	if err != nil {
		return c, fmt.Errorf("SITEADMIN_ORIGIN: %w", err)
	}
	for _, raw := range strings.Split(getenv("PROBE_TRUSTED_PROXIES"), ",") {
		if raw = strings.TrimSpace(raw); raw == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return c, errors.New("PROBE_TRUSTED_PROXIES must contain comma-separated CIDRs")
		}
		c.TrustedProxies = append(c.TrustedProxies, prefix.Masked())
	}
	return c, nil
}

// A saved upstream uses the same precedence as site-admin. Other connection
// fields, especially database credentials, are never part of probe config.
func loadProbeStartupConfig() (probeConfig, error) {
	settings := strings.TrimSpace(os.Getenv("SITEADMIN_CONFIG_FILE"))
	if settings == "" {
		return loadProbeConfig(os.Getenv)
	}
	file, err := os.Open(settings)
	if errors.Is(err, os.ErrNotExist) {
		return loadProbeConfig(os.Getenv)
	}
	if err != nil {
		return probeConfig{}, errors.New("cannot read saved site-admin settings")
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 49153))
	var document struct {
		Values map[string]string `json:"values"`
	}
	if err != nil || len(raw) > 49152 || json.Unmarshal(raw, &document) != nil || document.Values == nil {
		return probeConfig{}, errors.New("invalid saved site-admin settings")
	}
	return loadProbeConfig(func(key string) string {
		if key == "NEWAPI_UPSTREAM" {
			if value, found := document.Values[key]; found {
				return value
			}
		}
		return os.Getenv(key)
	})
}

func newLoginProbe(c probeConfig) (*loginProbe, error) {
	if c.Upstream == nil || c.Collector == nil {
		return nil, errors.New("probe upstream and collector are required")
	}
	if err := os.MkdirAll(c.DataDir, 0700); err != nil {
		return nil, errors.New("cannot create probe spool")
	}
	p := &loginProbe{config: c, devices: make(map[string]deviceRecord), now: time.Now,
		client: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}}
	p.proxy = &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(c.Upstream)
			r.Out.Host = r.In.Host
			r.SetXForwarded()
			ip, _, _ := p.sourceIP(r.In)
			r.Out.Header.Set("X-Forwarded-For", ip)
			r.Out.Header.Set("X-Real-IP", ip)
			_, peer, _ := p.sourceIP(r.In)
			if address, err := netip.ParseAddr(peer); err == nil && p.trusted(address) {
				if proto := r.In.Header.Get("X-Forwarded-Proto"); proto == "http" || proto == "https" {
					r.Out.Header.Set("X-Forwarded-Proto", proto)
				}
			}
		},
		ModifyResponse: p.observeResponse,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "NewAPI upstream is unavailable", http.StatusBadGateway)
		},
	}
	return p, nil
}

func (p *loginProbe) trusted(ip netip.Addr) bool {
	for _, prefix := range p.config.TrustedProxies {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

func (p *loginProbe) sourceIP(r *http.Request) (string, string, string) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return "", "", "unknown"
	}
	peer = peer.Unmap()
	if !p.trusted(peer) {
		return peer.String(), peer.String(), "socket"
	}
	chain := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	current := peer
	for i := len(chain) - 1; i >= 0 && p.trusted(current); i-- {
		next, err := netip.ParseAddr(strings.TrimSpace(chain[i]))
		if err != nil {
			return peer.String(), peer.String(), "invalid_forwarded_chain"
		}
		current = next.Unmap()
	}
	if current != peer {
		return current.String(), peer.String(), "trusted_proxy"
	}
	return peer.String(), peer.String(), "socket"
}

func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func (p *loginProbe) cookieSignature(id string) string {
	mac := hmac.New(sha256.New, []byte(p.config.Secret))
	_, _ = mac.Write([]byte("browser:" + id))
	return hex.EncodeToString(mac.Sum(nil))
}

func (p *loginProbe) browserID(r *http.Request) (string, string, error) {
	if cookie, err := r.Cookie(browserCookie); err == nil {
		parts := strings.Split(cookie.Value, ".")
		if len(parts) == 2 && len(parts[0]) == 32 {
			if _, err := hex.DecodeString(parts[0]); err == nil && hmac.Equal([]byte(parts[1]), []byte(p.cookieSignature(parts[0]))) {
				return parts[0], "", nil
			}
		}
	}
	id, err := randomID()
	if err != nil {
		return "", "", err
	}
	secure := r.TLS != nil
	_, peer, _ := p.sourceIP(r)
	if address, err := netip.ParseAddr(peer); err == nil && p.trusted(address) && r.Header.Get("X-Forwarded-Proto") == "https" {
		secure = true
	}
	return id, (&http.Cookie{Name: browserCookie, Value: id + "." + p.cookieSignature(id), Path: "/",
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: 180 * 86400}).String(), nil
}

func authRoute(method, path string) (string, string) {
	if method == http.MethodPost {
		switch path {
		case "/api/user/register":
			return "registration", "password"
		case "/api/user/login":
			return "login", "password"
		case "/api/user/login/2fa":
			return "login", "2fa"
		case "/api/user/login/verify":
			return "login", "verification"
		case "/api/user/login/passkey/finish", "/api/user/passkey/login/finish":
			return "login", "passkey"
		}
	}
	if method == http.MethodGet && path == "/api/oauth/telegram/login" {
		return "login", "oauth:telegram"
	}
	if method == http.MethodGet && strings.HasPrefix(path, "/api/oauth/") {
		provider := strings.TrimPrefix(path, "/api/oauth/")
		valid := provider != "" && len(provider) <= 40 && provider != "state" && provider != "email"
		for _, ch := range provider {
			valid = valid && (ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' ||
				ch >= '0' && ch <= '9' || ch == '_' || ch == '-')
		}
		if valid {
			return "login", "oauth:" + provider
		}
	}
	return "", ""
}

func (p *loginProbe) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == probePrefix+"healthz" && r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(`{"status":"ok","service":"login-probe"}`))
		return
	}
	if r.URL.Path == probePrefix+"readyz" {
		p.readiness(w, r)
		return
	}
	if r.URL.Path == probePrefix+"agent.js" && r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(browserScript)
		return
	}
	if r.URL.Path == probePrefix+"device" {
		p.captureDevice(w, r)
		return
	}
	source, method := authRoute(r.Method, r.URL.Path)
	isHTML := r.Method == http.MethodGet && !strings.HasPrefix(r.URL.Path, "/api/") &&
		!strings.HasPrefix(r.URL.Path, "/v1/") && strings.Contains(r.Header.Get("Accept"), "text/html")
	if source == "" && !isHTML {
		p.proxy.ServeHTTP(w, r)
		return
	}
	id, cookie, err := p.browserID(r)
	if err != nil {
		http.Error(w, "probe browser identity unavailable", http.StatusServiceUnavailable)
		return
	}
	ip, peer, ipSource := p.sourceIP(r)
	p.mu.Lock()
	record := p.devices[id]
	p.mu.Unlock()
	o := observation{Source: source, Method: method, BrowserID: id, Cookie: cookie, IP: ip, PeerIP: peer, IPSource: ipSource}
	if p.now().Sub(record.At) < 24*time.Hour {
		o.Device = record.Values
	}
	if source != "" && r.Method == http.MethodPost && strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		original := r.Body
		body, err := io.ReadAll(io.LimitReader(original, (1<<20)+1))
		if err != nil || len(body) > 1<<20 {
			r.Body = &joinedBody{Reader: io.MultiReader(bytes.NewReader(body), original), Closer: original}
			// Large or unrecognized bodies pass through without collecting credentials.
		} else {
			_ = original.Close()
			r.Body = io.NopCloser(bytes.NewReader(body))
			var request struct {
				Username string `json:"username"`
			}
			if json.Unmarshal(body, &request) == nil && len(request.Username) <= 128 {
				o.ClaimedAccount = request.Username
				if source == "registration" {
					o.ClaimedAccount = strings.TrimSpace(o.ClaimedAccount)
				}
			}
		}
	}
	// Authentication bodies need inspection; other responses retain compression.
	if source != "" || isHTML {
		r.Header.Del("Accept-Encoding")
	}
	r = r.WithContext(context.WithValue(r.Context(), observationKey{}, o))
	p.proxy.ServeHTTP(w, r)
}

func (p *loginProbe) readiness(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	values := r.Header.Values("X-Siteadmin-Probe")
	if len(values) != 1 || !hmac.Equal([]byte(values[0]), []byte(p.config.Secret)) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"authentication required"}`))
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	pending, quarantined, spoolReady := 0, 0, true
	count := func(directory string) (int, error) {
		file, err := os.Open(directory)
		if errors.Is(err, os.ErrNotExist) && directory != p.config.DataDir {
			return 0, nil
		}
		if err != nil {
			return 0, err
		}
		defer file.Close()
		total := 0
		for {
			names, err := file.Readdirnames(256)
			for _, name := range names {
				if strings.HasSuffix(name, ".json") {
					total++
				}
			}
			if errors.Is(err, io.EOF) {
				return total, nil
			}
			if err != nil {
				return total, err
			}
		}
	}
	var err error
	pending, err = count(p.config.DataDir)
	spoolReady = err == nil
	quarantined, err = count(filepath.Join(p.config.DataDir, "quarantine"))
	spoolReady = spoolReady && err == nil
	check := func(origin *url.URL, path string) bool {
		if origin == nil {
			return false
		}
		target := *origin
		target.Path = path
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target.String(), nil)
		if err != nil {
			return false
		}
		req.Header.Set("Accept", "application/json")
		response, err := p.client.Do(req)
		if err != nil {
			return false
		}
		defer response.Body.Close()
		return response.StatusCode == http.StatusOK
	}
	upstreamReady := check(p.config.Upstream, "/api/status")
	collectorReady := check(p.config.Collector, "/api/health")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"service": "login-probe", "configured": true,
		"ready":          upstreamReady && collectorReady && spoolReady,
		"upstream_ready": upstreamReady, "collector_ready": collectorReady,
		"spool_ready": spoolReady, "pending_events": pending, "quarantined_events": quarantined,
		"traffic_connected": "requires-console-and-auth-routing",
	})
}

func (p *loginProbe) captureDevice(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	origin, err := url.Parse(r.Header.Get("Origin"))
	if err != nil || origin.Host != r.Host || (origin.Scheme != "https" && origin.Scheme != "http") {
		http.Error(w, "same-origin browser event required", http.StatusForbidden)
		return
	}
	id, cookie, err := p.browserID(r)
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	var raw map[string]string
	if err != nil || json.Unmarshal(body, &raw) != nil {
		http.Error(w, "invalid device event", http.StatusBadRequest)
		return
	}
	allowed := map[string]bool{"browser": true, "platform": true, "language": true, "timezone": true,
		"screen": true, "color_depth": true, "touch": true}
	device := make(map[string]string)
	for k, v := range raw {
		if !allowed[k] || len(v) > 256 {
			http.Error(w, "unsupported device field", http.StatusBadRequest)
			return
		}
		device[k] = v
	}
	p.mu.Lock()
	if len(p.devices) >= 10000 {
		for key, record := range p.devices {
			if p.now().Sub(record.At) > time.Hour {
				delete(p.devices, key)
			}
		}
	}
	if len(p.devices) < 10000 {
		p.devices[id] = deviceRecord{Values: device, At: p.now()}
	}
	p.mu.Unlock()
	if cookie != "" {
		w.Header().Add("Set-Cookie", cookie)
	}
	w.WriteHeader(http.StatusNoContent)
}

type joinedBody struct {
	io.Reader
	io.Closer
}

func readResponse(r *http.Response, limit int64) ([]byte, bool) {
	original := r.Body
	raw, err := io.ReadAll(io.LimitReader(original, limit+1))
	if err != nil || int64(len(raw)) > limit {
		r.Body = &joinedBody{Reader: io.MultiReader(bytes.NewReader(raw), original), Closer: original}
		return nil, false
	}
	_ = original.Close()
	r.Body = io.NopCloser(bytes.NewReader(raw))
	if r.Header.Get("Content-Encoding") == "gzip" {
		decoder, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, false
		}
		defer decoder.Close()
		decoded, err := io.ReadAll(io.LimitReader(decoder, limit+1))
		if err != nil || int64(len(decoded)) > limit {
			return nil, false
		}
		return decoded, true
	}
	if encoding := r.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
		return nil, false
	}
	return raw, true
}

func (p *loginProbe) observeResponse(r *http.Response) error {
	o, ok := r.Request.Context().Value(observationKey{}).(observation)
	if !ok {
		return nil
	}
	if o.Cookie != "" {
		r.Header.Add("Set-Cookie", o.Cookie)
	}
	if o.Source != "" {
		// Never buffer SSE, downloads, or general API responses.
		if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
			body, ok := readResponse(r, 1<<20)
			if ok {
				event, recognized := p.authResult(o, r, body)
				if recognized {
					if err := p.enqueue(event); err != nil {
						log.Print("probe observation spool write failed")
					}
				}
			}
		}
		return nil
	}
	if !strings.Contains(r.Header.Get("Content-Type"), "text/html") || r.StatusCode != http.StatusOK {
		return nil
	}
	body, ok := readResponse(r, 4<<20)
	if !ok {
		return nil
	}
	nonce, permitted := injectionNonce(r.Header.Values("Content-Security-Policy"), body)
	if !permitted {
		r.Header.Set("X-Siteadmin-Probe-Device", "blocked-by-csp")
		return nil
	}
	tag := `<script defer src="` + probePrefix + `agent.js"`
	if nonce != "" {
		tag += ` nonce="` + html.EscapeString(nonce) + `"`
	}
	tag += `></script>`
	if at := bytes.Index(bytes.ToLower(body), []byte("</head>")); at >= 0 {
		body = append(append(append([]byte{}, body[:at]...), []byte(tag)...), body[at:]...)
	} else {
		body = append([]byte(tag), body...)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.Header.Set("Content-Length", strconv.Itoa(len(body)))
	r.Header.Del("Content-Encoding")
	r.Header.Del("ETag")
	r.Header.Del("Content-MD5")
	r.Header.Del("Last-Modified")
	r.Header.Set("Cache-Control", "private, no-store")
	r.Header.Set("X-Siteadmin-Probe-Device", "enabled")
	return nil
}

var scriptNonce = regexp.MustCompile(`(?i)<script\b[^>]*\bnonce=["']([^"']+)["']`)

func injectionNonce(policies []string, body []byte) (string, bool) {
	nonce := ""
	if matches := scriptNonce.FindSubmatch(body); len(matches) == 2 {
		nonce = string(matches[1])
	}
	for _, policy := range policies {
		directives := make(map[string][]string)
		for _, raw := range strings.Split(policy, ";") {
			parts := strings.Fields(raw)
			if len(parts) > 0 {
				directives[strings.ToLower(parts[0])] = parts[1:]
			}
		}
		var sources []string
		for _, key := range []string{"script-src-elem", "script-src", "default-src"} {
			if values, exists := directives[key]; exists {
				sources = values
				break
			}
		}
		if sources == nil {
			continue
		}
		allowed := false
		hasNonceOrHash, strict := false, false
		for _, source := range sources {
			hasNonceOrHash = hasNonceOrHash || strings.HasPrefix(source, "'nonce-") || strings.HasPrefix(source, "'sha")
			strict = strict || source == "'strict-dynamic'"
		}
		for _, source := range sources {
			if nonce != "" && source == "'nonce-"+nonce+"'" ||
				(!(strict && hasNonceOrHash) && (source == "'self'" || source == "*")) {
				allowed = true
			}
		}
		if !allowed {
			return "", false
		}
	}
	return nonce, true
}

func numberID(v any) int64 {
	if n, ok := v.(json.Number); ok {
		id, _ := n.Int64()
		return id
	}
	return 0
}

func probeText(value string, limit int) string {
	value = strings.ReplaceAll(strings.ToValidUTF8(value, ""), "\x00", "")
	if len(value) > limit {
		value = value[:limit]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	return value
}

func (p *loginProbe) authResult(o observation, r *http.Response, body []byte) (authEvent, bool) {
	var result map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if decoder.Decode(&result) != nil {
		return authEvent{}, false
	}
	success, explicit := result["success"].(bool)
	if !explicit {
		return authEvent{}, false
	}
	id, err := randomID()
	if err != nil {
		return authEvent{}, false
	}
	event := authEvent{ID: "probe:" + id, Source: o.Source, OccurredAt: p.now().Unix(),
		IP: o.IP, PeerIP: o.PeerIP, BrowserID: o.BrowserID, Method: o.Method,
		ClaimedAccount: o.ClaimedAccount, UserAgent: probeText(r.Request.UserAgent(), 1024), Device: o.Device,
		Metadata: map[string]string{"ip_source": o.IPSource, "sensor": "newapi-login-probe",
			"device_data": "unavailable"}}
	if len(o.Device) > 0 {
		event.Metadata["device_data"] = "available"
	}
	event.Result = "failure"
	if !success || r.StatusCode < 200 || r.StatusCode >= 300 {
		if strings.HasPrefix(o.Method, "oauth:") {
			// A shared OAuth callback can also be an account binding operation.
			return authEvent{}, false
		}
		return event, true
	}
	data, _ := result["data"].(map[string]any)
	if o.Source == "registration" {
		event.Result = "success"
		if data != nil {
			event.UserID = numberID(data["id"])
		}
		event.Metadata["identity_status"] = "awaiting_account_correlation"
		return event, true
	}
	event.Result = "pending"
	if data != nil {
		if user, ok := data["user"].(map[string]any); ok {
			event.UserID = numberID(user["id"])
		} else {
			// Legacy completed login responses carry the user directly in data.
			if username, ok := data["username"].(string); ok && username != "" {
				event.UserID = numberID(data["id"])
			}
		}
		if session, ok := data["session"].(map[string]any); ok {
			if sid, ok := session["id"].(string); ok {
				mac := hmac.New(sha256.New, []byte(p.config.Secret))
				_, _ = mac.Write([]byte("session:" + sid))
				event.SessionID = hex.EncodeToString(mac.Sum(nil))
			}
		}
	}
	challenge := false
	for _, object := range []map[string]any{result, data} {
		for _, key := range []string{"require_2fa", "requires_2fa", "requires_verification", "verification_required"} {
			if v, ok := object[key].(bool); ok && v {
				challenge = true
			}
		}
		if _, exists := object["flow_token"]; exists {
			challenge = true
		}
		if _, exists := object["challenge_id"]; exists {
			challenge = true
		}
	}
	if event.UserID > 0 && !challenge {
		event.Result = "success"
		event.Metadata["identity_status"] = "server_confirmed"
	} else {
		if strings.HasPrefix(o.Method, "oauth:") && !challenge {
			return authEvent{}, false
		}
		event.UserID = 0
		event.Metadata["identity_status"] = "awaiting_final_authentication"
	}
	return event, true
}

func (p *loginProbe) enqueue(event authEvent) error {
	body, err := json.Marshal(event)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(p.config.DataDir, ".event-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err = file.Write(body); err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(p.config.DataDir, strings.TrimPrefix(event.ID, "probe:")+".json"))
}

func (p *loginProbe) deliver(ctx context.Context) error {
	files, err := os.ReadDir(p.config.DataDir)
	if err != nil {
		return err
	}
	sent := 0
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		if sent >= 50 {
			break
		}
		path := filepath.Join(p.config.DataDir, file.Name())
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		target := *p.config.Collector
		target.Path = "/internal/probe/events"
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(body))
		if err != nil {
			return err
		}
		stamp := strconv.FormatInt(p.now().Unix(), 10)
		mac := hmac.New(sha256.New, []byte(p.config.Secret))
		_, _ = mac.Write([]byte(stamp + "\n" + target.Path + "\n"))
		_, _ = mac.Write(body)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Probe-Timestamp", stamp)
		request.Header.Set("X-Probe-Signature", hex.EncodeToString(mac.Sum(nil)))
		response, err := p.client.Do(request)
		if err != nil {
			return errors.New("probe collector unavailable")
		}
		raw, readErr := io.ReadAll(io.LimitReader(response.Body, 4096))
		_ = response.Body.Close()
		var acknowledgment struct {
			Success bool   `json:"success"`
			EventID string `json:"event_id"`
		}
		var event authEvent
		if response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusRequestEntityTooLarge ||
			response.StatusCode == http.StatusUnprocessableEntity || response.StatusCode == http.StatusConflict {
			// A bad observation must not block later events. Retain it for review.
			quarantine := filepath.Join(p.config.DataDir, "quarantine")
			if err := os.MkdirAll(quarantine, 0700); err != nil {
				return err
			}
			if err := os.Rename(path, filepath.Join(quarantine, file.Name())); err != nil {
				return err
			}
			log.Print("probe observation quarantined after collector validation failure")
			continue
		}
		if readErr != nil || response.StatusCode != http.StatusOK || json.Unmarshal(raw, &acknowledgment) != nil ||
			json.Unmarshal(body, &event) != nil || !acknowledgment.Success || acknowledgment.EventID != event.ID {
			return errors.New("probe collector did not acknowledge event")
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		sent++
	}
	return nil
}

func (p *loginProbe) worker(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := p.deliver(ctx); err != nil && ctx.Err() == nil {
				log.Print("probe delivery deferred; observations remain in the spool")
			}
		}
	}
}

func run() error {
	config, err := loadProbeStartupConfig()
	if err != nil {
		return err
	}
	probe, err := newLoginProbe(config)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go probe.worker(ctx)
	server := &http.Server{Addr: config.Listen, Handler: probe, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	select {
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return errors.New("login probe listener failed")
		}
		return nil
	case <-ctx.Done():
		shutdown, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		return server.Shutdown(shutdown)
	}
}

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
