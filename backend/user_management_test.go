package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestUserActivityBoundariesAndMissingHistory(t *testing.T) {
	now := int64(1800000000)
	tests := []struct {
		name     string
		last     int64
		requests int64
		complete bool
		want     string
	}{
		{"at24h", now - 86400, 1, false, "daily"},
		{"after24h", now - 86401, 1, false, "weekly"},
		{"at7d", now - 7*86400, 1, false, "weekly"},
		{"after7d", now - 7*86400 - 1, 1, false, "monthly"},
		{"at30d", now - 30*86400, 1, false, "monthly"},
		{"after30d", now - 30*86400 - 1, 1, false, "silent"},
		{"at90d", now - 90*86400, 1, false, "silent"},
		{"older", now - 90*86400 - 1, 1, false, "dormant"},
		{"missing", 0, 0, false, "unknown"},
		{"completeNever", 0, 0, true, "never"},
		{"counterProvesGap", 0, 5, true, "unknown"},
		{"future", now + 1, 1, true, "unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyUserActivity(test.last, test.requests, now, test.complete); got != test.want {
				t.Fatalf("activity = %q, want %q", got, test.want)
			}
		})
	}
}

func TestUserCallActivitySeparatesAuthenticationAndCountsFailedAttempts(t *testing.T) {
	now := int64(1800000000)
	users := []managedUserRecord{{ID: 1}, {ID: 2}, {ID: 3}}
	events := []networkEvent{
		{ID: "login", UserID: 1, Source: "login", Result: "success", OccurredAt: now - 1},
		{ID: "registration", UserID: 1, Source: "registration", Result: "success", OccurredAt: now - 2},
		{ID: "failed", UserID: 2, Source: "api_call", Result: "failure", OccurredAt: now - 3},
		{ID: "failed", UserID: 2, Source: "api_call", Result: "failure", OccurredAt: now - 3},
		{ID: "old", UserID: 3, Source: "api_call", Result: "success", OccurredAt: now - 150*86400},
		{ID: "future", UserID: 3, Source: "api_call", Result: "success", OccurredAt: now + 1},
	}
	items := summarizeUserActivity(users, events, now, false)
	if items[0].UserActivity.Activity != "unknown" || items[0].UserActivity.ObservedRequests != 0 || items[0].Login.Successes30d != 1 {
		t.Fatalf("authentication affected API activity: %+v", items[0])
	}
	if items[1].UserActivity.Activity != "daily" || items[1].UserActivity.Failures30d != 1 ||
		items[1].UserActivity.Successes30d != 0 || items[1].UserActivity.Requests7d != 1 {
		t.Fatalf("failed request was dropped or duplicated: %+v", items[1])
	}
	if items[2].UserActivity.Activity != "dormant" || items[2].UserActivity.LastAttemptAt != now-150*86400 {
		t.Fatalf("old persisted last request was lost: %+v", items[2])
	}
	stats := managedUserStatistics(items)
	if stats["active_7d"] != int64(1) || stats["login_users_30d"] != int64(1) {
		t.Fatalf("stats use a different activity definition: %+v", stats)
	}
	filtered := filterManagedUsers(items, userManagementFilters{
		activity: "active_7d", intensity: "all", status: "all", sort: "id", order: "asc",
	})
	if len(filtered) != 1 || filtered[0].ID != 2 {
		t.Fatalf("activity filter did not match stats: %+v", filtered)
	}
}

func TestUserIntensityBoundaries(t *testing.T) {
	for _, test := range []struct {
		calls int64
		want  string
	}{{0, "none"}, {1, "low"}, {9, "low"}, {10, "regular"}, {99, "regular"}, {100, "high"}, {999, "high"}, {1000, "dense"}} {
		if got := classifyCallIntensity(test.calls); got != test.want {
			t.Errorf("intensity(%d) = %q, want %q", test.calls, got, test.want)
		}
	}
}

