package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestFinanceSourceInheritsMainOnlyOverPrivateChannel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.RedirectTrailingSlash = false
	mainDSN := "siteadmin:p@ss-word@tcp([::1]:3306)/newapi?parseTime=true&loc=Local"
	config := Config{
		Mode: "live", MainDriver: "mysql", MainDSN: mainDSN,
		LogsDriver: "postgres", LogsDSN: "postgres://logs-secret",
		ManagementDriver: "mysql", ManagementDSN: "management-secret",
	}
	registerFinanceRoutes(router, nil, testFinanceProxyKey, config)
	tests := []struct {
		name, remote, path string
		keys               []string
		status             int
	}{
		{"no key", "127.0.0.1:43000", "/internal/finance/source", nil, 401},
		{"wrong key", "127.0.0.1:43000", "/internal/finance/source", []string{"browser-value"}, 401},
		{"duplicate key", "127.0.0.1:43000", "/internal/finance/source", []string{testFinanceProxyKey, testFinanceProxyKey}, 401},
		{"remote request", "192.0.2.20:43000", "/internal/finance/source", []string{testFinanceProxyKey}, 401},
		{"path mismatch", "127.0.0.1:43000", "/internal/finance/source/", []string{testFinanceProxyKey}, 404},
		{"private request", "[::1]:43000", "/internal/finance/source", []string{testFinanceProxyKey}, 200},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, tc.path, nil)
			request.RemoteAddr = tc.remote
			for _, key := range tc.keys {
				request.Header.Add(financeProxyHeader, key)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != tc.status || response.Header().Get("Cache-Control") != "no-store" && response.Code != 404 {
				t.Fatalf("response = %d %v", response.Code, response.Header())
			}
			body := response.Body.String()
			if strings.Contains(body, "logs-secret") || strings.Contains(body, "management-secret") {
				t.Fatal("finance inherited a non-main connection")
			}
			if tc.status != 200 {
				if strings.Contains(body, "p@ss-word") {
					t.Fatal("rejected request exposed a database secret")
				}
				return
			}
			var payload map[string]string
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil ||
				payload["db_dsn"] != mainDSN || payload["db_type"] != "mysql" ||
				payload["source"] != "siteadmin-main" {
				t.Fatal("main database contract changed")
			}
		})
	}
}

func TestFinanceDemoSourceCannotExposeInheritedLiveCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	registerFinanceSourceRoute(router, Config{
		Mode: "demo", MainDriver: "postgres", MainDSN: "postgres://do-not-expose-live-secret",
	}, testFinanceProxyKey)
	request := httptest.NewRequest(http.MethodGet, "/internal/finance/source", nil)
	request.RemoteAddr = "127.0.0.1:43000"
	request.Header.Set(financeProxyHeader, testFinanceProxyKey)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || strings.Contains(response.Body.String(), "do-not-expose") {
		t.Fatalf("demo source = %d %q", response.Code, response.Body.String())
	}
}
