package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

type redemptionTestResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Data    struct {
		Keys        []string `json:"keys"`
		Count       int      `json:"count"`
		Quotas      []int64  `json:"quotas"`
		ExpiredTime int64    `json:"expired_time"`
		Mode        string   `json:"mode"`
		Persisted   bool     `json:"persisted"`
	} `json:"data"`
}

func redemptionTestService(mode string) *dashboardService {
	location := time.FixedZone("CST", 8*60*60)
	return &dashboardService{
		config: Config{Mode: mode, QuotaPerUnit: 500000, Timezone: "Asia/Shanghai", Location: location},
		now: func() time.Time {
			return time.Date(2026, time.November, 20, 12, 30, 15, 250000000, location)
		},
	}
}

func redemptionTestRouter(s *dashboardService, token string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	registerRedemptionRoutes(router, s, token)
	return router
}

func redemptionTestRequest(t *testing.T, router http.Handler, body, authorization string) (*httptest.ResponseRecorder, redemptionTestResponse) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/redemptions/generate", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	var decoded redemptionTestResponse
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode response %d %q: %v", response.Code, response.Body.String(), err)
	}
	return response, decoded
}

func redemptionTestJSON(t *testing.T, payload map[string]any) string {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func redemptionTestPayload() map[string]any {
	return map[string]any{"name": "兑换测试", "count": 3, "fixed_amount": 1.25}
}

func redemptionTestAssertFailure(t *testing.T, response *httptest.ResponseRecorder, body redemptionTestResponse, status int) {
	t.Helper()
	if response.Code != status || body.Success || body.Message == "" {
		t.Fatalf("failure = %d %s, want status %d, success=false and a message", response.Code, response.Body.String(), status)
	}
	if len(body.Data.Keys) != 0 || strings.Contains(response.Body.String(), `"keys"`) {
		t.Fatalf("failed response exposes generated keys: %s", response.Body.String())
	}
}

func TestRedemptionRoutesRequireAdminToken(t *testing.T) {
	s := redemptionTestService("demo")
	router := redemptionTestRouter(s, "secret")
	for _, authorization := range []string{"", "Bearer wrong", "Basic secret", "Bearer secret extra"} {
		t.Run(authorization, func(t *testing.T) {
			response, _ := redemptionTestRequest(t, router, `{"name":"test","count":1,"fixed_amount":1}`, authorization)
			if response.Code != http.StatusUnauthorized || response.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Fatalf("authentication response = %d %s", response.Code, response.Body.String())
			}
		})
	}
	response, body := redemptionTestRequest(t, router, `{"name":"test","count":1,"fixed_amount":1}`, "bEaReR secret")
	if response.Code != http.StatusOK || !body.Success {
		t.Fatalf("valid administrator rejected: %d %s", response.Code, response.Body.String())
	}
}

func TestRedemptionRouteRegisteredInMainRouter(t *testing.T) {
	s := redemptionTestService("demo")
	response, body := redemptionTestRequest(t, newRouter(s.config, s), `{"name":"test","count":1,"fixed_amount":1}`, "")
	if response.Code != http.StatusOK || !body.Success {
		t.Fatalf("main router response = %d %s", response.Code, response.Body.String())
	}
}

func TestRedemptionDemoPreviewUsesConfiguredQuotaAndNeverTouchesDatabase(t *testing.T) {
	s := redemptionTestService("demo")
	s.config.QuotaPerUnit = 1250
	state := &redemptionTestDBState{beginErr: errors.New("demo must never begin a transaction")}
	s.management = queryDB{db: redemptionTestOpenDB(t, state), driver: "mysql"}
	payload := redemptionTestPayload()
	payload["key_prefix"] = "春日"
	response, body := redemptionTestRequest(t, redemptionTestRouter(s, ""), redemptionTestJSON(t, payload), "")
	if response.Code != http.StatusOK || !body.Success || body.Data.Persisted || body.Data.Mode != "demo" {
		t.Fatalf("demo response = %d %s", response.Code, response.Body.String())
	}
	if body.Data.Count != 3 || len(body.Data.Keys) != 3 || !reflect.DeepEqual(body.Data.Quotas, []int64{1563, 1563, 1563}) || body.Data.ExpiredTime != 0 {
		t.Fatalf("demo result = %+v", body.Data)
	}
	redemptionTestAssertKeys(t, body.Data.Keys, "春日")
	if state.beginCount != 0 || len(state.executions) != 0 {
		t.Fatalf("demo accessed database: %+v", state)
	}
}