func TestUserActiveDaysUseConfiguredTimezone(t *testing.T) {
	first := time.Date(2026, 11, 20, 15, 59, 0, 0, time.UTC).Unix()
	second := first + 120
	events := []networkEvent{
		{ID: "first", Source: "api_call", Result: "success", OccurredAt: first, UserID: 7},
		{ID: "second", Source: "api_call", Result: "success", OccurredAt: second, UserID: 7},
	}
	items := summarizeUserActivity([]managedUserRecord{{ID: 7}}, events, second+1, true, time.FixedZone("CST", 8*3600))
	if items[0].UserActivity.ActiveDays7d != 2 || items[0].UserActivity.ActiveDays30d != 2 {
		t.Fatalf("local midnight was not respected: %+v", items[0].UserActivity)
	}
}

func TestUserActiveDaysUseSevenAndThirtyCalendarDates(t *testing.T) {
	location := time.FixedZone("CST", 8*3600)
	asOf := time.Date(2026, time.November, 20, 23, 45, 0, 0, location)
	var events []networkEvent
	for day := 0; day < 30; day++ {
		at := time.Date(2026, time.November, 20, 12, 0, 0, 0, location).AddDate(0, 0, -day)
		events = append(events, networkEvent{ID: at.Format("2006-01-02"), UserID: 7,
			Source: "api_call", Result: "success", OccurredAt: at.Unix()})
	}
	for index, at := range []time.Time{
		time.Date(2026, time.October, 21, 23, 50, 0, 0, location),
		time.Date(2026, time.November, 13, 23, 50, 0, 0, location),
		time.Date(2026, time.November, 14, 0, 0, 0, 0, location),
		time.Date(2026, time.November, 13, 23, 59, 59, 0, location),
		asOf.Add(time.Second),
	} {
		events = append(events, networkEvent{ID: "boundary-" + string(rune('a'+index)), UserID: 7,
			Source: "api_call", Result: "failure", OccurredAt: at.Unix()})
	}
	item := summarizeUserActivity([]managedUserRecord{{ID: 7}}, events, asOf.Unix(), true, location)[0]
	if item.UserActivity.ActiveDays7d != 7 || item.UserActivity.ActiveDays30d != 30 {
		t.Fatalf("active days exceeded calendar windows: %+v", item.UserActivity)
	}
	if item.UserActivity.Requests7d != 10 || item.UserActivity.Requests30d != 34 {
		t.Fatalf("calendar-day correction changed rolling request counts: %+v", item.UserActivity)
	}
}

func TestUserActiveDaysExcludePriorDateInsteadOfClamping(t *testing.T) {
	location := time.FixedZone("CST", 8*3600)
	asOf := time.Date(2026, time.November, 20, 23, 45, 0, 0, location).Unix()
	events := []networkEvent{
		{ID: "prior-week-date", UserID: 7, Source: "api_call", Result: "success",
			OccurredAt: time.Date(2026, time.November, 13, 23, 50, 0, 0, location).Unix()},
		{ID: "prior-month-date", UserID: 8, Source: "api_call", Result: "success",
			OccurredAt: time.Date(2026, time.October, 21, 23, 50, 0, 0, location).Unix()},
	}
	items := summarizeUserActivity([]managedUserRecord{{ID: 7}, {ID: 8}}, events, asOf, true, location)
	if items[0].UserActivity.Requests7d != 1 || items[0].UserActivity.ActiveDays7d != 0 ||
		items[0].UserActivity.ActiveDays30d != 1 {
		t.Fatalf("the eighth date was counted or only capped: %+v", items[0].UserActivity)
	}
	if items[1].UserActivity.Requests30d != 1 || items[1].UserActivity.ActiveDays30d != 0 {
		t.Fatalf("the thirty-first date was counted or only capped: %+v", items[1].UserActivity)
	}
}

