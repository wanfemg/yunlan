package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

const testFinanceProxyKey = "local-test-gateway-key-1234567890123"

func TestFinanceUpstreamConfig(t *testing.T) {
	tests := []struct {
		upstream string
		valid    bool
	}{
		{"", true},
		{"http://127.0.0.1:8765", true},
		{"http://[::1]:8765", true},
		{"http://localhost:8765", false},
		{"http://0.0.0.0:8765", false},
		{"http://192.0.2.1:8765", false},
		{"https://127.0.0.1:8765", false},
		{"http://127.0.0.1", false},
		{"http://127.0.0.1:0", false},
		{"http://127.0.0.1:65536", false},
		{"http://127.0.0.1:8765/", false},
		{"http://127.0.0.1:8765/finance", false},
		{"http://127.0.0.1:8765?", false},
		{"http://127.0.0.1:8765?finance=1", false},
		{"http://127.0.0.1:8765#", false},
		{"http://user:secret@127.0.0.1:8765", false},
	}
	for _, tc := range tests {
		t.Run(tc.upstream, func(t *testing.T) {
			config, err := loadConfig(func(key string) string {
				switch key {
				case "FINANCE_UPSTREAM":
					return tc.upstream
				case "FINANCE_PROXY_SECRET":
					return testFinanceProxyKey
				case "ADMIN_TOKEN":
					return "site-admin-token"
				}
				return ""
			})
			if !tc.valid {
				if err == nil || !strings.Contains(err.Error(), "FINANCE_UPSTREAM") || strings.Contains(err.Error(), "secret") {
					t.Fatalf("expected an error naming the setting without credentials, got %v", err)
				}
				return
			}
			if err != nil || tc.upstream == "" && config.FinanceUpstream != nil ||
				tc.upstream != "" && (config.FinanceUpstream == nil || config.FinanceUpstream.String() != tc.upstream) {
				t.Fatalf("unexpected upstream %v, error %v", config.FinanceUpstream, err)
			}
		})
	}
}

