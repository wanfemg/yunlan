package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func networkTestService() *dashboardService {
	return &dashboardService{
		config: Config{Mode: "demo", Timezone: "Asia/Shanghai"},
		now:    func() time.Time { return time.Unix(1795150000, 0) },
	}
}

func networkTestEvent(id, source, ip string, user, token int64) networkEvent {
	return networkEvent{
		ID: id, Source: source, IP: ip, UserID: user, TokenID: token,
		OccurredAt: 1795149900, Result: "success",
	}
}

func addNetworkTestEvent(t *testing.T, store *networkStore, event networkEvent) {
	t.Helper()
	if err := store.Add(context.Background(), event); err != nil {
		t.Fatal(err)
	}
}

func TestNetworkEventsKeepAuthenticationAndCallsSeparate(t *testing.T) {
	service := networkTestService()
	store := newNetworkStore(service)
	for _, source := range []string{networkSourceCall, networkSourceLogin, networkSourceRegistration} {
		event := networkTestEvent("same-id", source, "::ffff:8.8.8.8", 42, 7)
		event.BrowserID, event.SessionID = "browser", "session"
		event.Device = map[string]string{"platform": "desktop"}
		addNetworkTestEvent(t, store, event)
	}
	for _, source := range []string{networkSourceCall, networkSourceLogin, networkSourceRegistration} {
		events, err := store.Events(context.Background(), source, 0, 0)
		if err != nil || len(events) != 1 || events[0].Source != source || events[0].IP != "8.8.8.8" {
			t.Fatalf("%s events = %#v, %v", source, events, err)
		}
		if source == networkSourceCall && (events[0].BrowserID != "" || len(events[0].Device) != 0) {
			t.Fatal("call logs inherited a browser identity")
		}
		data, err := store.Analyze(context.Background(), source, 1795140000, 1795150001, 0, 0, "")
		if err != nil || data.Summary.Events != 1 {
			t.Fatalf("%s analysis = %#v, %v", source, data.Summary, err)
		}
		if source != networkSourceCall && data.Summary.Tokens != 0 {
			t.Fatal("authentication event counted a token")
		}
	}
	if _, err := store.Analyze(context.Background(), "all", 1, 2, 0, 0, ""); err == nil {
		t.Fatal("mixed-source analysis must not be accepted")
	}
}

func TestNetworkImportIdentityIsSourceEventNotRequestID(t *testing.T) {
	store := newNetworkStore(networkTestService())
	first := networkTestEvent("log:1", networkSourceCall, "8.8.8.8", 1, 2)
	first.Metadata = map[string]string{"request_id": "upstream-may-reuse"}
	addNetworkTestEvent(t, store, first)
	addNetworkTestEvent(t, store, first)
	second := first
	second.ID = "log:2"
	addNetworkTestEvent(t, store, second)
	events, err := store.Snapshot(context.Background())
	if err != nil || len(events) != 2 {
		t.Fatalf("repeated delivery versus repeated call = %d, %v", len(events), err)
	}
	first.Result = "failure"
	if err := store.Add(context.Background(), first); err == nil {
		t.Fatal("conflicting replay must be rejected")
	}
}