func TestUserActiveDaysUseCalendarDatesAcrossDST(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, asOf := range []time.Time{
		time.Date(2026, time.March, 9, 23, 45, 0, 0, location),
		time.Date(2026, time.November, 2, 23, 45, 0, 0, location),
	} {
		t.Run(asOf.Format("2006-01-02"), func(t *testing.T) {
			var events []networkEvent
			for day := 0; day < 30; day++ {
				at := time.Date(asOf.Year(), asOf.Month(), asOf.Day(), 12, 0, 0, 0, location).AddDate(0, 0, -day)
				if day == 6 || day == 29 {
					at = time.Date(at.Year(), at.Month(), at.Day(), 0, 10, 0, 0, location)
				}
				events = append(events, networkEvent{ID: at.Format("2006-01-02"), UserID: 7,
					Source: "api_call", Result: "success", OccurredAt: at.Unix()})
			}
			for _, day := range []int{7, 30} {
				at := asOf.AddDate(0, 0, -day)
				at = time.Date(at.Year(), at.Month(), at.Day(), 23, 50, 0, 0, location)
				events = append(events, networkEvent{ID: at.Format("2006-01-02") + "-late", UserID: 7,
					Source: "api_call", Result: "success", OccurredAt: at.Unix()})
			}
			item := summarizeUserActivity([]managedUserRecord{{ID: 7}}, events, asOf.Unix(), true, location)[0]
			if item.UserActivity.ActiveDays7d != 7 || item.UserActivity.ActiveDays30d != 30 {
				t.Fatalf("DST changed calendar-day counts: %+v", item.UserActivity)
			}
			wantRequests7d := int64(8)
			if asOf.Month() == time.November {
				wantRequests7d = 6
			}
			if item.UserActivity.Requests7d != wantRequests7d {
				t.Fatalf("rolling requests were changed at DST: %+v", item.UserActivity)
			}
		})
	}
}

func TestUserActivityResponsesDeclareCalendarDayBasis(t *testing.T) {
	service := userManagementTestService(t)
	service.userManagement.demo[201] = managedUserRecord{ID: 201, Username: "calendar_user", Role: 1, Status: 1}
	router := userManagementTestRouter(service)
	for _, path := range []string{"/api/users", "/api/users/activity-stats", "/api/users/201"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer secret")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		var payload struct {
			Data struct {
				Basis    string `json:"active_days_basis"`
				Timezone string `json:"active_days_timezone"`
			} `json:"data"`
		}
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &payload) != nil ||
			payload.Data.Basis != "calendar_days_including_today" || payload.Data.Timezone != service.config.Location.String() {
			t.Fatalf("%s omits active-day semantics: %d %s", path, response.Code, response.Body.String())
		}
	}
	request := httptest.NewRequest(http.MethodGet, "/api/users/export", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	rows, err := csv.NewReader(strings.NewReader(response.Body.String())).ReadAll()
	if err != nil || response.Code != http.StatusOK || len(rows) != 2 {
		t.Fatalf("calendar CSV unavailable: %d %s %v", response.Code, response.Body.String(), err)
	}
	values := map[string]string{}
	for index, header := range rows[0] {
		values[header] = rows[1][index]
	}
	if values["active_days_7d"] != "0" || values["active_days_30d"] != "0" ||
		values["active_days_basis"] != "calendar_days_including_today" || values["active_days_timezone"] != service.config.Location.String() {
		t.Fatalf("CSV omits active-day semantics: %+v", values)
	}
}