func newFinanceTestRouter(t *testing.T, upstream string) http.Handler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	config, err := loadConfig(func(key string) string {
		switch key {
		case "FINANCE_UPSTREAM":
			return upstream
		case "ADMIN_TOKEN":
			return "site-admin-token"
		case "FINANCE_PROXY_SECRET":
			return testFinanceProxyKey
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	config.StaticDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(config.StaticDir, "index.html"), []byte("<main>站长后台</main>"), 0600); err != nil {
		t.Fatal(err)
	}
	service, err := newDashboardService(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	return newRouter(config, service)
}

func TestFinanceProxyPreservesFinanceContract(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, key := range []string{"Authorization", "Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Forwarded-Port", "X-Real-IP"} {
			if got := r.Header.Get(key); got != "" {
				t.Errorf("upstream received %s: %q", key, got)
			}
		}
		if len(r.Cookies()) != 0 || len(r.Header.Values(financeProxyHeader)) != 1 || r.Header.Get(financeProxyHeader) != testFinanceProxyKey {
			t.Error("finance gateway did not replace the request credentials")
		}
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.Header().Set("Referrer-Policy", "no-referrer")
		switch r.URL.Path {
		case "/finance/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, "<main>财务后台</main>")
		case "/finance/static/app.js":
			w.Header().Set("Content-Type", "text/javascript")
			io.WriteString(w, "console.log('finance')")
		case "/finance/api/session":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"authed":true}`)
		case "/finance/api/config":
			body, err := io.ReadAll(r.Body)
			if err != nil || string(body) != `{"password":"example"}` || r.Method != http.MethodPost ||
				r.Header.Get("Content-Type") != "application/json" || r.Header.Get("X-Requested-With") != "newapi-order-notifier" {
				t.Errorf("request not preserved: %s %v %q (%v)", r.Method, r.Header, body, err)
			}
			cookie := &http.Cookie{Name: "notifier_session", Value: "new-session", Path: "/finance/", HttpOnly: true, SameSite: http.SameSiteStrictMode}
			http.SetCookie(w, cookie)
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"ok":true}`)
		case "/finance/api/orders/export.csv":
			if r.URL.RawQuery != "days=7&keyword=a%3Bb%2Bc%25&status=success&status=expired" {
				t.Errorf("query changed: %s", r.URL.RawQuery)
			}
			w.Header().Set("Content-Type", "text/csv; charset=utf-8")
			w.Header().Set("Content-Disposition", "attachment; filename=orders.csv")
			io.WriteString(w, "\ufeff时间,订单号\r\n")
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":"未登录"}`)
		}
	}))
	defer upstream.Close()
	router := newFinanceTestRouter(t, upstream.URL)
	tests := []struct {
		method, target, contentType, body string
		status                            int
	}{
		{http.MethodGet, "/finance/", "text/html", "财务后台", http.StatusOK},
		{http.MethodGet, "/finance/static/app.js?v=2", "text/javascript", "console.log('finance')", http.StatusOK},
		{http.MethodGet, "/finance/api/session", "application/json", `{"authed":true}`, http.StatusOK},
		{http.MethodPost, "/finance/api/config", "application/json", `{"ok":true}`, http.StatusOK},
		{http.MethodGet, "/finance/api/orders/export.csv?days=7&keyword=a%3Bb%2Bc%25&status=success&status=expired", "text/csv", "\ufeff时间,订单号\r\n", http.StatusOK},
		{http.MethodGet, "/finance/api/other", "application/json", `{"error":"未登录"}`, http.StatusUnauthorized},
	}
	for _, tc := range tests {
		t.Run(tc.target, func(t *testing.T) {
			var body io.Reader
			if tc.method == http.MethodPost {
				body = strings.NewReader(`{"password":"example"}`)
			}
			request := httptest.NewRequest(tc.method, tc.target, body)
			request.Header.Set("Authorization", "Bearer site-admin-token")
			request.Header.Add(financeProxyHeader, "client-supplied")
			request.Header.Add(financeProxyHeader, "another-client-value")
			request.Header.Add("Cookie", "dashboard_session=dashboard-token; notifier_session=finance-token")
			request.Header.Add("Cookie", "analytics=analytics-token")
			request.Header.Set("Forwarded", "for=198.51.100.1")
			request.Header.Set("X-Forwarded-For", "198.51.100.1")
			request.Header.Set("X-Forwarded-Host", "example.invalid")
			request.Header.Set("X-Forwarded-Proto", "bogus")
			request.Header.Set("X-Forwarded-Port", "5130")
			request.Header.Set("X-Real-IP", "198.51.100.1")
			if body != nil {
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("X-Requested-With", "newapi-order-notifier")
			}
			result := httptest.NewRecorder()
			router.ServeHTTP(result, request)
			if result.Code != tc.status || !strings.HasPrefix(result.Header().Get("Content-Type"), tc.contentType) ||
				!strings.Contains(result.Body.String(), tc.body) || result.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("unexpected response: %d %v %q", result.Code, result.Header(), result.Body)
			}
			if got := result.Header().Values("Referrer-Policy"); len(got) != 1 || got[0] != "no-referrer" {
				t.Fatalf("unexpected Referrer-Policy: %v", got)
			}
			if len(result.Result().Cookies()) != 0 {
				t.Fatal("independent upstream session cookie leaked")
			}
			if tc.contentType == "text/csv" && result.Header().Get("Content-Disposition") != "attachment; filename=orders.csv" {
				t.Fatal("CSV attachment was lost")
			}
		})
	}
}