func TestRedemptionGenerationAcceptsCountNameAndPrefixBoundaries(t *testing.T) {
	for _, test := range []struct {
		name   string
		count  int
		prefix string
	}{
		{"one code", 1, ""},
		{"thousand codes", 1000, strings.Repeat("a", 20)},
		{"unicode name and prefix", 2, "兑换码AB"},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := redemptionTestPayload()
			payload["count"], payload["name"], payload["key_prefix"] = test.count, "  "+strings.Repeat("兑", 20)+"  ", test.prefix
			response, body := redemptionTestRequest(t, redemptionTestRouter(redemptionTestService("demo"), ""), redemptionTestJSON(t, payload), "")
			if response.Code != http.StatusOK || !body.Success || body.Data.Count != test.count || len(body.Data.Quotas) != test.count || len(body.Data.Keys) != test.count {
				t.Fatalf("generation response = %d %s", response.Code, response.Body.String())
			}
			redemptionTestAssertKeys(t, body.Data.Keys, test.prefix)
		})
	}
}

func redemptionTestAssertKeys(t *testing.T, keys []string, prefix string) {
	t.Helper()
	seen := make(map[string]bool, len(keys))
	for _, key := range keys {
		if !utf8.ValidString(key) || utf8.RuneCountInString(key) != 32 || !strings.HasPrefix(key, prefix) {
			t.Fatalf("invalid 32-character key %q for prefix %q", key, prefix)
		}
		if seen[key] {
			t.Fatalf("duplicate generated key %q", key)
		}
		seen[key] = true
		for _, char := range strings.TrimPrefix(key, prefix) {
			if !strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZ234567abcdefghijklmnopqrstuvwxyz", char) {
				t.Fatalf("non-base32 suffix in key %q", key)
			}
		}
	}
}