func TestUnverifiedFailedLoginAttemptsStaySeparateForEachAccount(t *testing.T) {
	now := int64(1800000000)
	users := []managedUserRecord{{ID: 11, Username: "alice"}, {ID: 12, Username: "bob"}}
	events := []networkEvent{
		{ID: "alice-1", Source: "login", Result: "failure", UserID: 0, ClaimedAccount: "alice", OccurredAt: now - 1},
		{ID: "alice-2", Source: "login", Result: "failure", UserID: 0, ClaimedAccount: "alice", OccurredAt: now - 2},
		{ID: "alice-2", Source: "login", Result: "failure", UserID: 0, ClaimedAccount: "alice", OccurredAt: now - 2},
		{ID: "bob-1", Source: "login", Result: "failure", UserID: 0, ClaimedAccount: "bob", OccurredAt: now - 3},
		{ID: "unknown", Source: "login", Result: "failure", UserID: 0, ClaimedAccount: "someone-else", OccurredAt: now - 4},
		{ID: "unconfirmed-success", Source: "login", Result: "success", UserID: 0, ClaimedAccount: "alice", OccurredAt: now - 5},
		{ID: "confirmed-failure", Source: "login", Result: "failure", UserID: 11, OccurredAt: now - 6},
	}
	items := summarizeUserActivity(users, events, now, false)
	if items[0].Login.AttemptedAccountFailures != 2 || items[1].Login.AttemptedAccountFailures != 1 {
		t.Fatalf("attempted accounts were mixed or duplicated: %+v", items)
	}
	if items[0].Login.Failures30d != 1 || items[1].Login.Failures30d != 0 {
		t.Fatalf("unverified attempts entered verified failure counters: %+v", items)
	}
	for _, item := range items {
		if item.Login.AttemptedAccountIdentity != "unverified" || item.Login.Successes30d != 0 ||
			item.UserActivity.ObservedRequests != 0 || item.UserActivity.Activity != "unknown" {
			t.Fatalf("attempted identity was promoted to authenticated usage: %+v", item)
		}
	}
	if events[0].UserID != 0 || managedUserStatistics(items)["attempted_account_failures"] != int64(3) {
		t.Fatal("event identity was rewritten or statistics lost attempted failures")
	}
}

func TestProbeRegistrationCorrelationRequiresFreshServerCreationEvidence(t *testing.T) {
	now := int64(1800000000)
	tests := []struct {
		name, username string
		eventAt        int64
		user           managedUserRecord
		wantID         int64
	}{
		{"fresh", "alice", now, managedUserRecord{ID: 7, Username: "alice", CreatedAt: now - 1, CreatedAtKnown: true}, 7},
		{"unknownCreationColumn", "alice", now, managedUserRecord{ID: 7, Username: "alice", CreatedAt: now - 1}, 0},
		{"existingAccount", "alice", now, managedUserRecord{ID: 7, Username: "alice", CreatedAt: now - 10000, CreatedAtKnown: true}, 0},
		{"reusedName", "alice", now, managedUserRecord{ID: 7, Username: "alice", CreatedAt: now + 3600, CreatedAtKnown: true}, 0},
		{"delayedEvenMatchingOldCreation", "alice", now - 3600, managedUserRecord{ID: 7, Username: "alice", CreatedAt: now - 3601, CreatedAtKnown: true}, 0},
		{"missingAccount", "nobody", now, managedUserRecord{ID: 7, Username: "alice", CreatedAt: now - 1, CreatedAtKnown: true}, 0},
		{"caseIsNotAssumedEquivalent", "ALICE", now, managedUserRecord{ID: 7, Username: "alice", CreatedAt: now - 1, CreatedAtKnown: true}, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s := userManagementTestService(t)
			s.userManagement.demo[test.user.ID] = test.user
			original := networkEvent{
				ID: "registration", Source: "registration", Result: "success", ClaimedAccount: test.username,
				OccurredAt: test.eventAt, Metadata: map[string]string{"probe_version": "1"},
			}
			event, err := s.correlateProbeRegistration(context.Background(), original)
			if err != nil || event.UserID != test.wantID {
				t.Fatalf("registration correlation = %+v, %v", event, err)
			}
			wantStatus := "uncorrelated"
			if test.wantID > 0 {
				wantStatus = "database_time_correlated"
				if event.Metadata["identity_basis"] != "username+created_at" {
					t.Fatalf("correlation basis is missing: %+v", event)
				}
			}
			if event.Metadata["identity_status"] != wantStatus {
				t.Fatalf("unexpected identity strength: %+v", event)
			}
			if original.UserID != 0 || len(original.Metadata) != 1 {
				t.Fatalf("original probe payload was mutated: %+v", original)
			}
		})
	}
}

