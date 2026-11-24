package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/oschwald/maxminddb-golang"
)

const (
	networkSourceCall         = "api_call"
	networkSourceLogin        = "login"
	networkSourceRegistration = "registration"
)

// Authentication events are supplied by the authenticated probe collector.
// Call events are imported exclusively from NewAPI's usage log.
type networkEvent struct {
	ID             string            `json:"id"`
	Source         string            `json:"source"`
	OccurredAt     int64             `json:"occurred_at"`
	UserID         int64             `json:"user_id"`
	TokenID        int64             `json:"token_id,omitempty"`
	IP             string            `json:"ip"`
	PeerIP         string            `json:"peer_ip,omitempty"`
	Result         string            `json:"result"`
	BrowserID      string            `json:"browser_id,omitempty"`
	SessionID      string            `json:"session_id,omitempty"`
	Method         string            `json:"method,omitempty"`
	ClaimedAccount string            `json:"claimed_account,omitempty"`
	UserAgent      string            `json:"user_agent,omitempty"`
	Device         map[string]string `json:"device,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
}

type networkIPInfo struct {
	IP           string                     `json:"ip"`
	AddressClass string                     `json:"address_class"`
	CountryCode  string                     `json:"country_code,omitempty"`
	Country      string                     `json:"country,omitempty"`
	Region       string                     `json:"region,omitempty"`
	City         string                     `json:"city,omitempty"`
	ASN          uint                       `json:"asn,omitempty"`
	ASOrg        string                     `json:"as_org,omitempty"`
	ISP          string                     `json:"isp,omitempty"`
	NetworkType  string                     `json:"network_type"`
	Proxy        map[string]string          `json:"proxy"`
	DataSource   string                     `json:"data_source"`
	UpdatedAt    int64                      `json:"updated_at"`
	GeoVersion   string                     `json:"geo_version,omitempty"`
	ASNVersion   string                     `json:"asn_version,omitempty"`
	ISPVersion   string                     `json:"isp_version,omitempty"`
	ProxyVersion string                     `json:"proxy_version,omitempty"`
	FieldSources map[string]string          `json:"field_sources"`
	Intelligence *networkIntelligenceResult `json:"intelligence,omitempty"`
}

type networkStore struct {
	service          *dashboardService
	mu               sync.Mutex
	syncMu           sync.Mutex
	ready            bool
	events           map[string]networkEvent
	meta             map[string]string
	attrs            map[string]networkIPInfo
	attrsAt          time.Time
	city             *maxminddb.Reader
	asn              *maxminddb.Reader
	isp              *maxminddb.Reader
	anonymous        *maxminddb.Reader
	geoOnce          sync.Once
	geoError         []string
	intelligenceOnce sync.Once
	intelligence     *networkIntelligenceStore
	intelligenceMu   sync.RWMutex
	geoMu            sync.RWMutex
	revision         atomic.Uint64
	background       networkBackground
	maintenance      networkMaintenance
	attributeMu      sync.Mutex
	attributeAudit   []networkAttributeAudit
}

func newNetworkStore(service *dashboardService) *networkStore {
	store := &networkStore{
		service: service, events: map[string]networkEvent{}, meta: map[string]string{},
		attrs: map[string]networkIPInfo{},
	}
	store.background.wake = make(chan struct{}, 1)
	return store
}

func (n *networkStore) clock() time.Time {
	if n.service.now != nil {
		return n.service.now()
	}
	return time.Now()
}

func validNetworkSource(source string) bool {
	return source == networkSourceCall || source == networkSourceLogin || source == networkSourceRegistration
}

func (n *networkStore) ensure(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.ready {
		return nil
	}
	if n.service.config.Mode == "demo" {
		n.ready = true
		return nil
	}
	db := n.service.management
	if db.db == nil {
		return errors.New("network management database is not configured")
	}
	payloadType := "TEXT"
	if db.driver == "mysql" {
		payloadType = "LONGTEXT"
	}
	eventsDDL := `CREATE TABLE IF NOT EXISTS siteadmin_network_events (
		id VARCHAR(160) PRIMARY KEY, source VARCHAR(20) NOT NULL, occurred_at BIGINT NOT NULL,
		user_id BIGINT NOT NULL, token_id BIGINT NOT NULL, ip VARCHAR(255) NOT NULL,
		result VARCHAR(16) NOT NULL, payload ` + payloadType + ` NOT NULL`
	if db.driver == "mysql" {
		eventsDDL += `, INDEX siteadmin_net_source_time (source, occurred_at),
			INDEX siteadmin_net_user_time (user_id, occurred_at)`
	}
	eventsDDL += ")"
	for _, statement := range []string{
		eventsDDL,
		`CREATE TABLE IF NOT EXISTS siteadmin_network_meta (name VARCHAR(64) PRIMARY KEY, payload TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS siteadmin_ip_enrichment (ip VARCHAR(64) PRIMARY KEY, payload TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS siteadmin_network_audit (id VARCHAR(80) PRIMARY KEY,
			kind VARCHAR(32) NOT NULL, subject VARCHAR(128) NOT NULL, occurred_at BIGINT NOT NULL, payload ` + payloadType + ` NOT NULL)`,
	} {
		if _, err := db.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize network event storage: %w", err)
		}
	}
	if db.driver == "postgres" {
		for _, statement := range []string{
			`CREATE INDEX IF NOT EXISTS siteadmin_net_source_time ON siteadmin_network_events (source, occurred_at)`,
			`CREATE INDEX IF NOT EXISTS siteadmin_net_user_time ON siteadmin_network_events (user_id, occurred_at)`,
		} {
			if _, err := db.db.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("initialize network event indexes: %w", err)
			}
		}
	}
	if err := ensureNetworkInvestigationIndexes(ctx, db); err != nil {
		return err
	}
	n.ready = true
	return nil
}

func normalizeNetworkEvent(event networkEvent) (networkEvent, error) {
	if !validNetworkSource(event.Source) {
		return event, errors.New("invalid network event source")
	}
	if event.ID == "" || len(event.ID) > 128 || !validLogFilter(event.ID, 128) ||
		event.OccurredAt <= 0 || event.UserID < 0 || event.TokenID < 0 {
		return event, errors.New("invalid network event identity or time")
	}
	switch event.Result {
	case "success", "failure", "pending":
	default:
		return event, errors.New("invalid network event result")
	}
	for _, field := range []struct {
		value string
		max   int
	}{
		{event.IP, 255}, {event.PeerIP, 255}, {event.BrowserID, 128}, {event.SessionID, 128},
		{event.Method, 64}, {event.ClaimedAccount, 128}, {event.UserAgent, 1024},
	} {
		if !utf8.ValidString(field.value) || len(field.value) > field.max || strings.ContainsRune(field.value, 0) {
			return event, errors.New("invalid network event field")
		}
	}
	for _, values := range []map[string]string{event.Device, event.Metadata} {
		if len(values) > 40 {
			return event, errors.New("too many network event attributes")
		}
		for key, value := range values {
			if !validLogFilter(key, 64) || !validLogFilter(value, 2048) {
				return event, errors.New("invalid network event attribute")
			}
			lower := strings.ToLower(key)
			if strings.Contains(lower, "password") || strings.Contains(lower, "cookie") ||
				strings.Contains(lower, "authorization") || strings.Contains(lower, "access_token") ||
				strings.Contains(lower, "refresh_token") || strings.Contains(lower, "credential") ||
				strings.Contains(lower, "api_key") || strings.Contains(lower, "secret") ||
				lower == "token" || lower == "otp" || lower == "id_token" {
				return event, errors.New("credentials must not be stored as network attributes")
			}
		}
	}
	event.IP = strings.TrimSpace(event.IP)
	event.PeerIP = strings.TrimSpace(event.PeerIP)
	if ip, err := netip.ParseAddr(event.IP); err == nil {
		event.IP = ip.Unmap().String()
	}
	if event.Source == networkSourceCall {
		if event.Result == "pending" {
			return event, errors.New("usage log events cannot be pending authentication")
		}
		event.BrowserID, event.SessionID, event.ClaimedAccount = "", "", ""
		event.Device = nil
	} else {
		event.TokenID = 0
	}
	return event, nil
}

func networkEventKey(event networkEvent) string { return event.Source + ":" + event.ID }

func (n *networkStore) Add(ctx context.Context, event networkEvent) error {
	var err error
	event, err = normalizeNetworkEvent(event)
	if err != nil {
		return err
	}
	if err := n.ensure(ctx); err != nil {
		return err
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if len(payload) > 60000 {
		return errors.New("network event is too large")
	}
	key := networkEventKey(event)
	if n.service.config.Mode == "demo" {
		n.mu.Lock()
		defer n.mu.Unlock()
		if old, exists := n.events[key]; exists {
			encoded, _ := json.Marshal(old)
			if string(encoded) != string(payload) {
				return errors.New("network event ID already has different content")
			}
			return nil
		}
		var clone networkEvent
		_ = json.Unmarshal(payload, &clone)
		n.events[key] = clone
		n.revision.Add(1)
		return nil
	}
	query := `INSERT INTO siteadmin_network_events
		(id,source,occurred_at,user_id,token_id,ip,result,payload) VALUES (?,?,?,?,?,?,?,?)`
	if n.service.management.driver == "postgres" {
		query += " ON CONFLICT (id) DO NOTHING"
	} else {
		query += " ON DUPLICATE KEY UPDATE id=id"
	}
	_, err = n.service.management.db.ExecContext(ctx, bindQuery(n.service.management.driver, query),
		key, event.Source, event.OccurredAt, event.UserID, event.TokenID, event.IP, event.Result, string(payload))
	if err != nil {
		return fmt.Errorf("save network event: %w", err)
	}
	var stored string
	if err := n.service.management.queryRow(ctx, "SELECT payload FROM siteadmin_network_events WHERE id=?", key).Scan(&stored); err != nil {
		return fmt.Errorf("verify network event: %w", err)
	}
	if stored != string(payload) {
		return errors.New("network event ID already has different content")
	}
	n.revision.Add(1)
	return nil
}

func (n *networkStore) FindEvent(ctx context.Context, source, id string) (networkEvent, bool, error) {
	var event networkEvent
	if !validNetworkSource(source) || id == "" || len(id) > 128 {
		return event, false, errors.New("invalid source event identity")
	}
	if err := n.ensure(ctx); err != nil {
		return event, false, err
	}
	key := source + ":" + id
	var payload []byte
	if n.service.config.Mode == "demo" {
		n.mu.Lock()
		value, found := n.events[key]
		if found {
			payload, _ = json.Marshal(value)
		}
		n.mu.Unlock()
		if !found {
			return event, false, nil
		}
	} else {
		var value string
		err := n.service.management.queryRow(ctx, "SELECT payload FROM siteadmin_network_events WHERE id=?", key).Scan(&value)
		if errors.Is(err, sql.ErrNoRows) {
			return event, false, nil
		}
		if err != nil {
			return event, false, err
		}
		payload = []byte(value)
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return event, false, errors.New("stored network event is invalid")
	}
	return event, true, nil
}

func (n *networkStore) Events(ctx context.Context, source string, start, end int64) ([]networkEvent, error) {
	if source != "" && !validNetworkSource(source) {
		return nil, errors.New("invalid network event source")
	}
	if start < 0 || end < 0 || (end != 0 && end <= start) {
		return nil, errors.New("invalid network event window")
	}
	if err := n.ensure(ctx); err != nil {
		return nil, err
	}
	items := make([]networkEvent, 0)
	if n.service.config.Mode == "demo" {
		n.mu.Lock()
		defer n.mu.Unlock()
		for _, event := range n.events {
			if (source == "" || event.Source == source) && event.OccurredAt >= start && (end == 0 || event.OccurredAt < end) {
				payload, _ := json.Marshal(event)
				var clone networkEvent
				_ = json.Unmarshal(payload, &clone)
				items = append(items, clone)
			}
		}
	} else {
		where, args := " WHERE occurred_at >= ?", []any{start}
		if source != "" {
			where += " AND source=?"
			args = append(args, source)
		}
		if end != 0 {
			where += " AND occurred_at < ?"
			args = append(args, end)
		}
		rows, err := n.service.management.query(ctx, "SELECT payload FROM siteadmin_network_events"+where+" ORDER BY occurred_at,id", args...)
		if err != nil {
			return nil, fmt.Errorf("read network events: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var payload string
			var event networkEvent
			if err := rows.Scan(&payload); err != nil {
				return nil, err
			}
			if err := json.Unmarshal([]byte(payload), &event); err != nil {
				return nil, errors.New("stored network event is invalid")
			}
			items = append(items, event)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].OccurredAt == items[j].OccurredAt {
			return networkEventKey(items[i]) < networkEventKey(items[j])
		}
		return items[i].OccurredAt < items[j].OccurredAt
	})
	return items, nil
}

func (n *networkStore) Snapshot(ctx context.Context) ([]networkEvent, error) {
	return n.Events(ctx, "", 0, 0)
}

func (n *networkStore) getMeta(ctx context.Context, name string) (string, error) {
	if err := n.ensure(ctx); err != nil {
		return "", err
	}
	if n.service.config.Mode == "demo" {
		n.mu.Lock()
		defer n.mu.Unlock()
		return n.meta[name], nil
	}
	var value string
	err := n.service.management.queryRow(ctx, "SELECT payload FROM siteadmin_network_meta WHERE name=?", name).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

func (n *networkStore) setMeta(ctx context.Context, name, value string) error {
	if err := n.ensure(ctx); err != nil {
		return err
	}
	if n.service.config.Mode == "demo" {
		n.mu.Lock()
		n.meta[name] = value
		n.mu.Unlock()
		return nil
	}
	query := "INSERT INTO siteadmin_network_meta (name,payload) VALUES (?,?)"
	if n.service.management.driver == "postgres" {
		query += " ON CONFLICT (name) DO UPDATE SET payload=EXCLUDED.payload"
	} else {
		query += " ON DUPLICATE KEY UPDATE payload=VALUES(payload)"
	}
	_, err := n.service.management.db.ExecContext(ctx, bindQuery(n.service.management.driver, query), name, value)
	return err
}

func (n *networkStore) Metadata(ctx context.Context) (map[string]any, error) {
	cursor, err := n.getMeta(ctx, "call_cursor")
	if err != nil {
		return nil, err
	}
	lastSync, err := n.getMeta(ctx, "last_call_sync")
	if err != nil {
		return nil, err
	}
	firstImport, err := n.getMeta(ctx, "first_call_import")
	if err != nil {
		return nil, err
	}
	retentionCutoff, err := n.getMeta(ctx, "retention_cutoff")
	if err != nil {
		return nil, err
	}
	capabilities := map[string]bool{}
	if raw, err := n.getMeta(ctx, "call_source_capabilities"); err != nil {
		return nil, err
	} else if raw != "" {
		if json.Unmarshal([]byte(raw), &capabilities) != nil {
			return nil, errors.New("stored call capabilities are invalid")
		}
	}
	observed := map[string]int64{networkSourceCall: 0, networkSourceLogin: 0, networkSourceRegistration: 0}
	if n.service.config.Mode == "demo" {
		n.mu.Lock()
		for _, event := range n.events {
			observed[event.Source]++
		}
		n.mu.Unlock()
	} else {
		rows, err := n.service.management.query(ctx, "SELECT source,COUNT(*) FROM siteadmin_network_events GROUP BY source")
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var source string
			var count int64
			if err := rows.Scan(&source, &count); err != nil {
				_ = rows.Close()
				return nil, err
			}
			observed[source] = count
		}
		readErr, closeErr := rows.Err(), rows.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return nil, err
		}
	}
	sources := map[string]map[string]any{}
	for source, count := range observed {
		status := "not_observed"
		if count > 0 {
			status = "observed"
		}
		sources[source] = map[string]any{"status": status, "observed_events": count}
		if source != networkSourceCall {
			sources[source]["coverage"] = nil
			sources[source]["coverage_status"] = "unmeasured"
		}
	}
	return map[string]any{
		"call_cursor": cursor, "last_call_sync": lastSync, "first_call_import": firstImport,
		"history_complete": n.service.config.NetworkHistoryComplete && retentionCutoff == "",
		"retention_cutoff": retentionCutoff,
		"history_scope":    "api_call_only", "probe_history_complete": false, "event_sources": sources,
		"call_source_capabilities": capabilities,
		"sample_mode":              "none", "aggregation_version": 1,
		"history_note": "Completeness refers to retained/imported events; removed source logs cannot be reconstructed.",
	}, nil
}

func networkTaskSettlement(other string) bool {
	var values map[string]any
	if json.Unmarshal([]byte(other), &values) != nil {
		return false
	}
	switch value := values["is_task"].(type) {
	case bool:
		return value
	case float64:
		return value != 0
	case string:
		return value == "true" || value == "1"
	}
	return false
}

func (n *networkStore) saveCallBatch(ctx context.Context, events []networkEvent, cursor int64) error {
	db := n.service.management
	tx, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if len(events) > 0 {
		values := make([]string, 0, len(events))
		args := make([]any, 0, len(events)*8)
		expected := make(map[string]string, len(events))
		for _, event := range events {
			event, err := normalizeNetworkEvent(event)
			if err != nil {
				return err
			}
			payload, err := json.Marshal(event)
			if err != nil {
				return err
			}
			key := networkEventKey(event)
			expected[key] = string(payload)
			values = append(values, "(?,?,?,?,?,?,?,?)")
			args = append(args, key, event.Source, event.OccurredAt, event.UserID, event.TokenID, event.IP, event.Result, string(payload))
		}
		query := `INSERT INTO siteadmin_network_events
			(id,source,occurred_at,user_id,token_id,ip,result,payload) VALUES ` + strings.Join(values, ",")
		if db.driver == "postgres" {
			query += " ON CONFLICT (id) DO NOTHING"
		} else {
			query += " ON DUPLICATE KEY UPDATE id=id"
		}
		result, err := tx.ExecContext(ctx, bindQuery(db.driver, query), args...)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected < int64(len(events)) {
			marks, keys := make([]string, 0, len(events)), make([]any, 0, len(events))
			for key := range expected {
				marks, keys = append(marks, "?"), append(keys, key)
			}
			rows, err := tx.QueryContext(ctx, bindQuery(db.driver,
				"SELECT id,payload FROM siteadmin_network_events WHERE id IN ("+strings.Join(marks, ",")+")"), keys...)
			if err != nil {
				return err
			}
			seen := 0
			for rows.Next() {
				var key, payload string
				if err := rows.Scan(&key, &payload); err != nil {
					_ = rows.Close()
					return err
				}
				if expected[key] != payload {
					_ = rows.Close()
					return errors.New("usage log ID already has different content; inspect source log resets")
				}
				seen++
			}
			readErr, closeErr := rows.Err(), rows.Close()
			if err := errors.Join(readErr, closeErr); err != nil {
				return err
			}
			if seen != len(events) {
				return errors.New("network import batch verification failed")
			}
		}
	}
	query := "INSERT INTO siteadmin_network_meta (name,payload) VALUES (?,?)"
	if db.driver == "postgres" {
		query += " ON CONFLICT (name) DO UPDATE SET payload=GREATEST(siteadmin_network_meta.payload::bigint,EXCLUDED.payload::bigint)::text"
	} else {
		query += " ON DUPLICATE KEY UPDATE payload=CAST(GREATEST(CAST(payload AS UNSIGNED),CAST(VALUES(payload) AS UNSIGNED)) AS CHAR)"
	}
	if _, err := tx.ExecContext(ctx, bindQuery(db.driver, query), "call_cursor", strconv.FormatInt(cursor, 10)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	n.revision.Add(1)
	return nil
}

func (n *networkStore) SyncCalls(ctx context.Context) (map[string]any, error) {
	return n.syncCallsProgress(ctx, nil)
}

func (n *networkStore) syncCallsProgress(ctx context.Context, progress func(map[string]any)) (map[string]any, error) {
	for !n.syncMu.TryLock() {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	defer n.syncMu.Unlock()
	if err := n.ensure(ctx); err != nil {
		return nil, err
	}
	result := map[string]any{"imported": 0, "skipped_settlements": 0, "complete": false, "sample_mode": "none"}
	if n.service.config.Mode == "demo" {
		result["complete"] = true
		result["mode"] = "demo"
		return result, nil
	}
	if n.service.logs.db == nil {
		return result, errors.New("usage log database is not configured")
	}
	schema, err := n.service.logs.query(ctx, "SELECT * FROM logs WHERE 1=0")
	if err != nil {
		return result, fmt.Errorf("read usage log capabilities: %w", err)
	}
	columns, columnErr := schema.Columns()
	closeErr := schema.Close()
	if err := errors.Join(columnErr, closeErr); err != nil {
		return result, err
	}
	available := map[string]bool{}
	for _, column := range columns {
		available[strings.ToLower(column)] = true
	}
	for _, required := range []string{"id", "created_at", "user_id", "type"} {
		if !available[required] {
			return result, errors.New("usage log schema lacks required event columns")
		}
	}
	column := func(name, fallback string) string {
		if !available[name] {
			return fallback
		}
		return "COALESCE(" + name + "," + fallback + ")"
	}
	callSelect := "SELECT id,created_at,user_id," + column("token_id", "0") + "," + column("ip", "''") +
		",type," + column("other", "''") + "," + column("quota", "0") + "," +
		column("model_name", "''") + "," + column("request_id", "''") +
		" FROM logs WHERE id>? AND id<=? AND type IN (2,5) ORDER BY id LIMIT 1000"
	capabilities, _ := json.Marshal(map[string]bool{
		"request_id": available["request_id"], "ip": available["ip"], "task_settlement_filter": available["other"],
	})
	if err := n.setMeta(ctx, "call_source_capabilities", string(capabilities)); err != nil {
		return result, err
	}
	if !available["other"] {
		result["warnings"] = []string{"Usage logs have no other column; task-settlement exclusion is unavailable."}
	}
	rawCursor, err := n.getMeta(ctx, "call_cursor")
	if err != nil {
		return result, err
	}
	cursor := int64(0)
	if rawCursor != "" {
		cursor, err = strconv.ParseInt(rawCursor, 10, 64)
		if err != nil || cursor < 0 {
			return result, errors.New("stored call cursor is invalid")
		}
	}
	rawRetention, err := n.getMeta(ctx, "retention_cutoff")
	if err != nil {
		return result, err
	}
	retentionCutoff := int64(0)
	if rawRetention != "" {
		retentionCutoff, err = strconv.ParseInt(rawRetention, 10, 64)
		if err != nil || retentionCutoff < 0 {
			return result, errors.New("stored retention cutoff is invalid")
		}
	}
	var watermark int64
	if err := n.service.logs.queryRow(ctx, "SELECT COALESCE(MAX(id),0) FROM logs").Scan(&watermark); err != nil {
		return result, fmt.Errorf("read usage log watermark: %w", err)
	}
	if watermark > 0 && watermark < cursor {
		return result, errors.New("usage log sequence decreased; inspect source restoration before continuing")
	}
	result["start_cursor"], result["cursor"], result["watermark"] = cursor, cursor, watermark
	if progress != nil {
		progress(result)
	}
	if first, err := n.getMeta(ctx, "first_call_import"); err != nil {
		return result, err
	} else if first == "" {
		if err := n.setMeta(ctx, "first_call_import", strconv.FormatInt(n.clock().Unix(), 10)); err != nil {
			return result, err
		}
	}
	imported, skipped, skippedRetention := 0, 0, 0
	for {
		rows, err := n.service.logs.query(ctx, callSelect, cursor, watermark)
		if err != nil {
			return result, fmt.Errorf("read usage logs for network import: %w", err)
		}
		type importedRow struct {
			id, at, user, token, kind, quota int64
			ip, other, model, request        string
		}
		batch := make([]importedRow, 0, 1000)
		for rows.Next() {
			var row importedRow
			if err := rows.Scan(&row.id, &row.at, &row.user, &row.token, &row.ip, &row.kind, &row.other, &row.quota, &row.model, &row.request); err != nil {
				_ = rows.Close()
				return result, err
			}
			batch = append(batch, row)
		}
		readErr := rows.Err()
		closeErr := rows.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return result, err
		}
		events := make([]networkEvent, 0, len(batch))
		for _, row := range batch {
			if retentionCutoff > 0 && row.at < retentionCutoff {
				skippedRetention++
			} else if networkTaskSettlement(row.other) {
				skipped++
			} else {
				outcome := "success"
				if row.kind == 5 {
					outcome = "failure"
				}
				event := networkEvent{
					ID: "log:" + strconv.FormatInt(row.id, 10), Source: networkSourceCall,
					OccurredAt: row.at, UserID: row.user, TokenID: row.token, IP: row.ip, Result: outcome,
					Metadata: map[string]string{
						"quota": strconv.FormatInt(row.quota, 10), "model": row.model,
						"request_id": row.request, "ip_source": "newapi_usage_log",
					},
				}
				events = append(events, event)
				imported++
			}
			cursor = row.id
		}
		if len(batch) > 0 {
			if err := n.saveCallBatch(ctx, events, cursor); err != nil {
				return result, err
			}
		}
		result["imported"], result["skipped_settlements"], result["cursor"] = imported, skipped, cursor
		result["skipped_retention"] = skippedRetention
		if progress != nil {
			progress(result)
		}
		if len(batch) < 1000 {
			if watermark > cursor {
				if err := n.saveCallBatch(ctx, nil, watermark); err != nil {
					return result, err
				}
				cursor = watermark
				result["cursor"] = cursor
			}
			if err := n.setMeta(ctx, "last_call_sync", strconv.FormatInt(n.clock().Unix(), 10)); err != nil {
				return result, err
			}
			result["complete"] = true
			if progress != nil {
				progress(result)
			}
			return result, nil
		}
	}
}

func (n *networkStore) openGeo() {
	n.geoOnce.Do(func() {
		n.geoMu.Lock()
		defer n.geoMu.Unlock()
		for _, database := range []struct {
			path   string
			target **maxminddb.Reader
			label  string
		}{
			{n.service.config.GeoCityPath, &n.city, "city"},
			{n.service.config.GeoASNPath, &n.asn, "ASN"},
			{n.service.config.GeoISPPath, &n.isp, "ISP"},
			{n.service.config.GeoAnonymousPath, &n.anonymous, "anonymous-IP"},
		} {
			if database.path == "" {
				continue
			}
			reader, err := maxminddb.Open(database.path)
			if err != nil {
				n.geoError = append(n.geoError, database.label+" database could not be opened")
			} else {
				*database.target = reader
			}
		}
	})
}

func (n *networkStore) Close() error {
	n.geoMu.Lock()
	defer n.geoMu.Unlock()
	var closeErr error
	for _, reader := range []*maxminddb.Reader{n.city, n.asn, n.isp, n.anonymous} {
		if reader != nil {
			closeErr = errors.Join(closeErr, reader.Close())
		}
	}
	n.city, n.asn, n.isp, n.anonymous = nil, nil, nil, nil
	return closeErr
}

var networkReservedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"),
}

func classifyNetworkIP(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "missing"
	}
	ip, err := netip.ParseAddr(raw)
	if err != nil || ip.Zone() != "" {
		return raw, "invalid"
	}
	ip = ip.Unmap()
	if ip.IsPrivate() {
		return ip.String(), "private"
	}
	if ip.IsLoopback() {
		return ip.String(), "loopback"
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return ip.String(), "link_local"
	}
	if !ip.IsGlobalUnicast() {
		return ip.String(), "reserved"
	}
	for _, prefix := range networkReservedPrefixes {
		if prefix.Contains(ip) {
			return ip.String(), "reserved"
		}
	}
	return ip.String(), "public"
}

func defaultProxyAttributes() map[string]string {
	return map[string]string{"vpn": "unknown", "public_proxy": "unknown", "residential_proxy": "unknown", "tor": "unknown"}
}

func (n *networkStore) loadAttributes(ctx context.Context) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.service.config.Mode == "demo" || !n.attrsAt.IsZero() && n.clock().Sub(n.attrsAt) < time.Minute {
		return nil
	}
	rows, err := n.service.management.query(ctx, "SELECT payload FROM siteadmin_ip_enrichment")
	if err != nil {
		return err
	}
	defer rows.Close()
	next := map[string]networkIPInfo{}
	for rows.Next() {
		var payload string
		var info networkIPInfo
		if err := rows.Scan(&payload); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(payload), &info); err != nil {
			return errors.New("stored IP enrichment is invalid")
		}
		next[info.IP] = info
	}
	if err := rows.Err(); err != nil {
		return err
	}
	n.attrs, n.attrsAt = next, n.clock()
	return nil
}

func (n *networkStore) Lookup(ctx context.Context, raw string) (networkIPInfo, error) {
	canonical, kind := classifyNetworkIP(raw)
	info := networkIPInfo{
		IP: canonical, AddressClass: kind, NetworkType: "unknown",
		Proxy: defaultProxyAttributes(), DataSource: "unavailable", UpdatedAt: n.clock().Unix(),
		FieldSources: map[string]string{},
	}
	if err := ctx.Err(); err != nil {
		return info, err
	}
	if kind != "public" {
		return info, nil
	}
	if err := n.ensure(ctx); err != nil {
		return info, err
	}
	if err := n.loadAttributes(ctx); err != nil {
		return info, err
	}
	n.mu.Lock()
	attributes := n.attrs[canonical]
	n.mu.Unlock()
	return n.enrichNetworkIP(ctx, info, attributes)
}

// No database reads: callers that stream events must load their attribute
// snapshot before opening a database cursor.
func (n *networkStore) enrichNetworkIP(ctx context.Context, info networkIPInfo, attributes networkIPInfo) (networkIPInfo, error) {
	if err := ctx.Err(); err != nil || info.AddressClass != "public" {
		return info, err
	}
	n.openGeo()
	n.geoMu.RLock()
	defer n.geoMu.RUnlock()
	address := net.ParseIP(info.IP)
	if n.city != nil {
		var city struct {
			Country struct {
				ISOCode string            `maxminddb:"iso_code"`
				Names   map[string]string `maxminddb:"names"`
			} `maxminddb:"country"`
			City struct {
				Names map[string]string `maxminddb:"names"`
			} `maxminddb:"city"`
			Subdivisions []struct {
				Names map[string]string `maxminddb:"names"`
			} `maxminddb:"subdivisions"`
		}
		if err := n.city.Lookup(address, &city); err != nil {
			return info, fmt.Errorf("city lookup failed: %w", err)
		}
		info.CountryCode = city.Country.ISOCode
		info.Country, info.City = networkLocalizedName(city.Country.Names), networkLocalizedName(city.City.Names)
		if len(city.Subdivisions) > 0 {
			info.Region = networkLocalizedName(city.Subdivisions[0].Names)
		}
		info.GeoVersion = fmt.Sprintf("%s:%d", n.city.Metadata.DatabaseType, n.city.Metadata.BuildEpoch)
		if info.CountryCode != "" {
			info.DataSource, info.FieldSources["geography"] = "offline_maxmind", "offline_maxmind_city"
		}
	}
	if n.asn != nil {
		var asn struct {
			Number uint   `maxminddb:"autonomous_system_number"`
			Org    string `maxminddb:"autonomous_system_organization"`
			ISP    string `maxminddb:"isp"`
		}
		if err := n.asn.Lookup(address, &asn); err != nil {
			return info, fmt.Errorf("ASN lookup failed: %w", err)
		}
		info.ASN, info.ASOrg, info.ISP = asn.Number, asn.Org, asn.ISP
		info.ASNVersion = fmt.Sprintf("%s:%d", n.asn.Metadata.DatabaseType, n.asn.Metadata.BuildEpoch)
		if info.ASN > 0 {
			info.DataSource, info.FieldSources["asn"] = "offline_maxmind", "offline_maxmind_asn"
		}
	}
	if n.isp != nil {
		var record struct {
			ISP          string `maxminddb:"isp"`
			Organization string `maxminddb:"organization"`
			ASN          uint   `maxminddb:"autonomous_system_number"`
			ASOrg        string `maxminddb:"autonomous_system_organization"`
		}
		_, found, err := n.isp.LookupNetwork(address, &record)
		if err != nil {
			return info, fmt.Errorf("ISP lookup failed: %w", err)
		}
		info.ISPVersion = fmt.Sprintf("%s:%d", n.isp.Metadata.DatabaseType, n.isp.Metadata.BuildEpoch)
		if found {
			if record.ISP != "" {
				info.ISP, info.FieldSources["isp"] = record.ISP, "offline_maxmind_isp"
			}
			if info.ASN == 0 && record.ASN > 0 {
				info.ASN, info.ASOrg = record.ASN, record.ASOrg
				info.FieldSources["asn"] = "offline_maxmind_isp"
			}
			if info.ASOrg == "" {
				info.ASOrg = record.Organization
			}
			info.DataSource = "offline_maxmind"
		}
	}
	if n.anonymous != nil {
		var record struct {
			Anonymous        *bool `maxminddb:"is_anonymous"`
			VPN              *bool `maxminddb:"is_anonymous_vpn"`
			PublicProxy      *bool `maxminddb:"is_public_proxy"`
			ResidentialProxy *bool `maxminddb:"is_residential_proxy"`
			Tor              *bool `maxminddb:"is_tor_exit_node"`
			Hosting          *bool `maxminddb:"is_hosting_provider"`
		}
		_, found, err := n.anonymous.LookupNetwork(address, &record)
		if err != nil {
			return info, fmt.Errorf("anonymous-IP lookup failed: %w", err)
		}
		info.ProxyVersion = fmt.Sprintf("%s:%d", n.anonymous.Metadata.DatabaseType, n.anonymous.Metadata.BuildEpoch)
		classified := record.Anonymous != nil || record.VPN != nil || record.PublicProxy != nil ||
			record.ResidentialProxy != nil || record.Tor != nil || record.Hosting != nil
		// MMDB may return a matching network with an empty data record.
		// Only an actual provider classification can turn unknown into no.
		if found && classified {
			for key, detected := range map[string]*bool{
				"vpn": record.VPN, "public_proxy": record.PublicProxy,
				"residential_proxy": record.ResidentialProxy, "tor": record.Tor,
			} {
				value := "no"
				if detected != nil && *detected {
					value = "yes"
				}
				info.Proxy[key], info.FieldSources["proxy_"+key] = value, "offline_maxmind_anonymous"
			}
			if record.Hosting != nil && *record.Hosting {
				info.NetworkType, info.FieldSources["network_type"] = "hosting", "offline_maxmind_anonymous"
			}
			info.DataSource = "offline_maxmind"
		}
	}
	n.applyIPIntelligence(&info)
	if attributes.IP != "" {
		if attributes.CountryCode != "" {
			info.CountryCode, info.Country = attributes.CountryCode, attributes.Country
			info.Region, info.City = attributes.Region, attributes.City
			info.FieldSources["geography"] = attributes.DataSource
		}
		if attributes.ASN > 0 {
			info.ASN, info.ASOrg = attributes.ASN, attributes.ASOrg
			info.FieldSources["asn"] = attributes.DataSource
		}
		if attributes.ISP != "" {
			info.ISP = attributes.ISP
			info.FieldSources["isp"] = attributes.DataSource
		}
		if attributes.NetworkType != "" {
			info.NetworkType = attributes.NetworkType
			info.FieldSources["network_type"] = attributes.DataSource
		}
		for key, value := range attributes.Proxy {
			info.Proxy[key] = value
			info.FieldSources["proxy_"+key] = attributes.DataSource
		}
		info.DataSource, info.UpdatedAt = attributes.DataSource, attributes.UpdatedAt
	}
	return info, nil
}

func networkLocalizedName(names map[string]string) string {
	if names["zh-CN"] != "" {
		return names["zh-CN"]
	}
	return names["en"]
}

func (n *networkStore) SaveAttributes(ctx context.Context, info networkIPInfo) error {
	info, err := normalizedNetworkAttributes(info, n.clock().Unix())
	if err != nil {
		return err
	}
	ip := info.IP
	if err := n.ensure(ctx); err != nil {
		return err
	}
	if n.service.config.Mode == "demo" {
		n.mu.Lock()
		n.attrs[ip] = info
		n.mu.Unlock()
		n.revision.Add(1)
		return nil
	}
	payload, _ := json.Marshal(info)
	query := "INSERT INTO siteadmin_ip_enrichment (ip,payload) VALUES (?,?)"
	if n.service.management.driver == "postgres" {
		query += " ON CONFLICT (ip) DO UPDATE SET payload=EXCLUDED.payload"
	} else {
		query += " ON DUPLICATE KEY UPDATE payload=VALUES(payload)"
	}
	_, err = n.service.management.db.ExecContext(ctx, bindQuery(n.service.management.driver, query), ip, string(payload))
	if err == nil {
		n.mu.Lock()
		n.attrs[ip] = info
		n.mu.Unlock()
		n.revision.Add(1)
	}
	return err
}

func normalizedNetworkAttributes(info networkIPInfo, at int64) (networkIPInfo, error) {
	ip, kind := classifyNetworkIP(info.IP)
	if kind != "public" {
		return info, errors.New("IP attributes require a public address")
	}
	if !validLogFilter(info.DataSource, 128) || info.DataSource == "" ||
		!validLogFilter(info.ASOrg, 256) || !validLogFilter(info.ISP, 256) ||
		!validLogFilter(info.Country, 128) || !validLogFilter(info.Region, 128) || !validLogFilter(info.City, 128) {
		return info, errors.New("invalid IP attributes or missing data source")
	}
	info.CountryCode = strings.ToUpper(info.CountryCode)
	if info.CountryCode != "" && (len(info.CountryCode) != 2 ||
		info.CountryCode[0] < 'A' || info.CountryCode[0] > 'Z' ||
		info.CountryCode[1] < 'A' || info.CountryCode[1] > 'Z') {
		return info, errors.New("country code must contain two ISO letters")
	}
	switch info.NetworkType {
	case "", "unknown", "residential", "mobile", "hosting", "corporate", "non_residential":
	default:
		return info, errors.New("invalid network type")
	}
	allowedProxy := defaultProxyAttributes()
	proxy := map[string]string{}
	for key, value := range info.Proxy {
		if _, ok := allowedProxy[key]; !ok {
			return info, errors.New("invalid proxy attribute")
		}
		if value != "yes" && value != "no" && value != "unknown" {
			return info, errors.New("proxy attributes must be yes, no or unknown")
		}
		proxy[key] = value
	}
	info.IP, info.AddressClass, info.Proxy, info.UpdatedAt = ip, kind, proxy, at
	info.FieldSources, info.Intelligence = nil, nil
	info.GeoVersion, info.ASNVersion, info.ISPVersion, info.ProxyVersion = "", "", "", ""
	return info, nil
}

type networkCountBucket struct {
	Key                 string  `json:"key"`
	Label               string  `json:"label"`
	Events              int64   `json:"events"`
	Success             int64   `json:"success"`
	Failure             int64   `json:"failure"`
	Users               int     `json:"users"`
	Tokens              int     `json:"tokens"`
	IPs                 int     `json:"ips"`
	UserIDs             []int64 `json:"user_ids,omitempty"`
	TokenIDs            []int64 `json:"token_ids,omitempty"`
	FirstSeenAt         int64   `json:"first_seen_at"`
	LastSeenAt          int64   `json:"last_seen_at"`
	RelatedIDsTruncated bool    `json:"related_ids_truncated,omitempty"`
}

type networkAggregateBucket struct {
	networkCountBucket
	users, tokens map[int64]struct{}
	ips           map[string]struct{}
}

type networkAnalysis struct {
	Source         string                   `json:"event_type"`
	Start          int64                    `json:"window_start"`
	End            int64                    `json:"window_end"`
	AsOf           int64                    `json:"as_of"`
	Timezone       string                   `json:"timezone"`
	SampleMode     string                   `json:"sample_mode"`
	Completeness   string                   `json:"completeness"`
	Summary        networkCountBucket       `json:"summary"`
	Coverage       map[string]any           `json:"coverage"`
	Metadata       map[string]any           `json:"metadata"`
	AddressClasses []networkCountBucket     `json:"address_classes"`
	Geography      []networkCountBucket     `json:"geography"`
	Regions        []networkCountBucket     `json:"regions"`
	Cities         []networkCountBucket     `json:"cities"`
	ASN            []networkCountBucket     `json:"asn"`
	Networks       []networkCountBucket     `json:"network_types"`
	Proxies        []networkCountBucket     `json:"proxy_attributes"`
	IPs            []networkCountBucket     `json:"ip_distribution"`
	MultiIPUsers   []networkCountBucket     `json:"multi_ip_users"`
	MultiIPTokens  []networkCountBucket     `json:"multi_ip_tokens"`
	Trend          []networkCountBucket     `json:"trend"`
	Warnings       []string                 `json:"warnings"`
	Enrichment     map[string]networkIPInfo `json:"-"`
}

func newNetworkBucket(key, label string) *networkAggregateBucket {
	return &networkAggregateBucket{
		networkCountBucket: networkCountBucket{Key: key, Label: label},
		users:              map[int64]struct{}{}, tokens: map[int64]struct{}{}, ips: map[string]struct{}{},
	}
}

func (b *networkAggregateBucket) add(event networkEvent, validIP bool) {
	b.Events++
	if b.FirstSeenAt == 0 || event.OccurredAt < b.FirstSeenAt {
		b.FirstSeenAt = event.OccurredAt
	}
	if event.OccurredAt > b.LastSeenAt {
		b.LastSeenAt = event.OccurredAt
	}
	if event.Result == "success" {
		b.Success++
	} else if event.Result == "failure" {
		b.Failure++
	}
	if event.UserID > 0 {
		b.users[event.UserID] = struct{}{}
	}
	if event.Source == networkSourceCall && event.TokenID > 0 {
		b.tokens[event.TokenID] = struct{}{}
	}
	if validIP {
		b.ips[event.IP] = struct{}{}
	}
}

func networkFinalBucket(bucket *networkAggregateBucket, includeIDs bool) networkCountBucket {
	result := bucket.networkCountBucket
	result.Users, result.Tokens, result.IPs = len(bucket.users), len(bucket.tokens), len(bucket.ips)
	if includeIDs {
		result.UserIDs, result.TokenIDs = []int64{}, []int64{}
		for id := range bucket.users {
			result.UserIDs = append(result.UserIDs, id)
		}
		for id := range bucket.tokens {
			result.TokenIDs = append(result.TokenIDs, id)
		}
		sort.Slice(result.UserIDs, func(i, j int) bool { return result.UserIDs[i] < result.UserIDs[j] })
		sort.Slice(result.TokenIDs, func(i, j int) bool { return result.TokenIDs[i] < result.TokenIDs[j] })
	}
	return result
}

func networkBucketsFinal(buckets map[string]*networkAggregateBucket, includeIDs bool) []networkCountBucket {
	items := make([]networkCountBucket, 0, len(buckets))
	for _, bucket := range buckets {
		items = append(items, networkFinalBucket(bucket, includeIDs))
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Events == items[j].Events {
			return items[i].Key < items[j].Key
		}
		return items[i].Events > items[j].Events
	})
	return items
}

func addNetworkBucket(buckets map[string]*networkAggregateBucket, key, label string, event networkEvent, validIP bool) {
	if buckets[key] == nil {
		buckets[key] = newNetworkBucket(key, label)
	}
	buckets[key].add(event, validIP)
}

func (n *networkStore) Analyze(ctx context.Context, source string, start, end int64, userID, tokenID int64, filterIP string) (networkAnalysis, error) {
	result := networkAnalysis{
		Source: source, Start: start, End: end, AsOf: n.clock().Unix(),
		Timezone: n.service.config.Timezone, SampleMode: "none", Completeness: "retained_events",
		Warnings: []string{}, Enrichment: map[string]networkIPInfo{},
	}
	if !validNetworkSource(source) || start < 0 || end <= start {
		return result, errors.New("a single valid source and time window are required")
	}
	var err error
	result.Metadata, err = n.Metadata(ctx)
	if err != nil {
		return result, err
	}
	if err := n.loadAttributes(ctx); err != nil {
		return result, err
	}
	n.mu.Lock()
	attributes := maps.Clone(n.attrs)
	n.mu.Unlock()
	windowComplete, err := n.historyCompleteFor(ctx, start)
	if err != nil {
		return result, err
	}
	result.Metadata["window_history_complete"] = windowComplete
	result.Metadata["aggregation_mode"] = "streamed_retained_events"
	if !windowComplete {
		result.Warnings = append(result.Warnings, "Source history before the imported/retained records is not proven complete.")
	}
	classes, countries, asns, networks, proxies := map[string]*networkAggregateBucket{}, map[string]*networkAggregateBucket{},
		map[string]*networkAggregateBucket{}, map[string]*networkAggregateBucket{}, map[string]*networkAggregateBucket{}
	regions, cities := map[string]*networkAggregateBucket{}, map[string]*networkAggregateBucket{}
	ips, users, tokens, trend := map[string]*networkAggregateBucket{}, map[string]*networkAggregateBucket{},
		map[string]*networkAggregateBucket{}, map[string]*networkAggregateBucket{}
	summary := newNetworkBucket("total", "total")
	validEvents, publicEvents, locatedEvents, asnEvents, proxyEvents, attributedEvents := int64(0), int64(0), int64(0), int64(0), int64(0), int64(0)
	domesticEvents, foreignEvents := int64(0), int64(0)
	filterIP, _ = classifyNetworkIP(filterIP)
	err = n.visitFilteredNetworkEventsE(ctx, networkEventFilter{
		source: source, start: start, end: end, userID: userID, tokenID: tokenID, ip: filterIP,
	}, func(event networkEvent) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if userID > 0 && event.UserID != userID || tokenID > 0 && event.TokenID != tokenID {
			return nil
		}
		event.IP, _ = classifyNetworkIP(event.IP)
		if filterIP != "" && event.IP != filterIP {
			return nil
		}
		info, exists := result.Enrichment[event.IP]
		if !exists {
			canonical, kind := classifyNetworkIP(event.IP)
			info = networkIPInfo{
				IP: canonical, AddressClass: kind, NetworkType: "unknown",
				Proxy: defaultProxyAttributes(), DataSource: "unavailable", UpdatedAt: n.clock().Unix(),
				FieldSources: map[string]string{},
			}
			info, err = n.enrichNetworkIP(ctx, info, attributes[canonical])
			if err != nil {
				return err
			}
			result.Enrichment[event.IP] = info
		}
		valid := info.AddressClass != "missing" && info.AddressClass != "invalid"
		summary.add(event, valid)
		if event.UserID > 0 {
			attributedEvents++
		}
		addNetworkBucket(classes, info.AddressClass, info.AddressClass, event, valid)
		geography := info.CountryCode
		if geography == "" {
			geography = info.AddressClass
			if geography == "public" {
				geography = "unknown"
			}
		}
		label := info.Country
		if label == "" {
			label = geography
		}
		addNetworkBucket(countries, geography, label, event, valid)
		region, city := info.Region, info.City
		if region == "" {
			region = "unknown"
		}
		if city == "" {
			city = "unknown"
		}
		regionKey, _ := json.Marshal([]string{geography, region})
		cityKey, _ := json.Marshal([]string{geography, region, city})
		addNetworkBucket(regions, string(regionKey), geography+" / "+region, event, valid)
		addNetworkBucket(cities, string(cityKey), geography+" / "+region+" / "+city, event, valid)
		asnKey := "unknown"
		if info.ASN > 0 {
			asnKey = strconv.FormatUint(uint64(info.ASN), 10)
		}
		addNetworkBucket(asns, asnKey, info.ASOrg, event, valid)
		addNetworkBucket(networks, info.NetworkType, info.NetworkType, event, valid)
		detected := false
		for key, value := range info.Proxy {
			addNetworkBucket(proxies, key+":"+value, key, event, valid)
			detected = detected || value != "unknown"
		}
		if valid {
			validEvents++
			addNetworkBucket(ips, event.IP, event.IP, event, true)
			if event.UserID > 0 {
				key := strconv.FormatInt(event.UserID, 10)
				addNetworkBucket(users, key, key, event, true)
			}
			if source == networkSourceCall && event.TokenID > 0 {
				key := strconv.FormatInt(event.TokenID, 10)
				addNetworkBucket(tokens, key, key, event, true)
			}
		}
		if info.AddressClass == "public" {
			publicEvents++
			if info.CountryCode != "" {
				locatedEvents++
				if info.CountryCode == "CN" {
					domesticEvents++
				} else {
					foreignEvents++
				}
			}
			if info.ASN > 0 {
				asnEvents++
			}
			if detected {
				proxyEvents++
			}
		}
		bucketAt := event.OccurredAt / 3600 * 3600
		if end-start > 7*86400 {
			location := n.service.config.Location
			if location == nil {
				location = time.UTC
			}
			local := time.Unix(event.OccurredAt, 0).In(location)
			bucketAt = time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location).Unix()
		}
		key := strconv.FormatInt(bucketAt, 10)
		addNetworkBucket(trend, key, key, event, valid)
		return nil
	})
	if err != nil {
		return result, err
	}
	result.Summary = networkFinalBucket(summary, false)
	ratio := func(numerator, denominator int64) any {
		if denominator == 0 {
			return nil
		}
		return float64(numerator) / float64(denominator)
	}
	result.Coverage = map[string]any{
		"ip_capture_rate":          ratio(validEvents, summary.Events),
		"account_attribution_rate": ratio(attributedEvents, summary.Events),
		"geo_resolution_rate":      ratio(locatedEvents, publicEvents),
		"asn_resolution_rate":      ratio(asnEvents, publicEvents),
		"proxy_detection_rate":     ratio(proxyEvents, publicEvents),
		"valid_ip_events":          validEvents, "public_ip_events": publicEvents, "located_events": locatedEvents,
		"domestic_country_code": "CN", "domestic_events": domesticEvents, "foreign_events": foreignEvents,
		"denominator_note": "Capture uses all events; geolocation/ASN/proxy coverage use public-IP events. Proxy labels can overlap.",
	}
	result.AddressClasses, result.Geography, result.ASN = networkBucketsFinal(classes, false), networkBucketsFinal(countries, false), networkBucketsFinal(asns, false)
	result.Regions, result.Cities = networkBucketsFinal(regions, false), networkBucketsFinal(cities, false)
	result.Networks, result.Proxies, result.IPs = networkBucketsFinal(networks, false), networkBucketsFinal(proxies, false), networkBucketsFinal(ips, true)
	result.MultiIPUsers, result.MultiIPTokens = []networkCountBucket{}, []networkCountBucket{}
	for _, bucket := range networkBucketsFinal(users, false) {
		if bucket.IPs > 1 {
			result.MultiIPUsers = append(result.MultiIPUsers, bucket)
		}
	}
	for _, bucket := range networkBucketsFinal(tokens, false) {
		if bucket.IPs > 1 {
			result.MultiIPTokens = append(result.MultiIPTokens, bucket)
		}
	}
	result.Trend = networkBucketsFinal(trend, false)
	sort.Slice(result.Trend, func(i, j int) bool { return result.Trend[i].Key < result.Trend[j].Key })
	n.openGeo()
	n.geoMu.RLock()
	result.Warnings = append(result.Warnings, n.geoError...)
	result.Metadata["geo_available"], result.Metadata["asn_available"] = n.city != nil, n.asn != nil
	result.Metadata["isp_available"], result.Metadata["proxy_database_available"] = n.isp != nil, n.anonymous != nil
	versions := map[string]string{}
	for name, reader := range map[string]*maxminddb.Reader{"city": n.city, "asn": n.asn, "isp": n.isp, "anonymous": n.anonymous} {
		if reader != nil {
			versions[name] = fmt.Sprintf("%s:%d", reader.Metadata.DatabaseType, reader.Metadata.BuildEpoch)
		}
	}
	result.Metadata["ip_database_versions"] = versions
	n.geoMu.RUnlock()
	result.Metadata["intelligence"] = n.ipIntelligenceStatus()
	if source == networkSourceCall {
		if capabilities, ok := result.Metadata["call_source_capabilities"].(map[string]bool); ok {
			if available, known := capabilities["task_settlement_filter"]; known && !available {
				result.Warnings = append(result.Warnings, "Task settlements cannot be distinguished because the source has no other column.")
			}
			if available, known := capabilities["ip"]; known && !available {
				result.Warnings = append(result.Warnings, "The usage-log source does not expose an IP column.")
			}
		}
	}
	if source != networkSourceCall && result.Summary.Events == 0 {
		result.Completeness = "no_observed_events"
		result.Warnings = append(result.Warnings, "No probe events were observed in this window; this does not prove there were no authentication events.")
	}
	return result, nil
}

func registerIPAnalysisRoutes(router *gin.Engine, service *dashboardService, adminToken string) {
	if service.network == nil {
		service.network = newNetworkStore(service)
	}
	group := router.Group("/api/ip", requireAdminToken(adminToken))
	group.GET("/stats", service.ipAnalysisHTTP("stats"))
	group.GET("/distribution", service.ipAnalysisHTTP("distribution"))
	group.GET("/shared", service.ipAnalysisHTTP("shared"))
	group.GET("/addresses", service.ipAnalysisHTTP("addresses"))
	group.GET("/multi-ip-users", service.ipAnalysisHTTP("users"))
	group.GET("/multi-ip-tokens", service.ipAnalysisHTTP("tokens"))
	group.GET("/users/:id/ips", service.ipAnalysisHTTP("user_ips"))
	group.GET("/lookup/:ip", service.ipLookupHTTP)
	group.GET("/details/:ip", service.ipDetailsHTTP)
	group.GET("/intelligence/status", service.ipIntelligenceStatusHTTP)
	group.POST("/sync", service.ipSyncHTTP)
	group.GET("/sync/status", service.ipSyncStatusHTTP)
	group.POST("/sync/cancel", service.ipSyncCancelHTTP)
	group.POST("/attributes", service.ipAttributesHTTP)
	registerIPMaintenanceRoutes(group, service)
}

func networkHTTPWindow(c *gin.Context, now time.Time) (string, int64, int64, error) {
	source := c.DefaultQuery("source", networkSourceCall)
	if !validNetworkSource(source) {
		return "", 0, 0, errors.New("source must be api_call, login or registration")
	}
	durations := map[string]int64{"1h": 3600, "3h": 10800, "6h": 21600, "12h": 43200, "24h": 86400, "3d": 259200, "7d": 604800, "30d": 2592000, "90d": 7776000}
	duration, exists := durations[c.DefaultQuery("period", "7d")]
	if !exists {
		return "", 0, 0, errors.New("invalid network period")
	}
	end := now.Unix() + 1
	start := end - duration
	if rawStart, rawEnd := c.Query("start"), c.Query("end"); rawStart != "" || rawEnd != "" {
		var err error
		start, err = strconv.ParseInt(rawStart, 10, 64)
		if err != nil {
			return "", 0, 0, errors.New("start and end must both be Unix seconds")
		}
		end, err = strconv.ParseInt(rawEnd, 10, 64)
		if err != nil || start < 0 || end <= start || end > now.Unix()+1 || end-start > 366*86400 {
			return "", 0, 0, errors.New("invalid network window")
		}
	}
	return source, start, end, nil
}

func networkHTTPID(raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("ID must be a positive integer")
	}
	return id, nil
}

func (s *dashboardService) ipAnalysisHTTP(view string) gin.HandlerFunc {
	return func(c *gin.Context) {
		source, start, end, err := networkHTTPWindow(c, s.network.clock())
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		userID, err := networkHTTPID(c.Query("user_id"))
		if err == nil && view == "user_ips" {
			userID, err = networkHTTPID(c.Param("id"))
		}
		tokenID, tokenErr := networkHTTPID(c.Query("token_id"))
		ip, ipErr := networkFilterAddress(c.Query("ip"), false)
		if err != nil || tokenErr != nil || tokenID > 0 && source != networkSourceCall || ipErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user/token filter"})
			return
		}
		page, pageErr := positiveLogInt(c.Query("page"), 1, 1000000)
		pageSize, sizeErr := positiveLogInt(c.Query("page_size"), 100, 500)
		syncOption := c.DefaultQuery("sync", "true")
		if pageErr != nil || sizeErr != nil || syncOption != "true" && syncOption != "false" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid pagination"})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
		defer cancel()
		if source == networkSourceCall && syncOption != "false" {
			s.network.queueCallSync("page")
		}
		data, err := s.network.cachedAnalysis(ctx, source, start, end, userID, tokenID, ip)
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "network analysis is unavailable"})
			return
		}
		if view == "stats" || view == "distribution" {
			// Detailed IP and relationship lists have paginated routes; metrics still use all events.
			data.IPs, data.MultiIPUsers, data.MultiIPTokens = nil, nil, nil
			c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
			return
		}
		items := data.IPs
		switch view {
		case "users":
			items = data.MultiIPUsers
		case "tokens":
			items = data.MultiIPTokens
		case "shared":
			items = []networkCountBucket{}
			for _, item := range data.IPs {
				if item.Users > 1 {
					items = append(items, item)
				}
			}
		}
		total := len(items)
		offset := (page - 1) * pageSize
		if offset >= total {
			items = []networkCountBucket{}
		} else {
			limit := offset + pageSize
			if limit > total {
				limit = total
			}
			items = items[offset:limit]
		}
		userLabels, tokenLabels, labelWarnings := s.networkRelationLabels(ctx, items, view)
		data.Metadata["relation_id_preview_limit"] = networkRelationIDPreviewLimit
		c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
			"event_type": source, "items": items, "total": total, "page": page, "page_size": pageSize,
			"window_start": start, "window_end": end, "as_of": data.AsOf, "sample_mode": "none",
			"coverage": data.Coverage, "metadata": data.Metadata,
			"user_labels": userLabels, "token_labels": tokenLabels,
			"warnings": append(data.Warnings, labelWarnings...),
		}})
	}
}

func (s *dashboardService) ipLookupHTTP(c *gin.Context) {
	ip, parseErr := networkFilterAddress(c.Param("ip"), true)
	if parseErr != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid IP address"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), queryTimeout)
	defer cancel()
	info, err := s.network.Lookup(ctx, ip)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "IP lookup is unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": info})
}

func (s *dashboardService) ipSyncHTTP(c *gin.Context) {
	status := s.network.queueCallSync("manual")
	c.JSON(http.StatusAccepted, gin.H{"success": true, "data": status})
}

func (s *dashboardService) ipAttributesHTTP(c *gin.Context) {
	s.saveNetworkAttributesHTTP(c)
}