func TestNetworkGeographyDeduplicatesUsersAndSeparatesUnknownAddresses(t *testing.T) {
	store := newNetworkStore(networkTestService())
	for _, ip := range []string{"1.1.1.1", "8.8.8.8"} {
		if err := store.SaveAttributes(context.Background(), networkIPInfo{
			IP: ip, CountryCode: "CN", Country: "China", DataSource: "test-fixture",
		}); err != nil {
			t.Fatal(err)
		}
	}
	input := []networkEvent{
		networkTestEvent("1", networkSourceCall, "1.1.1.1", 1, 1),
		networkTestEvent("2", networkSourceCall, "8.8.8.8", 1, 1),
		networkTestEvent("3", networkSourceCall, "8.8.8.8", 2, 2),
		networkTestEvent("4", networkSourceCall, "9.9.9.9", 1, 1),
		networkTestEvent("5", networkSourceCall, "192.168.1.2", 1, 1),
		networkTestEvent("6", networkSourceCall, "", 1, 1),
		networkTestEvent("7", networkSourceCall, "not-an-ip", 1, 1),
	}
	for _, event := range input {
		addNetworkTestEvent(t, store, event)
	}
	data, err := store.Analyze(context.Background(), networkSourceCall, 1, 1795150001, 0, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if data.Summary.Events != 7 || data.Summary.Users != 2 || data.Summary.Tokens != 2 {
		t.Fatalf("summary = %#v", data.Summary)
	}
	countries := map[string]networkCountBucket{}
	for _, country := range data.Geography {
		countries[country.Key] = country
	}
	if countries["CN"].Users != 2 || countries["CN"].Events != 3 {
		t.Fatalf("country distinct users = %#v", countries["CN"])
	}
	for _, key := range []string{"unknown", "private", "missing", "invalid"} {
		if countries[key].Events != 1 {
			t.Fatalf("%s bucket = %#v", key, countries[key])
		}
	}
	if data.Coverage["domestic_events"] != int64(3) || data.Coverage["foreign_events"] != int64(0) {
		t.Fatalf("unknown/private classified as foreign: %#v", data.Coverage)
	}
}

func TestNetworkCrossCountryUsersAreNotSummedForGlobalUsers(t *testing.T) {
	store := newNetworkStore(networkTestService())
	for index, country := range []string{"CN", "US"} {
		ip := []string{"1.1.1.1", "8.8.8.8"}[index]
		if err := store.SaveAttributes(context.Background(), networkIPInfo{
			IP: ip, CountryCode: country, DataSource: "test-fixture",
		}); err != nil {
			t.Fatal(err)
		}
		addNetworkTestEvent(t, store, networkTestEvent(country, networkSourceCall, ip, 1, 1))
	}
	data, err := store.Analyze(context.Background(), networkSourceCall, 1, 1795150001, 0, 0, "")
	if err != nil || data.Summary.Users != 1 || len(data.Geography) != 2 ||
		data.Geography[0].Users != 1 || data.Geography[1].Users != 1 {
		t.Fatalf("country/global user counts = %#v / %#v, %v", data.Geography, data.Summary, err)
	}
}

func TestNetworkFullStatisticsIncludeMoreThanThreeThousandIPs(t *testing.T) {
	store := newNetworkStore(networkTestService())
	for index := 0; index < 3501; index++ {
		ip := fmt.Sprintf("11.%d.%d.%d", index/65536, index/256%256, index%256)
		addNetworkTestEvent(t, store, networkTestEvent(fmt.Sprintf("log:%d", index), networkSourceCall, ip, int64(index+1), 1))
	}
	data, err := store.Analyze(context.Background(), networkSourceCall, 1, 1795150001, 0, 0, "")
	if err != nil || data.Summary.Events != 3501 || data.Summary.IPs != 3501 || data.Summary.Users != 3501 || data.SampleMode != "none" {
		t.Fatalf("full statistics = %#v, %v", data.Summary, err)
	}
}

func TestNetworkWindowIsHalfOpenAndDoesNotTrustTaskBilling(t *testing.T) {
	store := newNetworkStore(networkTestService())
	for index, at := range []int64{100, 199, 200} {
		event := networkTestEvent(fmt.Sprint(index), networkSourceCall, "8.8.8.8", 1, 1)
		event.OccurredAt = at
		addNetworkTestEvent(t, store, event)
	}
	events, err := store.Events(context.Background(), networkSourceCall, 100, 200)
	if err != nil || len(events) != 2 {
		t.Fatalf("half-open events = %d, %v", len(events), err)
	}
	for _, other := range []string{`{"is_task":true}`, `{"is_task":"true"}`, `{"is_task":1}`} {
		if !networkTaskSettlement(other) {
			t.Fatalf("task settlement not recognized: %s", other)
		}
	}
	if networkTaskSettlement(`{"is_task":false}`) || networkTaskSettlement("broken") {
		t.Fatal("ordinary calls classified as task settlement")
	}
}

func TestNetworkIPClassesAndProxyUnknownAreExplicit(t *testing.T) {
	for _, test := range []struct{ ip, kind string }{
		{"", "missing"}, {"8.8.8.8", "public"}, {"::ffff:8.8.8.8", "public"},
		{"10.1.1.1", "private"}, {"fd00::1", "private"}, {"127.0.0.1", "loopback"},
		{"169.254.1.1", "link_local"}, {"192.0.2.1", "reserved"}, {"100.64.1.1", "reserved"},
		{"2001:db8::1", "reserved"}, {"bad", "invalid"}, {"fe80::1%eth0", "invalid"},
	} {
		if _, kind := classifyNetworkIP(test.ip); kind != test.kind {
			t.Errorf("%q class = %s, want %s", test.ip, kind, test.kind)
		}
	}
	store := newNetworkStore(networkTestService())
	if err := store.SaveAttributes(context.Background(), networkIPInfo{
		IP: "8.8.8.8", ASN: 15169, NetworkType: "hosting", DataSource: "test-fixture",
	}); err != nil {
		t.Fatal(err)
	}
	info, err := store.Lookup(context.Background(), "8.8.8.8")
	if err != nil || info.Proxy["vpn"] != "unknown" || info.Proxy["public_proxy"] != "unknown" || info.ASN != 15169 {
		t.Fatalf("hosting should not imply a proxy: %#v, %v", info, err)
	}
	if err := store.SaveAttributes(context.Background(), networkIPInfo{
		IP: "8.8.8.8", DataSource: "test", Proxy: map[string]string{"vpn": "maybe"},
	}); err == nil {
		t.Fatal("unrecognized proxy state was accepted")
	}
	if err := store.Add(context.Background(), networkEvent{
		ID: "secret", Source: networkSourceLogin, OccurredAt: 1, Result: "success",
		Metadata: map[string]string{"password": "never-store"},
	}); err == nil {
		t.Fatal("credentials accepted into network attributes")
	}
}

func TestNetworkRoutesAuthenticateAndDatabaseFailuresDoNotReturnZero(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := networkTestService()
	service.config.Mode = "live"
	router := gin.New()
	registerIPAnalysisRoutes(router, service, "secret")
	for _, target := range []string{"/api/ip/stats", "/api/ip/distribution?source=login", "/api/ip/lookup/8.8.8.8"} {
		unauthorized := httptest.NewRecorder()
		router.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, target, nil))
		if unauthorized.Code != http.StatusUnauthorized {
			t.Fatalf("%s unauthorized = %d", target, unauthorized.Code)
		}
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.Header.Set("Authorization", "Bearer secret")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), `"success":true`) {
			t.Fatalf("%s missing database = %d %s", target, response.Code, response.Body.String())
		}
	}
	service.config.Mode = "demo"
	for _, target := range []string{
		"/api/ip/stats?source=all", "/api/ip/stats?period=bad",
		"/api/ip/stats?source=login&token_id=1", "/api/ip/stats?start=50&end=1",
	} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.Header.Set("Authorization", "Bearer secret")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s invalid filter = %d", target, response.Code)
		}
	}
}