func TestRedemptionGenerationRejectsInvalidRequests(t *testing.T) {
	tests := []struct {
		name   string
		modify func(map[string]any)
	}{
		{"missing name", func(p map[string]any) { delete(p, "name") }},
		{"blank name", func(p map[string]any) { p["name"] = " \t\n" }},
		{"long unicode name", func(p map[string]any) { p["name"] = strings.Repeat("兑", 21) }},
		{"zero count", func(p map[string]any) { p["count"] = 0 }},
		{"negative count", func(p map[string]any) { p["count"] = -1 }},
		{"excess count", func(p map[string]any) { p["count"] = 1001 }},
		{"fractional count", func(p map[string]any) { p["count"] = 1.5 }},
		{"long prefix", func(p map[string]any) { p["key_prefix"] = strings.Repeat("a", 21) }},
		{"unicode prefix byte limit", func(p map[string]any) { p["key_prefix"] = strings.Repeat("兑", 7) }},
		{"prefix space", func(p map[string]any) { p["key_prefix"] = "a b" }},
		{"prefix unicode space", func(p map[string]any) { p["key_prefix"] = "a\u3000b" }},
		{"prefix control", func(p map[string]any) { p["key_prefix"] = "a\x00b" }},
		{"missing fixed amount", func(p map[string]any) { delete(p, "fixed_amount") }},
		{"null fixed amount", func(p map[string]any) { p["fixed_amount"] = nil }},
		{"zero fixed amount", func(p map[string]any) { p["fixed_amount"] = 0 }},
		{"negative fixed amount", func(p map[string]any) { p["fixed_amount"] = -1 }},
		{"rounds to zero", func(p map[string]any) { p["fixed_amount"] = 0.0000009 }},
		{"fixed overflow", func(p map[string]any) { p["fixed_amount"] = math.MaxFloat64 }},
		{"unknown quota mode", func(p map[string]any) { p["quota_mode"] = "arbitrary" }},
		{"random missing minimum", func(p map[string]any) { p["quota_mode"], p["max_amount"] = "random", 2 }},
		{"random missing maximum", func(p map[string]any) { p["quota_mode"], p["min_amount"] = "random", 1 }},
		{"random zero minimum", func(p map[string]any) { p["quota_mode"], p["min_amount"], p["max_amount"] = "random", 0, 2 }},
		{"random reversed range", func(p map[string]any) { p["quota_mode"], p["min_amount"], p["max_amount"] = "random", 2, 1 }},
		{"random endpoint overflow", func(p map[string]any) {
			p["quota_mode"], p["min_amount"], p["max_amount"] = "random", 1, math.MaxFloat64
		}},
		{"unknown expiration mode", func(p map[string]any) { p["expire_mode"] = "later" }},
		{"days missing", func(p map[string]any) { p["expire_mode"] = "days" }},
		{"days zero", func(p map[string]any) { p["expire_mode"], p["expire_days"] = "days", 0 }},
		{"days negative", func(p map[string]any) { p["expire_mode"], p["expire_days"] = "days", -1 }},
		{"days beyond year limit", func(p map[string]any) { p["expire_mode"], p["expire_days"] = "days", 3650000 }},
		{"date missing", func(p map[string]any) { p["expire_mode"] = "date" }},
		{"date malformed", func(p map[string]any) { p["expire_mode"], p["expire_date"] = "date", "tomorrow" }},
		{"date past", func(p map[string]any) { p["expire_mode"], p["expire_date"] = "date", "2026-11-19" }},
		{"date same unix second", func(p map[string]any) {
			p["expire_mode"], p["expire_date"] = "date", "2026-11-20T12:30:15.999999999+08:00"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := redemptionTestPayload()
			test.modify(payload)
			response, body := redemptionTestRequest(t, redemptionTestRouter(redemptionTestService("demo"), ""), redemptionTestJSON(t, payload), "")
			redemptionTestAssertFailure(t, response, body, http.StatusBadRequest)
		})
	}
	for _, malformed := range []string{"", "{", `[]`, `{"name":"test","count":"1","fixed_amount":1}`} {
		t.Run("malformed "+malformed, func(t *testing.T) {
			response, body := redemptionTestRequest(t, redemptionTestRouter(redemptionTestService("demo"), ""), malformed, "")
			redemptionTestAssertFailure(t, response, body, http.StatusBadRequest)
		})
	}
}

func TestRedemptionServiceRejectsNonFiniteAmountsAndInvalidUTF8Prefix(t *testing.T) {
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			for _, mode := range []string{"fixed", "random"} {
				params := redemptionGenerateParams{Name: "test", Count: 1, QuotaMode: mode, FixedAmount: &value, MinAmount: &value, MaxAmount: &value}
				if _, err := redemptionTestService("demo").GenerateRedemptions(context.Background(), params); err == nil {
					t.Fatalf("%s mode accepted non-finite amount %v", mode, value)
				}
			}
		})
	}
	amount := 1.0
	params := redemptionGenerateParams{Name: "test", Count: 1, FixedAmount: &amount, KeyPrefix: string([]byte{0xff, 'a'})}
	if _, err := redemptionTestService("demo").GenerateRedemptions(context.Background(), params); err == nil {
		t.Fatal("invalid UTF-8 prefix was accepted")
	}
}

