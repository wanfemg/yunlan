package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testSecret = "0123456789abcdef0123456789abcdef"

func testProbe(t *testing.T, upstream http.Handler, collector http.Handler) *loginProbe {
	t.Helper()
	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)
	if collector == nil {
		collector = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) })
	}
	sink := httptest.NewServer(collector)
	t.Cleanup(sink.Close)
	target, _ := url.Parse(server.URL)
	destination, _ := url.Parse(sink.URL)
	probe, err := newLoginProbe(probeConfig{Upstream: target, Collector: destination, Secret: testSecret, DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	probe.now = func() time.Time { return time.Unix(1795150800, 0) }
	return probe
}

func spoolEvents(t *testing.T, p *loginProbe) []authEvent {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(p.config.DataDir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	var result []authEvent
	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var event authEvent
		if err := json.Unmarshal(body, &event); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "password-secret") || strings.Contains(string(body), "access-token-secret") {
			t.Fatalf("credentials leaked into observation: %s", body)
		}
		result = append(result, event)
	}
	return result
}

func TestAuthClassificationAndCredentialExclusion(t *testing.T) {
	for _, tc := range []struct {
		name, path, response, result, source string
		id                                   int64
	}{
		{"modern login", "/api/user/login", `{"success":true,"data":{"user":{"id":42,"username":"alice"},"access_token":"access-token-secret","session":{"id":"session-secret"}}}`, "success", "login", 42},
		{"legacy login", "/api/user/login", `{"success":true,"data":{"id":42,"username":"alice"}}`, "success", "login", 42},
		{"challenge", "/api/user/login", `{"success":true,"data":{"id":42,"flow_token":"challenge-secret"}}`, "pending", "login", 0},
		{"unknown success", "/api/user/login", `{"success":true,"data":{"id":42}}`, "pending", "login", 0},
		{"legacy 2fa", "/api/user/login", `{"success":true,"data":{"require_2fa":true}}`, "pending", "login", 0},
		{"failure", "/api/user/login", `{"success":false,"message":"bad password"}`, "failure", "login", 0},
		{"registration", "/api/user/register", `{"success":true,"message":""}`, "success", "registration", 0},
		{"passkey", "/api/user/passkey/login/finish", `{"success":true,"data":{"user":{"id":42}}}`, "success", "login", 42},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := testProbe(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requestBody, _ := io.ReadAll(r.Body)
				if !bytes.Contains(requestBody, []byte("password-secret")) {
					t.Fatal("authentication request was altered")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.response)
			}), nil)
			request := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(`{"username":"alice","password":"password-secret"}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-Forwarded-For", "8.8.8.8")
			result := httptest.NewRecorder()
			p.ServeHTTP(result, request)
			if result.Body.String() != tc.response || result.Code != http.StatusOK {
				t.Fatalf("upstream response changed: %d %s", result.Code, result.Body.String())
			}
			events := spoolEvents(t, p)
			if len(events) != 1 || events[0].Result != tc.result || events[0].Source != tc.source || events[0].UserID != tc.id ||
				events[0].IP != "192.0.2.1" || events[0].ClaimedAccount != "alice" {
				t.Fatalf("unexpected auth event: %+v", events)
			}
			if events[0].SessionID == "session-secret" {
				t.Fatal("raw upstream session id persisted")
			}
		})
	}
}

func TestAPIRequestsAndRefreshNeverBecomeLoginEvents(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/api/user/self", "/api/user/auth/refresh", "/api/user/passkey/login/begin", "/api/oauth/state"} {
		p := testProbe(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"success":true,"data":{"user":{"id":42}}}`)
		}), nil)
		p.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, path, nil))
		if events := spoolEvents(t, p); len(events) != 0 {
			t.Fatalf("%s generated auth events: %+v", path, events)
		}
	}
}

func TestOAuthBindingAndUnknownCallbackAreNotInventedLogins(t *testing.T) {
	for _, tc := range []struct {
		body string
		want string
	}{
		{`{"success":true,"message":"binding complete"}`, ""},
		{`{"success":false,"message":"invalid state"}`, ""},
		{`{"success":true,"data":{"flow_token":"pending"}}`, "pending"},
		{`{"success":true,"data":{"user":{"id":42}}}`, "success"},
	} {
		p := testProbe(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, tc.body)
		}), nil)
		p.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/oauth/github?code=transient", nil))
		events := spoolEvents(t, p)
		if tc.want == "" {
			if len(events) != 0 {
				t.Fatalf("ambiguous OAuth callback classified as login: %+v", events)
			}
		} else if len(events) != 1 || events[0].Result != tc.want {
			t.Fatalf("verified OAuth flow not recognized: %+v", events)
		}
	}
}