type networkDBLog struct {
	id, at, user, token, kind, quota int64
	ip, other, model, request        string
}

type networkDBState struct {
	mu                  sync.Mutex
	events, meta, attrs map[string]string
	logs                []networkDBLog
	queryErr, commitErr error
	columns             []string
	lastLogSelect       string
}

func newNetworkDBState() *networkDBState {
	return &networkDBState{events: map[string]string{}, meta: map[string]string{}, attrs: map[string]string{}}
}

var networkDBCounter atomic.Int64

type networkDBDriver struct{ state *networkDBState }
type networkDBConn struct {
	state               *networkDBState
	events, meta, attrs map[string]string
	inTx                bool
}
type networkDBTx struct{ conn *networkDBConn }
type networkDBRows struct {
	columns []string
	values  [][]driver.Value
}

func (d networkDBDriver) Open(string) (driver.Conn, error) {
	return &networkDBConn{state: d.state}, nil
}
func (c *networkDBConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not used") }
func (c *networkDBConn) Close() error                        { return nil }
func (c *networkDBConn) Begin() (driver.Tx, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	copyMap := func(source map[string]string) map[string]string {
		result := map[string]string{}
		for key, value := range source {
			result[key] = value
		}
		return result
	}
	c.events, c.meta, c.attrs, c.inTx = copyMap(c.state.events), copyMap(c.state.meta), copyMap(c.state.attrs), true
	return &networkDBTx{conn: c}, nil
}
func (tx *networkDBTx) Commit() error {
	c := tx.conn
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	c.inTx = false
	if c.state.commitErr != nil {
		return c.state.commitErr
	}
	c.state.events, c.state.meta, c.state.attrs = c.events, c.meta, c.attrs
	return nil
}
func (tx *networkDBTx) Rollback() error       { tx.conn.inTx = false; return nil }
func (rows *networkDBRows) Columns() []string { return rows.columns }
func (rows *networkDBRows) Close() error      { return nil }
func (rows *networkDBRows) Next(values []driver.Value) error {
	if len(rows.values) == 0 {
		return io.EOF
	}
	copy(values, rows.values[0])
	rows.values = rows.values[1:]
	return nil
}
func (c *networkDBConn) maps() (map[string]string, map[string]string, map[string]string) {
	if c.inTx {
		return c.events, c.meta, c.attrs
	}
	return c.state.events, c.state.meta, c.state.attrs
}
func (c *networkDBConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	events, meta, attrs := c.maps()
	if strings.HasPrefix(query, "CREATE") {
		return driver.RowsAffected(0), nil
	}
	if strings.Contains(query, "INSERT INTO siteadmin_network_events") {
		count := int64(0)
		for index := 0; index < len(args); index += 8 {
			key, payload := args[index].Value.(string), args[index+7].Value.(string)
			if _, exists := events[key]; !exists {
				events[key] = payload
				count++
			}
		}
		return driver.RowsAffected(count), nil
	}
	if strings.Contains(query, "INSERT INTO siteadmin_network_meta") {
		key, value := args[0].Value.(string), args[1].Value.(string)
		if strings.Contains(query, "GREATEST") && key == "call_cursor" {
			old, _ := strconv.ParseInt(meta[key], 10, 64)
			next, _ := strconv.ParseInt(value, 10, 64)
			if old > next {
				value = meta[key]
			}
		}
		meta[key] = value
		return driver.RowsAffected(1), nil
	}
	if strings.Contains(query, "INSERT INTO siteadmin_ip_enrichment") {
		attrs[args[0].Value.(string)] = args[1].Value.(string)
		return driver.RowsAffected(1), nil
	}
	return nil, fmt.Errorf("unexpected SQL: %s", query)
}
func (c *networkDBConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	if c.state.queryErr != nil {
		return nil, c.state.queryErr
	}
	events, meta, attrs := c.maps()
	if query == "SELECT * FROM logs WHERE 1=0" {
		columns := c.state.columns
		if columns == nil {
			columns = []string{"id", "created_at", "user_id", "token_id", "ip", "type", "other", "quota", "model_name", "request_id"}
		}
		return &networkDBRows{columns: columns}, nil
	}
	if strings.Contains(query, "MAX(id)") {
		maximum := int64(0)
		for _, row := range c.state.logs {
			if row.id > maximum {
				maximum = row.id
			}
		}
		return &networkDBRows{columns: []string{"max"}, values: [][]driver.Value{{maximum}}}, nil
	}
	if strings.Contains(query, "FROM logs WHERE id>") {
		c.state.lastLogSelect = query
		if c.state.columns != nil && strings.Contains(query, "COALESCE(request_id") {
			hasRequestID := false
			for _, name := range c.state.columns {
				hasRequestID = hasRequestID || name == "request_id"
			}
			if !hasRequestID {
				return nil, errors.New("unknown column request_id")
			}
		}
		result := &networkDBRows{columns: []string{"id", "created_at", "user_id", "token_id", "ip", "type", "other", "quota", "model_name", "request_id"}}
		for _, row := range c.state.logs {
			if row.id > args[0].Value.(int64) && row.id <= args[1].Value.(int64) && (row.kind == 2 || row.kind == 5) {
				result.values = append(result.values, []driver.Value{row.id, row.at, row.user, row.token, row.ip, row.kind, row.other, row.quota, row.model, row.request})
			}
		}
		if len(result.values) > 1000 {
			result.values = result.values[:1000]
		}
		return result, nil
	}
	if strings.Contains(query, "siteadmin_network_meta WHERE name=") {
		rows := &networkDBRows{columns: []string{"payload"}}
		if value, exists := meta[args[0].Value.(string)]; exists {
			rows.values = [][]driver.Value{{value}}
		}
		return rows, nil
	}
	if strings.Contains(query, "FROM siteadmin_ip_enrichment") {
		rows := &networkDBRows{columns: []string{"payload"}}
		for _, value := range attrs {
			rows.values = append(rows.values, []driver.Value{value})
		}
		return rows, nil
	}
	if strings.Contains(query, "SELECT source,COUNT(*) FROM siteadmin_network_events GROUP BY source") {
		counts := map[string]int64{}
		for _, value := range events {
			var event networkEvent
			_ = json.Unmarshal([]byte(value), &event)
			counts[event.Source]++
		}
		rows := &networkDBRows{columns: []string{"source", "count"}}
		for source, count := range counts {
			rows.values = append(rows.values, []driver.Value{source, count})
		}
		return rows, nil
	}
	if strings.Contains(query, "FROM siteadmin_network_events WHERE id") {
		withID := strings.Contains(query, "SELECT id,payload")
		columns := []string{"payload"}
		if withID {
			columns = []string{"id", "payload"}
		}
		rows := &networkDBRows{columns: columns}
		for _, arg := range args {
			key := arg.Value.(string)
			if value, exists := events[key]; exists {
				item := []driver.Value{value}
				if withID {
					item = []driver.Value{key, value}
				}
				rows.values = append(rows.values, item)
			}
		}
		return rows, nil
	}
	if strings.Contains(query, "FROM siteadmin_network_events WHERE occurred_at") {
		rows := &networkDBRows{columns: []string{"payload"}}
		start, source, end, index := args[0].Value.(int64), "", int64(0), 1
		if strings.Contains(query, "AND source=") {
			source = args[index].Value.(string)
			index++
		}
		if strings.Contains(query, "AND occurred_at <") {
			end = args[index].Value.(int64)
		}
		for _, value := range events {
			var event networkEvent
			_ = json.Unmarshal([]byte(value), &event)
			if event.OccurredAt >= start && (source == "" || event.Source == source) && (end == 0 || event.OccurredAt < end) {
				rows.values = append(rows.values, []driver.Value{value})
			}
		}
		return rows, nil
	}
	return nil, fmt.Errorf("unexpected SQL: %s", query)
}

func networkTestDB(t *testing.T, state *networkDBState) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("network-test-%d", networkDBCounter.Add(1))
	sql.Register(name, networkDBDriver{state: state})
	db, err := sql.Open(name, "")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestNetworkCallImportPersistsCursorAndExcludesTaskSettlement(t *testing.T) {
	state := newNetworkDBState()
	state.logs = []networkDBLog{
		{id: 1, at: 100, user: 1, token: 2, kind: 2, ip: "8.8.8.8", other: "{}", request: "same", quota: 10},
		{id: 2, at: 110, user: 1, token: 2, kind: 5, ip: "1.1.1.1", other: "{}", request: "same"},
		{id: 3, at: 120, user: 1, token: 2, kind: 2, ip: "", other: `{"is_task":true}`, quota: 99},
		{id: 4, at: 130, user: 1, kind: 7, ip: "9.9.9.9", other: "{}"},
	}
	db := networkTestDB(t, state)
	service := networkTestService()
	service.config.Mode = "live"
	service.management, service.logs = queryDB{db: db, driver: "postgres"}, queryDB{db: db, driver: "postgres"}
	store := newNetworkStore(service)
	status, err := store.SyncCalls(context.Background())
	if err != nil || status["imported"] != 2 || status["skipped_settlements"] != 1 || status["cursor"] != int64(4) {
		t.Fatalf("import = %#v, %v", status, err)
	}
	// A different store instance must recover data and cursor from persistent storage.
	restarted := newNetworkStore(service)
	status, err = restarted.SyncCalls(context.Background())
	if err != nil || status["imported"] != 0 {
		t.Fatalf("restart import = %#v, %v", status, err)
	}
	events, err := restarted.Snapshot(context.Background())
	sort.Slice(events, func(i, j int) bool { return events[i].ID < events[j].ID })
	if err != nil || len(events) != 2 || events[0].Result != "success" || events[1].Result != "failure" {
		t.Fatalf("persisted events = %#v, %v", events, err)
	}
	metadata, err := restarted.Metadata(context.Background())
	if err != nil || metadata["history_complete"] != false || metadata["call_cursor"] != "4" {
		t.Fatalf("history metadata = %#v, %v", metadata, err)
	}
}

func TestNetworkImportCommitFailureDoesNotAdvanceCursor(t *testing.T) {
	state := newNetworkDBState()
	state.logs = []networkDBLog{{id: 1, at: 100, user: 1, kind: 2, ip: "8.8.8.8", other: "{}"}}
	state.commitErr = errors.New("commit failed")
	db := networkTestDB(t, state)
	service := networkTestService()
	service.config.Mode = "live"
	service.management, service.logs = queryDB{db: db, driver: "mysql"}, queryDB{db: db, driver: "mysql"}
	store := newNetworkStore(service)
	if _, err := store.SyncCalls(context.Background()); err == nil {
		t.Fatal("commit failure must surface")
	}
	if len(state.events) != 0 || state.meta["call_cursor"] != "" {
		t.Fatalf("failed transaction advanced import: %#v %#v", state.events, state.meta)
	}
	state.commitErr = nil
	if _, err := store.SyncCalls(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(state.events) != 1 || state.meta["call_cursor"] != "1" {
		t.Fatalf("recovery failed: %#v %#v", state.events, state.meta)
	}
}

func TestNetworkReadErrorIsNotAnEmptySuccessfulResult(t *testing.T) {
	state := newNetworkDBState()
	state.queryErr = errors.New("source unavailable")
	service := networkTestService()
	service.config.Mode = "live"
	service.management = queryDB{db: networkTestDB(t, state), driver: "postgres"}
	store := newNetworkStore(service)
	if _, err := store.Events(context.Background(), networkSourceLogin, 1, 1000); err == nil {
		t.Fatal("database error became an empty result")
	}
}

func TestNetworkLegacyLogSchemaDoesNotRequireRequestID(t *testing.T) {
	state := newNetworkDBState()
	state.columns = []string{"id", "created_at", "user_id", "token_id", "ip", "type", "other", "quota", "model_name"}
	state.logs = []networkDBLog{{id: 1, at: 100, user: 1, kind: 2, ip: "8.8.8.8", other: "{}"}}
	db := networkTestDB(t, state)
	service := networkTestService()
	service.config.Mode = "live"
	service.management, service.logs = queryDB{db: db, driver: "mysql"}, queryDB{db: db, driver: "mysql"}
	store := newNetworkStore(service)
	if _, err := store.SyncCalls(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(state.lastLogSelect, "request_id") {
		t.Fatalf("legacy SELECT references a nonexistent column: %s", state.lastLogSelect)
	}
	events, err := store.Snapshot(context.Background())
	if err != nil || len(events) != 1 {
		t.Fatalf("legacy event import = %#v, %v", events, err)
	}
	metadata, err := store.Metadata(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	capabilities := metadata["call_source_capabilities"].(map[string]bool)
	if capabilities["request_id"] || !capabilities["ip"] || !capabilities["task_settlement_filter"] {
		t.Fatalf("legacy capabilities = %#v", capabilities)
	}
}

func TestNetworkRegionalGroupingUsesCountryRegionAndCityIdentity(t *testing.T) {
	store := newNetworkStore(networkTestService())
	for index, region := range []string{"Region A", "Region B"} {
		ip := []string{"1.1.1.1", "8.8.8.8"}[index]
		if err := store.SaveAttributes(context.Background(), networkIPInfo{
			IP: ip, CountryCode: "US", Region: region, City: "Springfield", DataSource: "test",
		}); err != nil {
			t.Fatal(err)
		}
		addNetworkTestEvent(t, store, networkTestEvent(region, networkSourceCall, ip, 1, 1))
	}
	data, err := store.Analyze(context.Background(), networkSourceCall, 1, 1795150001, 0, 0, "")
	if err != nil || len(data.Regions) != 2 || len(data.Cities) != 2 ||
		data.Geography[0].Users != 1 || data.Regions[0].Users != 1 || data.Cities[0].Users != 1 {
		t.Fatalf("region/city aggregation = %#v / %#v, %v", data.Regions, data.Cities, err)
	}
}

func TestNetworkProbeCoverageIsUnknownUntilIndependentlyMeasured(t *testing.T) {
	service := networkTestService()
	service.config.NetworkHistoryComplete = true
	store := newNetworkStore(service)
	data, err := store.Analyze(context.Background(), networkSourceLogin, 1, 1795150001, 0, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	sources := data.Metadata["event_sources"].(map[string]map[string]any)
	if sources[networkSourceLogin]["status"] != "not_observed" ||
		sources[networkSourceLogin]["coverage"] != nil ||
		data.Metadata["probe_history_complete"] != false || data.Completeness != "no_observed_events" {
		t.Fatalf("unobserved probe source reported complete: %#v", data)
	}
	event := networkTestEvent("failure", networkSourceLogin, "8.8.8.8", 0, 0)
	event.Result, event.ClaimedAccount = "failure", "claimed"
	addNetworkTestEvent(t, store, event)
	data, err = store.Analyze(context.Background(), networkSourceLogin, 1, 1795150001, 0, 0, "")
	if err != nil || data.Summary.Users != 0 || data.Summary.Failure != 1 {
		t.Fatalf("claimed account attributed as authenticated: %#v, %v", data.Summary, err)
	}
}

func TestNetworkFindEventUsesSourceNamespaceAndReturnsIndependentData(t *testing.T) {
	store := newNetworkStore(networkTestService())
	event := networkTestEvent("shared-id", networkSourceLogin, "8.8.8.8", 1, 0)
	event.Metadata = map[string]string{"probe_body_digest": "body-digest"}
	addNetworkTestEvent(t, store, event)
	found, exists, err := store.FindEvent(context.Background(), networkSourceLogin, event.ID)
	if err != nil || !exists || found.Metadata["probe_body_digest"] != "body-digest" {
		t.Fatalf("find probe event = %#v, %v, %v", found, exists, err)
	}
	found.Metadata["probe_body_digest"] = "changed-by-caller"
	reloaded, exists, err := store.FindEvent(context.Background(), networkSourceLogin, event.ID)
	if err != nil || !exists || reloaded.Metadata["probe_body_digest"] != "body-digest" {
		t.Fatal("caller mutated stored probe identity")
	}
	if _, exists, err := store.FindEvent(context.Background(), networkSourceRegistration, event.ID); err != nil || exists {
		t.Fatal("find matched a different event source")
	}
}

func TestNetworkCursorCannotRegressAcrossStoreInstances(t *testing.T) {
	state := newNetworkDBState()
	db := networkTestDB(t, state)
	service := networkTestService()
	service.config.Mode = "live"
	service.management = queryDB{db: db, driver: "postgres"}
	first, second := newNetworkStore(service), newNetworkStore(service)
	for _, store := range []*networkStore{first, second} {
		if err := store.ensure(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if err := first.saveCallBatch(context.Background(), nil, 200); err != nil {
		t.Fatal(err)
	}
	if err := second.saveCallBatch(context.Background(), nil, 100); err != nil {
		t.Fatal(err)
	}
	if state.meta["call_cursor"] != "200" {
		t.Fatalf("late importer regressed cursor to %q", state.meta["call_cursor"])
	}
}

func TestNetworkUnavailableOptionalDatabasesDoNotInventProxyClassification(t *testing.T) {
	service := networkTestService()
	service.config.GeoISPPath = "missing-test-isp.mmdb"
	service.config.GeoAnonymousPath = "missing-test-anonymous.mmdb"
	store := newNetworkStore(service)
	addNetworkTestEvent(t, store, networkTestEvent("1", networkSourceCall, "8.8.8.8", 1, 1))
	data, err := store.Analyze(context.Background(), networkSourceCall, 1, 1795150001, 0, 0, "")
	if err != nil || data.Metadata["isp_available"] != false ||
		data.Metadata["proxy_database_available"] != false || len(data.Warnings) < 2 {
		t.Fatalf("optional source failure = %#v, %v", data.Metadata, err)
	}
	info := data.Enrichment["8.8.8.8"]
	if info.Proxy["vpn"] != "unknown" || info.Proxy["public_proxy"] != "unknown" || info.NetworkType != "unknown" {
		t.Fatalf("missing database invented a classification: %#v", info)
	}
}

// Fixtures are obtained separately from MaxMind/MaxMind-DB's test-data directory.
// This test never downloads databases or reads production database credentials.
func TestNetworkOfficialMMDBIntegration(t *testing.T) {
	fixtures := os.Getenv("NETWORK_TEST_MMDB_DIR")
	if fixtures == "" {
		t.Skip("NETWORK_TEST_MMDB_DIR is unset; official MaxMind test fixtures were not supplied")
	}
	files := map[string]string{
		"city":      "GeoIP2-City-Test.mmdb",
		"asn":       "GeoLite2-ASN-Test.mmdb",
		"isp":       "GeoIP2-ISP-Test.mmdb",
		"anonymous": "GeoIP2-Anonymous-IP-Test.mmdb",
	}
	for name, file := range files {
		if info, err := os.Stat(filepath.Join(fixtures, file)); err != nil || info.Size() == 0 {
			t.Fatalf("%s fixture is missing or empty: %s", name, file)
		}
	}
	service := networkTestService()
	service.config.GeoCityPath = filepath.Join(fixtures, files["city"])
	service.config.GeoASNPath = filepath.Join(fixtures, files["asn"])
	service.config.GeoISPPath = filepath.Join(fixtures, files["isp"])
	service.config.GeoAnonymousPath = filepath.Join(fixtures, files["anonymous"])
	store := newNetworkStore(service)
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close official MMDB fixtures: %v", err)
		}
	})
	ctx := context.Background()

	t.Run("country_region_city", func(t *testing.T) {
		info, err := store.Lookup(ctx, "81.2.69.142")
		if err != nil || info.CountryCode != "GB" || info.Region == "" || info.City == "" ||
			info.FieldSources["geography"] != "offline_maxmind_city" || info.GeoVersion == "" {
			t.Fatalf("official City fixture lookup = %#v, %v", info, err)
		}
		t.Logf("official synthetic fixture: 81.2.69.142 country=%s region=%s city=%s", info.CountryCode, info.Region, info.City)
	})
	t.Run("asn_and_isp", func(t *testing.T) {
		info, err := store.Lookup(ctx, "1.128.0.1")
		if err != nil || info.ASN != 1221 || info.ASOrg != "Telstra Pty Ltd" || info.ISP != "Telstra Internet" ||
			info.FieldSources["asn"] != "offline_maxmind_asn" || info.FieldSources["isp"] != "offline_maxmind_isp" ||
			info.ASNVersion == "" || info.ISPVersion == "" {
			t.Fatalf("official ASN/ISP fixture lookup = %#v, %v", info, err)
		}
		t.Logf("official synthetic fixture: 1.128.0.1 ASN=%d organization=%s ISP=%s", info.ASN, info.ASOrg, info.ISP)
	})
	t.Run("anonymous_matches", func(t *testing.T) {
		info, err := store.Lookup(ctx, "81.2.69.142")
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"vpn", "public_proxy", "residential_proxy", "tor"} {
			if info.Proxy[key] != "yes" || info.FieldSources["proxy_"+key] != "offline_maxmind_anonymous" {
				t.Fatalf("official Anonymous fixture %s = %#v", key, info)
			}
		}
		if info.NetworkType != "hosting" || info.ProxyVersion == "" {
			t.Fatalf("official Anonymous fixture network/version = %#v", info)
		}
		t.Log("official synthetic fixture: 81.2.69.142 matched VPN, public proxy, residential proxy and Tor")
	})
	t.Run("hosting_does_not_imply_proxy", func(t *testing.T) {
		info, err := store.Lookup(ctx, "71.160.223.1")
		if err != nil || info.NetworkType != "hosting" {
			t.Fatalf("official hosting fixture = %#v, %v", info, err)
		}
		for _, key := range []string{"vpn", "public_proxy", "residential_proxy", "tor"} {
			if info.Proxy[key] != "no" {
				t.Fatalf("hosting invented a proxy match: %s = %q", key, info.Proxy[key])
			}
		}
		t.Log("official synthetic fixture: 71.160.223.1 is hosting; all four proxy flags are no")
	})
	t.Run("unmatched_address_stays_unknown", func(t *testing.T) {
		info, err := store.Lookup(ctx, "8.8.8.8")
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"vpn", "public_proxy", "residential_proxy", "tor"} {
			if info.Proxy[key] != "unknown" {
				t.Fatalf("missing Anonymous record became non-proxy: %s = %q", key, info.Proxy[key])
			}
		}
		t.Log("official synthetic fixture: 8.8.8.8 has no Anonymous classification; proxy attributes remain unknown")
	})
	t.Run("manual_attribute_overrides_are_scoped", func(t *testing.T) {
		if err := store.SaveAttributes(ctx, networkIPInfo{
			IP: "81.2.69.142", DataSource: "operator-test-source",
			Proxy: map[string]string{"vpn": "no"},
		}); err != nil {
			t.Fatal(err)
		}
		info, err := store.Lookup(ctx, "81.2.69.142")
		if err != nil || info.Proxy["vpn"] != "no" || info.Proxy["public_proxy"] != "yes" ||
			info.FieldSources["proxy_vpn"] != "operator-test-source" ||
			info.FieldSources["proxy_public_proxy"] != "offline_maxmind_anonymous" ||
			info.NetworkType != "hosting" || info.CountryCode != "GB" {
			t.Fatalf("manual attributes erased unrelated MMDB evidence: %#v, %v", info, err)
		}
	})
}