func TestManagedUserDetectsMissingCreationTimeColumn(t *testing.T) {
	state := &managedUserTestDBState{
		record:  managedUserRecord{ID: 7, Username: "alice", Role: 1, Status: 1, CreatedAt: 1800000000},
		columns: []string{"id", "username", "role", "status"},
	}
	s := managedUserLiveTestService(t, state)
	user, err := s.managedUser(context.Background(), 7)
	if err != nil || user.CreatedAtKnown {
		t.Fatalf("missing source column was treated as trusted creation evidence: %+v %v", user, err)
	}
}

func userManagementTestService(t *testing.T) *dashboardService {
	t.Helper()
	location := time.FixedZone("CST", 8*3600)
	s := &dashboardService{
		config: Config{Mode: "demo", Timezone: "Asia/Shanghai", Location: location, QuotaPerUnit: 500000},
		now:    func() time.Time { return time.Unix(1800000000, 0) },
	}
	s.network = newNetworkStore(s)
	s.userManagement = newUserManagementState()
	return s
}

func userManagementTestRouter(s *dashboardService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	registerUserManagementRoutes(router, s, "secret")
	return router
}

func TestUserRoutesUseSameFilteredDatasetForListStatsAndExport(t *testing.T) {
	s := userManagementTestService(t)
	s.userManagement.demo[201] = managedUserRecord{ID: 201, Username: "=cmd()", Role: 1, Status: 1, Group: "default"}
	s.userManagement.demo[202] = managedUserRecord{ID: 202, Username: "quiet", Role: 1, Status: 1, Group: "default"}
	if err := s.network.Add(context.Background(), networkEvent{
		ID: "test-call", Source: "api_call", Result: "failure", OccurredAt: s.now().Unix() - 1, UserID: 201, IP: "8.8.8.8",
	}); err != nil {
		t.Fatal(err)
	}
	router := userManagementTestRouter(s)
	read := func(path string, authorized bool) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		if authorized {
			request.Header.Set("Authorization", "Bearer secret")
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	if response := read("/api/users", false); response.Code != http.StatusUnauthorized {
		t.Fatalf("unprotected user route: %d", response.Code)
	}
	list := read("/api/users?activity=active_7d&page_size=1", true)
	if list.Code != http.StatusOK {
		t.Fatalf("list failed: %d %s", list.Code, list.Body.String())
	}
	var decoded struct {
		Data struct {
			Total int64                `json:"total"`
			Items []userManagementItem `json:"items"`
			Stats map[string]any       `json:"stats"`
		} `json:"data"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Data.Total != 1 || len(decoded.Data.Items) != 1 || decoded.Data.Items[0].ID != 201 {
		t.Fatalf("filter was not applied: %s", list.Body.String())
	}
	statsResponse := read("/api/users/activity-stats?activity=active_7d", true)
	var stats struct {
		Data struct {
			Stats map[string]any `json:"stats"`
		} `json:"data"`
	}
	if err := json.Unmarshal(statsResponse.Body.Bytes(), &stats); err != nil || statsResponse.Code != http.StatusOK {
		t.Fatalf("stats failed: %d %s %v", statsResponse.Code, statsResponse.Body.String(), err)
	}
	if !reflect.DeepEqual(stats.Data.Stats, decoded.Data.Stats) {
		t.Fatalf("list and stats disagree: %+v vs %+v", decoded.Data.Stats, stats.Data.Stats)
	}
	export := read("/api/users/export?activity=active_7d&page=2", true)
	rows, err := csv.NewReader(strings.NewReader(export.Body.String())).ReadAll()
	if err != nil || export.Code != http.StatusOK || len(rows) != 2 {
		t.Fatalf("export failed: %d %s %v", export.Code, export.Body.String(), err)
	}
	if rows[1][0] != "201" || rows[1][1] != "'=cmd()" {
		t.Fatalf("export is paginated, inconsistent or contains an executable formula: %+v", rows)
	}
}

func TestUserFiltersRejectMalformedValues(t *testing.T) {
	s := userManagementTestService(t)
	router := userManagementTestRouter(s)
	for _, query := range []string{
		"?activity=active", "?intensity=malicious", "?status=3", "?sort=password", "?order=DROP",
		"?page=2147483647&page_size=200", "?user_id=-1", "?page_size=1000",
	} {
		request := httptest.NewRequest(http.MethodGet, "/api/users"+query, nil)
		request.Header.Set("Authorization", "Bearer secret")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Errorf("%s = %d %s", query, response.Code, response.Body.String())
		}
	}
}

type managedUserTestDBState struct {
	mu      sync.Mutex
	record  managedUserRecord
	fail    bool
	columns []string
}

type managedUserTestConnector struct{ state *managedUserTestDBState }
type managedUserTestDriver struct{}
type managedUserTestConn struct{ state *managedUserTestDBState }
type managedUserTestRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (c managedUserTestConnector) Connect(context.Context) (driver.Conn, error) {
	return &managedUserTestConn{state: c.state}, nil
}
func (managedUserTestConnector) Driver() driver.Driver { return managedUserTestDriver{} }
func (managedUserTestDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("use connector")
}
func (*managedUserTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is unexpected")
}
func (*managedUserTestConn) Close() error { return nil }
func (*managedUserTestConn) Begin() (driver.Tx, error) {
	return nil, errors.New("direct SQL mutation is unexpected")
}
func (c *managedUserTestConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	if c.state.fail {
		return nil, errors.New("database unavailable")
	}
	columns := c.state.columns
	if columns == nil {
		columns = []string{"id", "username", "display_name", "email", "role", "status", "group", "quota",
			"used_quota", "request_count", "created_at", "last_login_at", "auth_version", "deleted_at"}
	}
	if strings.Contains(query, "WHERE 1 = 0") {
		return &managedUserTestRows{columns: columns}, nil
	}
	record := c.state.record
	return &managedUserTestRows{
		columns: []string{"id", "username", "display_name", "email", "role", "status", "group", "quota",
			"used_quota", "request_count", "created_at", "inviter_id", "last_login_at", "auth_version", "remark"},
		values: [][]driver.Value{{record.ID, record.Username, record.DisplayName, record.Email, record.Role, record.Status,
			record.Group, record.Quota, record.UsedQuota, record.RequestCount, record.CreatedAt, record.InviterID,
			record.LastLoginAt, record.AuthVersion, record.Remark}},
	}, nil
}
func (r *managedUserTestRows) Columns() []string { return r.columns }
func (*managedUserTestRows) Close() error        { return nil }
func (r *managedUserTestRows) Next(values []driver.Value) error {
	if r.index >= len(r.values) {
		return io.EOF
	}
	copy(values, r.values[r.index])
	r.index++
	return nil
}

func managedUserLiveTestService(t *testing.T, state *managedUserTestDBState) *dashboardService {
	t.Helper()
	db := sql.OpenDB(managedUserTestConnector{state: state})
	t.Cleanup(func() { _ = db.Close() })
	return &dashboardService{config: Config{Mode: "live"}, management: queryDB{db: db, driver: "postgres"}}
}

func TestManagedUserStatusUsesUpstreamAndVerifiesOutcome(t *testing.T) {
	state := &managedUserTestDBState{record: managedUserRecord{ID: 7, Username: "user", Role: 1, Status: 1, AuthVersion: 3}}
	s := managedUserLiveTestService(t, state)
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/prefix/api/user/manage" || r.Method != http.MethodPost ||
			r.Header.Get("Authorization") != "Bearer upstream-secret" || r.Header.Get("New-Api-User") != "1" {
			t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var body struct {
			ID     int64  `json:"id"`
			Action string `json:"action"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID != 7 || body.Action != "disable" {
			t.Errorf("unexpected action: %+v, %v", body, err)
		}
		state.mu.Lock()
		state.record.Status = 2
		state.record.AuthVersion++
		state.mu.Unlock()
		_, _ = io.WriteString(w, `{"success":true}`)
	}))
	defer server.Close()
	s.config.NewAPIUpstream, _ = url.Parse(server.URL + "/prefix")
	s.config.NewAPIAdminToken, s.config.NewAPIAdminUserID = "upstream-secret", 1
	if err := s.setManagedUserStatus(context.Background(), 7, false, "reviewed"); err != nil {
		t.Fatal(err)
	}
	state.mu.Lock()
	after := state.record
	state.mu.Unlock()
	if requests.Load() != 1 || after.Status != 2 || after.AuthVersion != 4 {
		t.Fatalf("upstream lifecycle was bypassed: requests=%d record=%+v", requests.Load(), after)
	}
}