func TestRedemptionQuotaRoundingRandomRangeAndWalletLimit(t *testing.T) {
	for _, test := range []struct {
		name       string
		rate       float64
		parameters map[string]any
		min, max   int64
		status     int
	}{
		{"half rounds up", 500000, map[string]any{"fixed_amount": 0.000001}, 1, 1, http.StatusOK},
		{"decimal half rounds up exactly", 500000, map[string]any{"fixed_amount": 1.000001}, 500001, 500001, http.StatusOK},
		{"maximum wallet quota", 1, map[string]any{"fixed_amount": float64(9007199254740991)}, 9007199254740991, 9007199254740991, http.StatusOK},
		{"above maximum wallet quota", 1, map[string]any{"fixed_amount": float64(9007199254740992)}, 0, 0, http.StatusBadRequest},
		{"random inclusive range", 10, map[string]any{"quota_mode": "random", "min_amount": 1.01, "max_amount": 1.49, "count": 1000}, 10, 15, http.StatusOK},
		{"random equal endpoints", 10, map[string]any{"quota_mode": "random", "min_amount": 1.25, "max_amount": 1.25}, 13, 13, http.StatusOK},
		{"random decimal half boundaries", 500000, map[string]any{"quota_mode": "random", "min_amount": 1.000001, "max_amount": 1.000003, "count": 1000}, 500001, 500002, http.StatusOK},
		{"random decimal equal boundary", 500000, map[string]any{"quota_mode": "random", "min_amount": 1.000001, "max_amount": 1.000001}, 500001, 500001, http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := redemptionTestService("demo")
			s.config.QuotaPerUnit = test.rate
			payload := redemptionTestPayload()
			for key, value := range test.parameters {
				payload[key] = value
			}
			response, body := redemptionTestRequest(t, redemptionTestRouter(s, ""), redemptionTestJSON(t, payload), "")
			if test.status != http.StatusOK {
				redemptionTestAssertFailure(t, response, body, test.status)
				return
			}
			if response.Code != http.StatusOK || !body.Success || len(body.Data.Quotas) != body.Data.Count {
				t.Fatalf("quota result = %d %s", response.Code, response.Body.String())
			}
			for _, quota := range body.Data.Quotas {
				if quota < test.min || quota > test.max {
					t.Fatalf("quota %d outside [%d,%d]", quota, test.min, test.max)
				}
			}
		})
	}
}

