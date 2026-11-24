package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRouter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	staticDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(staticDir, "index.html"), []byte("<main>dashboard</main>"), 0600); err != nil {
		t.Fatal(err)
	}
	config, err := loadConfig(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	config.StaticDir = staticDir
	service, err := newDashboardService(config)
	if err != nil {
		t.Fatal(err)
	}
	demo := newRouter(config, service)
	config.AdminToken = "known-token"
	secured := newRouter(config, service)
	tests := []struct {
		name, target, auth, contentType, body string
		router                                http.Handler
		status                                int
	}{
		{"public health", "/api/health", "", "application/json", `"status":"ok"`, secured, http.StatusOK},
		{"public meta", "/api/meta", "", "application/json", `"auth_required":true`, secured, http.StatusOK},
		{"demo without token", "/api/dashboard?period=7d", "", "application/json", `"mode":"demo"`, demo, http.StatusOK},
		{"empty token", "/api/dashboard?period=7d", "", "application/json", `"error"`, secured, http.StatusUnauthorized},
		{"wrong token", "/api/dashboard?period=7d", "Bearer wrong-token", "application/json", `"error"`, secured, http.StatusUnauthorized},
		{"wrong scheme", "/api/dashboard?period=7d", "Basic known-token", "application/json", `"error"`, secured, http.StatusUnauthorized},
		{"valid token", "/api/dashboard?period=30d", "Bearer known-token", "application/json", `"period":"30d"`, secured, http.StatusOK},
		{"invalid period", "/api/dashboard?period=2d", "Bearer known-token", "application/json", `"error"`, secured, http.StatusBadRequest},
		{"protected request logs", "/api/log-analysis", "", "application/json", `"error"`, secured, http.StatusUnauthorized},
		{"authorized request logs", "/api/log-analysis", "Bearer known-token", "application/json", `"records"`, secured, http.StatusOK},
		{"unknown API", "/api/typo", "", "application/json", `"error"`, demo, http.StatusNotFound},
		{"unknown API with trailing slash", "/api/health/", "", "application/json", `"error"`, demo, http.StatusNotFound},
		{"frontend fallback", "/settings/dashboard", "", "text/html", "<main>dashboard</main>", demo, http.StatusOK},
		{"missing asset", "/assets/missing.css", "", "application/json", `"error"`, demo, http.StatusNotFound},
		{"invalid static separator", "/assets%5c..%5cindex.html", "", "application/json", `"error"`, demo, http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, tc.target, nil)
			if tc.auth != "" {
				request.Header.Set("Authorization", tc.auth)
			}
			result := httptest.NewRecorder()
			tc.router.ServeHTTP(result, request)
			if result.Code != tc.status || !strings.Contains(result.Header().Get("Content-Type"), tc.contentType) ||
				!strings.Contains(result.Body.String(), tc.body) {
				t.Fatalf("unexpected response: status=%d content-type=%s body=%s", result.Code, result.Header().Get("Content-Type"), result.Body)
			}
			if tc.status == http.StatusUnauthorized && result.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Fatal("missing Bearer challenge")
			}
			if tc.status == http.StatusOK && strings.HasPrefix(tc.target, "/api/dashboard") {
				var data DashboardData
				if err := json.NewDecoder(result.Body).Decode(&data); err != nil || data.Trend == nil ||
					data.Models == nil || data.Warnings == nil || data.Sources == nil {
					t.Fatalf("dashboard arrays are missing: %v, %+v", err, data)
				}
			}
		})
	}
}