func TestManagedUserStatusRejectsBusinessFailureAndUnverifiedSuccess(t *testing.T) {
	for _, result := range []string{`{"success":false,"message":"private-upstream-detail"}`, `{"success":true}`} {
		t.Run(result, func(t *testing.T) {
			state := &managedUserTestDBState{record: managedUserRecord{ID: 7, Role: 1, Status: 1}}
			s := managedUserLiveTestService(t, state)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, result)
			}))
			defer server.Close()
			s.config.NewAPIUpstream, _ = url.Parse(server.URL)
			s.config.NewAPIAdminToken, s.config.NewAPIAdminUserID = "secret", 1
			err := s.setManagedUserStatus(context.Background(), 7, false, "reviewed")
			if err == nil || strings.Contains(err.Error(), "private-upstream-detail") {
				t.Fatalf("business failure or unchanged state accepted: %v", err)
			}
		})
	}
}

func TestManagedUserStatusProtectsAdministratorsAndCredentialsOnRedirect(t *testing.T) {
	state := &managedUserTestDBState{record: managedUserRecord{ID: 1, Role: 100, Status: 1}}
	s := managedUserLiveTestService(t, state)
	if err := s.setManagedUserStatus(context.Background(), 1, false, "reviewed"); err == nil {
		t.Fatal("administrator account could be disabled")
	}
	var leaked atomic.Bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked.Store(true)
		_, _ = io.WriteString(w, `{"success":true}`)
	}))
	defer destination.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	state.record = managedUserRecord{ID: 7, Role: 1, Status: 1}
	s.config.NewAPIUpstream, _ = url.Parse(redirect.URL)
	s.config.NewAPIAdminToken, s.config.NewAPIAdminUserID = "secret", 1
	if err := s.setManagedUserStatus(context.Background(), 7, false, "reviewed"); err == nil || leaked.Load() {
		t.Fatalf("redirect followed or accepted: err=%v leaked=%v", err, leaked.Load())
	}
}

