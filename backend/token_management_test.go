package main

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestMaskTokenKey(t *testing.T) {
	for _, test := range []struct{ key, want string }{
		{"1234567890abcdef", "12345678****"},
		{"short", "short****"},
		{"", "****"},
	} {
		if got := maskTokenKey(test.key); got != test.want {
			t.Errorf("maskTokenKey(%q) = %q, want %q", test.key, got, test.want)
		}
	}
}

func TestParseTokenIDs(t *testing.T) {
	got, err := parseTokenIDs("7, 2,7,9")
	if err != nil || !reflect.DeepEqual(got, []int64{7, 2, 9}) {
		t.Fatalf("parseTokenIDs() = %v, %v", got, err)
	}
	if _, err := parseTokenIDs("1,nope"); err == nil {
		t.Fatal("expected malformed token ID to fail")
	}
	if _, err := parseTokenIDs(strings.Repeat("1,", 201)); err == nil {
		t.Fatal("expected oversized token ID list to fail")
	}
}

func TestTokenFilterValidationAndDialectIdentifiers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		query string
		valid bool
	}{
		{"?status=active&page=2&page_size=50&group=default", true},
		{"?status=unknown", false},
		{"?page_size=1000", false},
		{"?user_id=-1", false},
		{"?risk=leak", false},
	}
	for _, test := range tests {
		request := httptest.NewRequest(http.MethodGet, "/api/tokens"+test.query, nil)
		context, _ := gin.CreateTestContext(httptest.NewRecorder())
		context.Request = request
		_, err := (&dashboardService{}).parseTokenFilters(context)
		if (err == nil) != test.valid {
			t.Errorf("parseTokenFilters(%q) error = %v, valid = %v", test.query, err, test.valid)
		}
	}
	if got := tokenIdentifier("mysql", "group"); got != "`group`" {
		t.Errorf("MySQL identifier = %q", got)
	}
	if got := tokenIdentifier("postgres", "key"); got != `"key"` {
		t.Errorf("PostgreSQL identifier = %q", got)
	}
}

func TestTokenStatusFiltersPreserveExpiredAndExhaustedStates(t *testing.T) {
	disabled, _ := tokenBaseConditions(tokenListFilters{status: "disabled"}, "mysql")
	if !strings.Contains(strings.Join(disabled, " AND "), "t.status = 2") {
		t.Fatalf("disabled filter does not target manual disable: %v", disabled)
	}
	expired, args := tokenBaseConditions(tokenListFilters{status: "expired"}, "postgres")
	if !strings.Contains(strings.Join(expired, " AND "), "t.status = 3 OR") || len(args) != 1 {
		t.Fatalf("expired filter misses status 3: %v, args=%v", expired, args)
	}
	if target, expected, action := tokenBatchStatus(false); target != 2 || expected != 1 || action != "disabled" {
		t.Fatalf("disable transition = %d from %d (%s)", target, expected, action)
	}
	if target, expected, action := tokenBatchStatus(true); target != 1 || expected != 2 || action != "enabled" {
		t.Fatalf("enable transition = %d from %d (%s)", target, expected, action)
	}
}

func TestTokenRoutesRequireAdminAndReportMissingDatabase(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	service := &dashboardService{}
	registerTokenManagementRoutes(router, service, "secret")

	unauthorized := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/tokens/statistics", nil)
	router.ServeHTTP(unauthorized, request)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want %d", unauthorized.Code, http.StatusUnauthorized)
	}

	missingDB := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/api/tokens/statistics", nil)
	request.Header.Set("Authorization", "Bearer secret")
	router.ServeHTTP(missingDB, request)
	if missingDB.Code != http.StatusServiceUnavailable || !strings.Contains(missingDB.Body.String(), "token management database") {
		t.Fatalf("missing DB response = %d %s", missingDB.Code, missingDB.Body.String())
	}
}