func TestFinanceDoesNotRequireSiteAdmin(t *testing.T) {
	var reached []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = append(reached, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	router := newFinanceTestRouter(t, upstream.URL)
	for _, tc := range []struct {
		method, path, authorization string
		code                        int
	}{
		{"GET", "/finance/", "", 200},
		{"GET", "/finance/static/app.js", "", 200},
		{"GET", "/finance/api/session", "", 200},
		{"GET", "/finance/api/orders/export.csv", "", 200},
		{"POST", "/finance/api/config", "", 200},
		{"GET", "/finance/api/finance/summary", "", 200},
		{"GET", "/finance/api/finance/summary", "Bearer wrong-token", 200},
		{"POST", "/finance/api/login", "Bearer site-admin-token", 404},
		{"POST", "/finance/api/logout", "Bearer site-admin-token", 404},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("Authorization", tc.authorization)
		req.Header.Set(financeProxyHeader, testFinanceProxyKey)
		req.Header.Set("Cookie", "notifier_session=old-cookie")
		result := httptest.NewRecorder()
		router.ServeHTTP(result, req)
		if result.Code != tc.code {
			t.Fatalf("%s %s returned %d: %s", tc.method, tc.path, result.Code, result.Body)
		}
	}
	if want := "/finance/,/finance/static/app.js,/finance/api/session,/finance/api/orders/export.csv,/finance/api/config,/finance/api/finance/summary,/finance/api/finance/summary"; strings.Join(reached, ",") != want {
		t.Errorf("unexpected upstream requests: %v", reached)
	}
}

func TestFinanceUnavailableIsolated(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	offline := upstream.URL
	upstream.Close()
	for _, tc := range []struct {
		name, upstream, message string
		status                  int
	}{
		{"unconfigured", "", "尚未配置", http.StatusServiceUnavailable},
		{"offline", offline, "暂时无法连接", http.StatusBadGateway},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := newFinanceTestRouter(t, tc.upstream)
			request := func(method, target string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, target, nil)
				r.Header.Set("Accept", "text/html")
				result := httptest.NewRecorder()
				router.ServeHTTP(result, r)
				return result
			}
			redirect := request(http.MethodGet, "/finance?from=%2F&next=overview")
			if redirect.Code != http.StatusPermanentRedirect || redirect.Header().Get("Location") != "/finance/?from=%2F&next=overview" ||
				redirect.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("unexpected redirect: %d %v", redirect.Code, redirect.Header())
			}
			page := request(http.MethodGet, "/finance/")
			if page.Code != tc.status || !strings.Contains(page.Header().Get("Content-Type"), "text/html") ||
				!strings.Contains(page.Body.String(), tc.message) || !strings.Contains(page.Body.String(), `href="/"`) ||
				page.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("unexpected unavailable page: %d %v %s", page.Code, page.Header(), page.Body)
			}
			head := request(http.MethodHead, "/finance/")
			if head.Code != tc.status || head.Body.Len() != 0 {
				t.Fatalf("unexpected HEAD: %d %q", head.Code, head.Body)
			}
			for _, target := range []string{"/finance/api/session", "/finance/static/app.js", "/finance/favicon.ico"} {
				req := httptest.NewRequest(http.MethodGet, target, nil)
				req.Header.Set("Authorization", "Bearer site-admin-token")
				req.Header.Set("Accept", "text/html")
				result := httptest.NewRecorder()
				router.ServeHTTP(result, req)
				var payload map[string]string
				if result.Code != tc.status || !strings.Contains(result.Header().Get("Content-Type"), "application/json") ||
					result.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(result.Body.Bytes(), &payload) != nil ||
					!strings.Contains(payload["error"], tc.message) || tc.upstream != "" && strings.Contains(result.Body.String(), tc.upstream) {
					t.Fatalf("unexpected error response for %s: %d %v %s", target, result.Code, result.Header(), result.Body)
				}
			}
			if health := request(http.MethodGet, "/api/health"); health.Code != http.StatusOK {
				t.Fatalf("site health affected: %d %s", health.Code, health.Body)
			}
			if dashboard := request(http.MethodGet, "/api/dashboard"); dashboard.Code != http.StatusUnauthorized {
				t.Fatalf("site admin token requirement affected: %d %s", dashboard.Code, dashboard.Body)
			}
			if home := request(http.MethodGet, "/"); home.Code != http.StatusOK || !strings.Contains(home.Body.String(), "站长后台") {
				t.Fatalf("site home affected: %d %s", home.Code, home.Body)
			}
		})
	}
}