func TestRedemptionRawDecimalPrecisionRejectsRoundingAndOrderingLoss(t *testing.T) {
	for _, test := range []struct {
		name string
		rate float64
		body string
	}{
		{"positive amount below one-half quota", 1, `{"name":"precise","count":1,"fixed_amount":0.4999999999999999999999999}`},
		{"reversed random range below float precision", 500000, `{"name":"precise","count":1,"quota_mode":"random","min_amount":1.00000000000000002,"max_amount":1.00000000000000001}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := redemptionTestService("demo")
			s.config.QuotaPerUnit = test.rate
			response, body := redemptionTestRequest(t, redemptionTestRouter(s, ""), test.body, "")
			redemptionTestAssertFailure(t, response, body, http.StatusBadRequest)
		})
	}
}

func TestRedemptionExpirationUsesConfiguredLocationAndFrozenClock(t *testing.T) {
	s := redemptionTestService("demo")
	want := time.Date(2026, time.December, 1, 9, 10, 0, 0, s.config.Location).Unix()
	for _, date := range []string{"2026-12-01T09:10:00+08:00", "2026-12-01T01:10:00.123456789Z", "2026-12-01T09:10:00", "2026-12-01T09:10"} {
		t.Run(date, func(t *testing.T) {
			payload := redemptionTestPayload()
			payload["expire_mode"], payload["expire_date"] = "date", date
			response, body := redemptionTestRequest(t, redemptionTestRouter(s, ""), redemptionTestJSON(t, payload), "")
			if response.Code != http.StatusOK || !body.Success || body.Data.ExpiredTime != want {
				t.Fatalf("expiration = %d %s, want timestamp %d", response.Code, response.Body.String(), want)
			}
		})
	}
	payload := redemptionTestPayload()
	payload["expire_mode"], payload["expire_date"] = "date", "2026-12-01"
	response, body := redemptionTestRequest(t, redemptionTestRouter(s, ""), redemptionTestJSON(t, payload), "")
	if response.Code != http.StatusOK || body.Data.ExpiredTime != time.Date(2026, time.December, 1, 0, 0, 0, 0, s.config.Location).Unix() {
		t.Fatalf("date-only expiration = %d %s", response.Code, response.Body.String())
	}
	payload["expire_mode"], payload["expire_days"] = "days", 2
	response, body = redemptionTestRequest(t, redemptionTestRouter(s, ""), redemptionTestJSON(t, payload), "")
	if response.Code != http.StatusOK || body.Data.ExpiredTime != s.now().Unix()+2*86400 {
		t.Fatalf("days expiration = %d %s", response.Code, response.Body.String())
	}
}

func TestRedemptionLiveMissingManagementDatabase(t *testing.T) {
	s := redemptionTestService("live")
	response, body := redemptionTestRequest(t, redemptionTestRouter(s, ""), redemptionTestJSON(t, redemptionTestPayload()), "")
	redemptionTestAssertFailure(t, response, body, http.StatusServiceUnavailable)
}

func TestRedemptionCanceledContextDoesNotTouchDatabaseOrReturnKeys(t *testing.T) {
	s := redemptionTestService("live")
	state := &redemptionTestDBState{}
	s.management = queryDB{db: redemptionTestOpenDB(t, state), driver: "mysql"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	amount := 1.0
	result, err := s.GenerateRedemptions(ctx, redemptionGenerateParams{Name: "test", Count: 1, FixedAmount: &amount})
	if !errors.Is(err, context.Canceled) || len(result.Keys) != 0 || state.beginCount != 0 || len(state.executions) != 0 {
		t.Fatalf("canceled generation = %+v, err=%v, database=%+v", result, err, state)
	}
}

func TestRedemptionLivePersistsOneParameterizedBatchInManagementTransaction(t *testing.T) {
	for _, dialect := range []string{"mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			s := redemptionTestService("live")
			state := &redemptionTestDBState{}
			database := redemptionTestOpenDB(t, state)
			s.management = queryDB{db: database, driver: dialect}
			var mainState *redemptionTestDBState
			if dialect == "mysql" {
				// This is the normal main-connection alias established by newDashboardService.
				s.main = s.management
			} else {
				mainState = &redemptionTestDBState{beginErr: errors.New("must use management connection")}
				s.main = queryDB{db: redemptionTestOpenDB(t, mainState), driver: "mysql"}
			}
			payload := redemptionTestPayload()
			payload["name"], payload["key_prefix"], payload["expire_mode"], payload["expire_days"] = "  测试'批次  ", "x'", "days", 1
			response, body := redemptionTestRequest(t, redemptionTestRouter(s, ""), redemptionTestJSON(t, payload), "")
			if response.Code != http.StatusOK || !body.Success || !body.Data.Persisted || body.Data.Mode != "live" {
				t.Fatalf("live result = %d %s", response.Code, response.Body.String())
			}
			if state.beginCount != 1 || state.commitCount != 1 || state.rollbackCount != 0 || len(state.executions) != 1 {
				t.Fatalf("transaction operations = %+v", state)
			}
			if mainState != nil && (mainState.beginCount != 0 || len(mainState.executions) != 0) {
				t.Fatalf("explicit management write touched main connection: %+v", mainState)
			}
			execution := state.executions[0]
			query := strings.Join(strings.Fields(execution.query), " ")
			quotedKey := "`key`"
			if dialect == "postgres" {
				quotedKey = `"key"`
				if strings.Contains(query, "?") || !strings.Contains(query, "$1") || !strings.Contains(query, "$27") {
					t.Fatalf("PostgreSQL placeholders = %s", query)
				}
			} else if strings.Count(query, "?") != 27 || strings.Contains(query, "$1") {
				t.Fatalf("MySQL placeholders = %s", query)
			}
			for _, expected := range []string{"INSERT INTO redemptions", quotedKey, "user_id", "status", "name", "quota", "created_time", "redeemed_time", "used_user_id", "expired_time"} {
				if !strings.Contains(query, expected) {
					t.Fatalf("insert missing %q: %s", expected, query)
				}
			}
			if strings.Contains(query, "测试") || strings.Contains(query, "x'") || len(execution.args) != 27 {
				t.Fatalf("query contains literal input or wrong argument count: %s, %v", query, execution.args)
			}
			redemptionTestAssertKeys(t, body.Data.Keys, "x'")
			for index, key := range body.Data.Keys {
				args := execution.args[index*9 : (index+1)*9]
				want := []driver.Value{int64(1), key, int64(1), "测试'批次", int64(625000), s.now().Unix(), int64(0), int64(0), s.now().Unix() + 86400}
				if !reflect.DeepEqual(args, want) {
					t.Fatalf("insert row %d = %#v, want %#v", index, args, want)
				}
			}
		})
	}
}

func TestRedemptionLiveTransactionFailuresNeverReturnKeys(t *testing.T) {
	for _, test := range []struct {
		name                                   string
		state                                  redemptionTestDBState
		begins, commits, rollbacks, executions int
	}{
		{"begin", redemptionTestDBState{beginErr: errors.New("sensitive-db-error password=secret")}, 1, 0, 0, 0},
		{"execution", redemptionTestDBState{execErr: errors.New("sensitive-db-error INSERT code=secret")}, 1, 0, 1, 1},
		{"commit", redemptionTestDBState{commitErr: errors.New("sensitive-db-error connection=secret")}, 1, 1, 0, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := redemptionTestService("live")
			state := test.state
			s.management = queryDB{db: redemptionTestOpenDB(t, &state), driver: "mysql"}
			response, body := redemptionTestRequest(t, redemptionTestRouter(s, ""), redemptionTestJSON(t, redemptionTestPayload()), "")
			redemptionTestAssertFailure(t, response, body, http.StatusServiceUnavailable)
			if strings.Contains(response.Body.String(), "sensitive-db-error") || strings.Contains(response.Body.String(), "secret") {
				t.Fatalf("failed response exposes database details: %s", response.Body.String())
			}
			if state.beginCount != test.begins || state.commitCount != test.commits || state.rollbackCount != test.rollbacks || len(state.executions) != test.executions {
				t.Fatalf("failure transaction operations = %+v", state)
			}
		})
	}
}

// The connector intentionally uses no real driver or database server.
type redemptionTestDBState struct {
	beginErr, execErr, commitErr           error
	beginCount, commitCount, rollbackCount int
	executions                             []redemptionTestExecution
}

type redemptionTestExecution struct {
	query string
	args  []driver.Value
}

type redemptionTestConnector struct{ state *redemptionTestDBState }
type redemptionTestDriver struct{}
type redemptionTestConn struct {
	state         *redemptionTestDBState
	inTransaction bool
}
type redemptionTestTx struct{ conn *redemptionTestConn }

func (c *redemptionTestConn) QueryContext(_ context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
	return &managedUserTestRows{columns: []string{
		"user_id", "key", "status", "name", "quota", "created_time", "redeemed_time", "used_user_id", "expired_time",
	}}, nil
}

func (c *redemptionTestConn) CheckNamedValue(_ *driver.NamedValue) error {
	return driver.ErrSkip
}

func redemptionTestOpenDB(t *testing.T, state *redemptionTestDBState) *sql.DB {
	t.Helper()
	database := sql.OpenDB(redemptionTestConnector{state: state})
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func (connector redemptionTestConnector) Connect(context.Context) (driver.Conn, error) {
	return &redemptionTestConn{state: connector.state}, nil
}

func (redemptionTestConnector) Driver() driver.Driver { return redemptionTestDriver{} }
func (redemptionTestDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("use the test connector")
}
func (*redemptionTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepared statement")
}
func (*redemptionTestConn) Close() error { return nil }
func (conn *redemptionTestConn) Begin() (driver.Tx, error) {
	return conn.BeginTx(context.Background(), driver.TxOptions{})
}
func (conn *redemptionTestConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	conn.state.beginCount++
	if conn.state.beginErr != nil {
		return nil, conn.state.beginErr
	}
	conn.inTransaction = true
	return &redemptionTestTx{conn: conn}, nil
}
func (conn *redemptionTestConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if !conn.inTransaction {
		return nil, errors.New("execution outside transaction")
	}
	values := make([]driver.Value, len(args))
	for index, arg := range args {
		values[index] = arg.Value
	}
	conn.state.executions = append(conn.state.executions, redemptionTestExecution{query: query, args: values})
	if conn.state.execErr != nil {
		return nil, conn.state.execErr
	}
	return driver.RowsAffected(len(args) / 9), nil
}
func (transaction *redemptionTestTx) Commit() error {
	transaction.conn.state.commitCount++
	transaction.conn.inTransaction = false
	return transaction.conn.state.commitErr
}
func (transaction *redemptionTestTx) Rollback() error {
	transaction.conn.state.rollbackCount++
	transaction.conn.inTransaction = false
	return nil
}