func TestTrustedProxyChain(t *testing.T) {
	p := &loginProbe{config: probeConfig{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "10.0.0.2:1234"
	request.Header.Set("X-Forwarded-For", "1.1.1.1, 8.8.8.8, 10.0.0.1")
	ip, peer, source := p.sourceIP(request)
	if ip != "8.8.8.8" || peer != "10.0.0.2" || source != "trusted_proxy" {
		t.Fatalf("wrong trusted boundary: %s %s %s", ip, peer, source)
	}
	request.Header.Set("X-Forwarded-For", "1.1.1.1, invalid")
	ip, _, source = p.sourceIP(request)
	if ip != peer || source != "invalid_forwarded_chain" {
		t.Fatalf("invalid chain accepted: %s %s", ip, source)
	}
}

func TestTrustedTLSOffloadPreservesUpstreamProtocol(t *testing.T) {
	var upstreamProto string
	p := testProbe(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamProto = r.Header.Get("X-Forwarded-Proto")
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<head></head>")
	}), nil)
	p.config.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	request := httptest.NewRequest(http.MethodGet, "/login", nil)
	request.RemoteAddr = "10.0.0.2:1000"
	request.Header.Set("Accept", "text/html")
	request.Header.Set("X-Forwarded-Proto", "https")
	response := httptest.NewRecorder()
	p.ServeHTTP(response, request)
	if upstreamProto != "https" || !strings.Contains(response.Header().Get("Set-Cookie"), "Secure") {
		t.Fatalf("TLS offload lost: proto=%q cookie=%q", upstreamProto, response.Header().Get("Set-Cookie"))
	}
	request = httptest.NewRequest(http.MethodGet, "/login", nil)
	request.RemoteAddr = "8.8.8.8:1000"
	request.Header.Set("Accept", "text/html")
	request.Header.Set("X-Forwarded-Proto", "https")
	p.ServeHTTP(httptest.NewRecorder(), request)
	if upstreamProto != "http" {
		t.Fatalf("untrusted proto accepted: %s", upstreamProto)
	}
}

func TestHTMLInjectionCompressionAndCSP(t *testing.T) {
	for _, tc := range []struct {
		name, policy, body string
		injected           bool
		gzip               bool
	}{
		{"plain", "", "<html><head></head><body>app</body></html>", true, false},
		{"gzip", "", "<html><head></head><body>app</body></html>", true, true},
		{"self", "default-src 'self'", "<head></head>", true, false},
		{"blocked", "script-src 'none'", "<head></head>", false, false},
		{"nonce", "script-src 'nonce-abc' 'strict-dynamic'", `<head><script nonce="abc">app()</script></head>`, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := testProbe(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				w.Header().Set("ETag", "old-etag")
				if tc.policy != "" {
					w.Header().Set("Content-Security-Policy", tc.policy)
				}
				if tc.gzip {
					w.Header().Set("Content-Encoding", "gzip")
					writer := gzip.NewWriter(w)
					_, _ = io.WriteString(writer, tc.body)
					_ = writer.Close()
				} else {
					_, _ = io.WriteString(w, tc.body)
				}
			}), nil)
			request := httptest.NewRequest(http.MethodGet, "/login", nil)
			request.Header.Set("Accept", "text/html")
			recorder := httptest.NewRecorder()
			p.ServeHTTP(recorder, request)
			injected := strings.Contains(recorder.Body.String(), probePrefix+"agent.js")
			if injected != tc.injected {
				t.Fatalf("injection mismatch: %s", recorder.Body.String())
			}
			if tc.injected && (recorder.Header().Get("ETag") != "" || recorder.Header().Get("Content-Encoding") != "") {
				t.Fatal("stale encoding or cache validator after injection")
			}
			if recorder.Header().Get("Content-Security-Policy") != tc.policy {
				t.Fatal("upstream CSP was weakened")
			}
		})
	}
}

