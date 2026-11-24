package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestFinanceShellEmbeddingRemainsSameOrigin(t *testing.T) {
	const originalPolicy = "default-src 'self'; script-src 'self'; frame-ancestors 'none'; form-action 'none'"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", originalPolicy)
		if strings.HasSuffix(r.URL.Path, ".json") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true}`))
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<html>finance</html>"))
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)
	handler := newFinanceHandler(target, strings.Repeat("a", 32))
	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/finance/", nil))
	policy := page.Header().Get("Content-Security-Policy")
	if page.Code != http.StatusOK || !strings.Contains(policy, "frame-ancestors 'self'") ||
		!strings.Contains(policy, "script-src 'self'") || !strings.Contains(policy, "form-action 'none'") ||
		page.Header().Get("X-Frame-Options") != "SAMEORIGIN" {
		t.Fatalf("finance frame policy = %d %q %q", page.Code, policy, page.Header().Get("X-Frame-Options"))
	}
	api := httptest.NewRecorder()
	handler.ServeHTTP(api, httptest.NewRequest(http.MethodGet, "/finance/api/example.json", nil))
	if api.Header().Get("Content-Security-Policy") != originalPolicy || api.Header().Get("X-Frame-Options") != "" {
		t.Fatal("non-HTML finance response policy changed")
	}
}