func TestManagedUserStatusVerifiesKnownAuthenticationVersion(t *testing.T) {
	state := &managedUserTestDBState{record: managedUserRecord{ID: 7, Role: 1, Status: 1, AuthVersion: 3}}
	s := managedUserLiveTestService(t, state)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		state.mu.Lock()
		state.record.Status = 2
		state.mu.Unlock()
		_, _ = io.WriteString(w, `{"success":true}`)
	}))
	defer server.Close()
	s.config.NewAPIUpstream, _ = url.Parse(server.URL)
	s.config.NewAPIAdminToken, s.config.NewAPIAdminUserID = "secret", 1
	if err := s.setManagedUserStatus(context.Background(), 7, false, "reviewed"); err == nil {
		t.Fatal("status-only mutation was reported as verified authentication invalidation")
	}
}

func TestManagedUserDemoStatusPreservesLifecycleVersion(t *testing.T) {
	s := userManagementTestService(t)
	before, err := s.managedUser(context.Background(), 101)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.setManagedUserStatus(context.Background(), 101, false, "reviewed"); err != nil {
		t.Fatal(err)
	}
	after, err := s.managedUser(context.Background(), 101)
	if err != nil || after.Status != 2 || after.AuthVersion != before.AuthVersion+1 {
		t.Fatalf("demo transition = %+v %v", after, err)
	}
	if err := s.setManagedUserStatus(context.Background(), 1, false, "reviewed"); err == nil {
		t.Fatal("demo administrator was not protected")
	}
}