func TestDeviceClaimsCannotSetIdentityAndCookiesAreSigned(t *testing.T) {
	p := testProbe(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil)
	request := httptest.NewRequest(http.MethodPost, probePrefix+"device", strings.NewReader(`{"user_id":"42","ip":"8.8.8.8"}`))
	request.Header.Set("Origin", "http://example.com")
	recorder := httptest.NewRecorder()
	p.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("browser identity claims accepted: %d", recorder.Code)
	}
	request = httptest.NewRequest(http.MethodPost, probePrefix+"device", strings.NewReader(`{"timezone":"Asia/Shanghai","screen":"1920x1080"}`))
	request.Header.Set("Origin", "http://example.com")
	recorder = httptest.NewRecorder()
	p.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent || !strings.Contains(recorder.Header().Get("Set-Cookie"), "HttpOnly") {
		t.Fatalf("device event rejected: %d %s", recorder.Code, recorder.Body.String())
	}
	fake := httptest.NewRequest(http.MethodGet, "/", nil)
	fake.AddCookie(&http.Cookie{Name: browserCookie, Value: strings.Repeat("a", 32) + ".forged"})
	id, newCookie, err := p.browserID(fake)
	if err != nil || id == strings.Repeat("a", 32) || newCookie == "" {
		t.Fatal("forged browser identifier trusted")
	}
}

func TestSpoolRetryAndSignedAcknowledgment(t *testing.T) {
	available := false
	sawSignature := false
	p := testProbe(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		stamp := r.Header.Get("X-Probe-Timestamp")
		mac := hmac.New(sha256.New, []byte(testSecret))
		_, _ = mac.Write([]byte(stamp + "\n/internal/probe/events\n"))
		_, _ = mac.Write(body)
		sawSignature = hmac.Equal([]byte(r.Header.Get("X-Probe-Signature")), []byte(hex.EncodeToString(mac.Sum(nil))))
		if !available {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		var event authEvent
		_ = json.Unmarshal(body, &event)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "event_id": event.ID})
	}))
	event := authEvent{ID: "probe:abcd", Source: "login", Result: "success", UserID: 42}
	if err := p.enqueue(event); err != nil {
		t.Fatal(err)
	}
	if err := p.deliver(context.Background()); err == nil || len(spoolEvents(t, p)) != 1 {
		t.Fatal("failed delivery lost the durable event")
	}
	available = true
	if err := p.deliver(context.Background()); err != nil || len(spoolEvents(t, p)) != 0 || !sawSignature {
		t.Fatalf("signed retry failed: %v", err)
	}
}

func TestPermanentObservationFailureDoesNotBlockTheQueue(t *testing.T) {
	var accepted []string
	p := testProbe(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var event authEvent
		_ = json.NewDecoder(r.Body).Decode(&event)
		if event.ID == "probe:aaaa" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		accepted = append(accepted, event.ID)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "event_id": event.ID})
	}))
	for _, id := range []string{"probe:aaaa", "probe:bbbb"} {
		if err := p.enqueue(authEvent{ID: id, Source: "login", Result: "failure"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.deliver(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(accepted) != 1 || accepted[0] != "probe:bbbb" || len(spoolEvents(t, p)) != 0 {
		t.Fatalf("poison observation blocked delivery: %+v", accepted)
	}
	if _, err := os.Stat(filepath.Join(p.config.DataDir, "quarantine", "aaaa.json")); err != nil {
		t.Fatal("rejected event was not retained:", err)
	}
}

func TestObservedUserAgentFitsCollectorLimits(t *testing.T) {
	p := testProbe(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"success":false}`)
	}), nil)
	request := httptest.NewRequest(http.MethodPost, "/api/user/login", nil)
	request.Header.Set("User-Agent", strings.Repeat("x", 2000))
	p.ServeHTTP(httptest.NewRecorder(), request)
	events := spoolEvents(t, p)
	if len(events) != 1 || len(events[0].UserAgent) != 1024 {
		t.Fatalf("unbounded user agent: %+v", events)
	}
}

func TestSSEIsForwardedBeforeCompletion(t *testing.T) {
	release := make(chan struct{})
	p := testProbe(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-release
		_, _ = io.WriteString(w, "data: second\n\n")
	}), nil)
	server := httptest.NewServer(p)
	defer server.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/chat/completions", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal("stream was buffered:", err)
	}
	defer response.Body.Close()
	first := make([]byte, len("data: first\n\n"))
	if _, err := io.ReadFull(response.Body, first); err != nil || string(first) != "data: first\n\n" {
		t.Fatalf("first streamed chunk unavailable: %q %v", first, err)
	}
}
