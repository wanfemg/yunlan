package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-sql-driver/mysql"
)

const networkRelationIDPreviewLimit = 20

type networkEventFilter struct {
	source          string
	start, end      int64
	userID, tokenID int64
	ip              string
}

func ensureNetworkInvestigationIndexes(ctx context.Context, db queryDB) error {
	for _, index := range []struct{ name, columns string }{
		{"siteadmin_net_ip_time", "ip,source,occurred_at"},
		{"siteadmin_net_token_time", "token_id,source,occurred_at"},
	} {
		prefix := "CREATE INDEX "
		if db.driver == "postgres" {
			prefix += "IF NOT EXISTS "
		}
		_, err := db.db.ExecContext(ctx, prefix+index.name+" ON siteadmin_network_events ("+index.columns+")")
		if err != nil {
			var duplicate *mysql.MySQLError
			if db.driver == "mysql" && errors.As(err, &duplicate) && duplicate.Number == 1061 {
				continue
			}
			return fmt.Errorf("initialize IP investigation index: %w", err)
		}
	}
	return nil
}

func networkFilterAddress(raw string, required bool) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" && !required {
		return "", nil
	}
	ip, class := classifyNetworkIP(raw)
	if class == "missing" || class == "invalid" {
		return "", errors.New("invalid IP address")
	}
	return ip, nil
}

func (filter networkEventFilter) matches(event networkEvent) bool {
	if event.Source != filter.source || event.OccurredAt < filter.start || event.OccurredAt >= filter.end ||
		filter.userID > 0 && event.UserID != filter.userID || filter.tokenID > 0 && event.TokenID != filter.tokenID {
		return false
	}
	ip, _ := classifyNetworkIP(event.IP)
	return filter.ip == "" || ip == filter.ip
}

func (filter networkEventFilter) predicate() (string, []any) {
	where := " WHERE source=? AND occurred_at>=? AND occurred_at<?"
	args := []any{filter.source, filter.start, filter.end}
	if filter.ip != "" {
		ip, _ := netip.ParseAddr(filter.ip)
		if ip.Is4() {
			// Canonical writes use IPv4. The alternate form also finds older
			// records whose indexed address was IPv4 mapped IPv6.
			where += " AND ip IN (?,?)"
			args = append(args, filter.ip, "::ffff:"+filter.ip)
		} else {
			where += " AND ip=?"
			args = append(args, filter.ip)
		}
	}
	if filter.userID > 0 {
		where += " AND user_id=?"
		args = append(args, filter.userID)
	}
	if filter.tokenID > 0 {
		where += " AND token_id=?"
		args = append(args, filter.tokenID)
	}
	return where, args
}

// Detailed views stream only the chosen address and window. Payload fields
// are projected explicitly below instead of returning arbitrary event data.
func (n *networkStore) visitFilteredNetworkEvents(ctx context.Context, filter networkEventFilter, visit func(networkEvent)) error {
	return n.visitFilteredNetworkEventsE(ctx, filter, func(event networkEvent) error {
		visit(event)
		return nil
	})
}

func (n *networkStore) visitFilteredNetworkEventsE(ctx context.Context, filter networkEventFilter, visit func(networkEvent) error) error {
	if err := n.ensure(ctx); err != nil {
		return err
	}
	if n.service.config.Mode == "demo" {
		n.mu.Lock()
		events := make([]networkEvent, 0)
		for _, event := range n.events {
			if filter.matches(event) {
				events = append(events, event)
			}
		}
		n.mu.Unlock()
		for _, event := range events {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := visit(event); err != nil {
				return err
			}
		}
		return nil
	}
	where, args := filter.predicate()
	rows, err := n.service.management.query(ctx, "SELECT payload FROM siteadmin_network_events"+where, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var payload string
		var event networkEvent
		if err := rows.Scan(&payload); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return errors.New("stored network observation is invalid")
		}
		if filter.matches(event) {
			if err := visit(event); err != nil {
				return err
			}
		}
	}
	return rows.Err()
}

func (n *networkStore) filteredNetworkEvents(ctx context.Context, filter networkEventFilter) ([]networkEvent, error) {
	if filter.userID == 0 && filter.tokenID == 0 && strings.TrimSpace(filter.ip) == "" {
		return n.Events(ctx, filter.source, filter.start, filter.end)
	}
	if filter.ip != "" {
		canonical, err := networkFilterAddress(filter.ip, true)
		if err != nil {
			return nil, err
		}
		filter.ip = canonical
	}
	events := []networkEvent{}
	err := n.visitFilteredNetworkEvents(ctx, filter, func(event networkEvent) {
		events = append(events, event)
	})
	return events, err
}

type networkTokenLabel struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	UserID int64  `json:"user_id"`
	Status int64  `json:"status"`
	Known  bool   `json:"known"`
}

func networkSafeUser(user managedUserRecord, known bool) riskIPMonitorUser {
	return riskIPMonitorUser{
		ID: user.ID, Username: user.Username, DisplayName: user.DisplayName,
		Email: user.Email, Status: user.Status, Role: user.Role, Known: known,
		RequestCount: user.RequestCount, UsedQuota: user.UsedQuota,
	}
}

func orderedNetworkIDs(ids map[int64]bool) []int64 {
	result := make([]int64, 0, len(ids))
	for id := range ids {
		if id > 0 {
			result = append(result, id)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func (s *dashboardService) networkUserLabels(ctx context.Context, ids map[int64]bool) (map[int64]riskIPMonitorUser, error) {
	result := make(map[int64]riskIPMonitorUser, len(ids))
	ordered := orderedNetworkIDs(ids)
	for _, id := range ordered {
		result[id] = riskIPMonitorUser{ID: id}
	}
	for offset := 0; offset < len(ordered); offset += 500 {
		batch := map[int64]bool{}
		for _, id := range ordered[offset:min(offset+500, len(ordered))] {
			batch[id] = true
		}
		users, err := s.riskRankingUserLabels(ctx, batch)
		if err != nil {
			return result, err
		}
		for id, user := range users {
			result[id] = networkSafeUser(user, true)
		}
	}
	return result, nil
}

func (s *dashboardService) networkTokenLabels(ctx context.Context, ids map[int64]bool) (map[int64]networkTokenLabel, error) {
	result := make(map[int64]networkTokenLabel, len(ids))
	ordered := orderedNetworkIDs(ids)
	for _, id := range ordered {
		result[id] = networkTokenLabel{ID: id}
	}
	if len(ordered) == 0 {
		return result, nil
	}
	if s.config.Mode == "demo" {
		store := s.demoTokenState()
		store.mu.Lock()
		defer store.mu.Unlock()
		for _, id := range ordered {
			if token, found := store.items[id]; found {
				result[id] = networkTokenLabel{ID: id, Name: token.Name, UserID: token.UserID, Status: token.Status, Known: true}
			}
		}
		return result, nil
	}
	if s.management.db == nil {
		return result, errors.New("token identity source is unavailable")
	}
	schema, err := s.management.query(ctx, "SELECT * FROM tokens WHERE 1=0")
	if err != nil {
		return result, err
	}
	columns, columnErr := schema.Columns()
	_ = schema.Close()
	if columnErr != nil {
		return result, columnErr
	}
	has := map[string]bool{}
	for _, column := range columns {
		has[strings.ToLower(column)] = true
	}
	if !has["id"] {
		return result, errors.New("token identity fields are unavailable")
	}
	column := func(name, fallback string) string {
		if has[name] {
			return "COALESCE(" + tokenIdentifier(s.management.driver, name) + "," + fallback + ")"
		}
		return fallback
	}
	for offset := 0; offset < len(ordered); offset += 500 {
		batch := ordered[offset:min(offset+500, len(ordered))]
		args := make([]any, len(batch))
		for index, id := range batch {
			args[index] = id
		}
		query := "SELECT id," + column("name", "''") + "," + column("user_id", "0") + "," + column("status", "0") +
			" FROM tokens WHERE id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",") + ")"
		if has["deleted_at"] {
			query += " AND deleted_at IS NULL"
		}
		rows, err := s.management.query(ctx, query, args...)
		if err != nil {
			return result, err
		}
		for rows.Next() {
			var token networkTokenLabel
			if err := rows.Scan(&token.ID, &token.Name, &token.UserID, &token.Status); err != nil {
				_ = rows.Close()
				return result, err
			}
			token.Known = true
			result[token.ID] = token
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

func (s *dashboardService) networkRelationLabels(ctx context.Context, items []networkCountBucket, view string) (map[int64]riskIPMonitorUser, map[int64]networkTokenLabel, []string) {
	users, tokens := map[int64]bool{}, map[int64]bool{}
	for index := range items {
		item := &items[index]
		if len(item.UserIDs) > networkRelationIDPreviewLimit {
			item.UserIDs = item.UserIDs[:networkRelationIDPreviewLimit]
			item.RelatedIDsTruncated = true
		}
		if len(item.TokenIDs) > networkRelationIDPreviewLimit {
			item.TokenIDs = item.TokenIDs[:networkRelationIDPreviewLimit]
			item.RelatedIDsTruncated = true
		}
		if view == "users" {
			if id, err := strconv.ParseInt(item.Key, 10, 64); err == nil && id > 0 {
				users[id] = true
			}
		}
		if view == "tokens" {
			if id, err := strconv.ParseInt(item.Key, 10, 64); err == nil && id > 0 {
				tokens[id] = true
			}
		}
		for _, id := range item.UserIDs {
			users[id] = true
		}
		for _, id := range item.TokenIDs {
			tokens[id] = true
		}
	}
	warnings := []string{}
	tokenLabels, err := s.networkTokenLabels(ctx, tokens)
	if err != nil {
		warnings = append(warnings, "部分令牌资料暂不可用，已保留原始令牌 ID。")
	}
	for _, token := range tokenLabels {
		if token.UserID > 0 {
			users[token.UserID] = true
		}
	}
	userLabels, err := s.networkUserLabels(ctx, users)
	if err != nil {
		warnings = append(warnings, "部分账号资料暂不可用，已保留原始用户 ID。")
	}
	return userLabels, tokenLabels, warnings
}

type networkIPDetailSummary struct {
	Events             int64 `json:"events"`
	Success            int64 `json:"success"`
	Failure            int64 `json:"failure"`
	Pending            int64 `json:"pending"`
	Users              int   `json:"users"`
	Tokens             int   `json:"tokens"`
	Browsers           int   `json:"browsers"`
	UnassociatedEvents int64 `json:"unassociated_events"`
	FirstSeenAt        int64 `json:"first_seen_at"`
	LastSeenAt         int64 `json:"last_seen_at"`
}

type networkIPDetailSource struct {
	Source     string                  `json:"source"`
	Applicable bool                    `json:"applicable"`
	Summary    *networkIPDetailSummary `json:"summary"`
}

type networkIPDetailUser struct {
	User         riskIPMonitorUser `json:"user"`
	Events       int64             `json:"events"`
	Success      int64             `json:"success"`
	Failure      int64             `json:"failure"`
	FirstSeenAt  int64             `json:"first_seen_at"`
	LastSeenAt   int64             `json:"last_seen_at"`
	TokenCount   int               `json:"token_count"`
	BrowserCount int               `json:"browser_count"`
}

type networkIPDetailToken struct {
	networkTokenLabel
	Events      int64 `json:"events"`
	Success     int64 `json:"success"`
	Failure     int64 `json:"failure"`
	FirstSeenAt int64 `json:"first_seen_at"`
	LastSeenAt  int64 `json:"last_seen_at"`
}

type networkIPDetailEvent struct {
	ID         string            `json:"id"`
	Source     string            `json:"source"`
	OccurredAt int64             `json:"occurred_at"`
	Result     string            `json:"result"`
	UserID     int64             `json:"user_id"`
	TokenID    int64             `json:"token_id"`
	Method     string            `json:"method"`
	BrowserID  string            `json:"browser_id"`
	Model      string            `json:"model"`
	Device     map[string]string `json:"device"`
}

type networkIPDetailDevice struct {
	BrowserID   string              `json:"browser_id"`
	UserIDs     []int64             `json:"user_ids"`
	Users       []riskIPMonitorUser `json:"users"`
	Events      int64               `json:"events"`
	FirstSeenAt int64               `json:"first_seen_at"`
	LastSeenAt  int64               `json:"last_seen_at"`
}

type networkInvestigationBucket struct {
	summary  networkIPDetailSummary
	users    map[int64]bool
	tokens   map[int64]bool
	browsers map[string]bool
}

func newNetworkInvestigationBucket() *networkInvestigationBucket {
	return &networkInvestigationBucket{
		users: map[int64]bool{}, tokens: map[int64]bool{}, browsers: map[string]bool{},
	}
}

func (bucket *networkInvestigationBucket) add(event networkEvent) {
	bucket.summary.Events++
	switch event.Result {
	case "success":
		bucket.summary.Success++
	case "failure":
		bucket.summary.Failure++
	case "pending":
		bucket.summary.Pending++
	}
	if bucket.summary.FirstSeenAt == 0 || event.OccurredAt < bucket.summary.FirstSeenAt {
		bucket.summary.FirstSeenAt = event.OccurredAt
	}
	bucket.summary.LastSeenAt = max(bucket.summary.LastSeenAt, event.OccurredAt)
	if event.UserID > 0 {
		bucket.users[event.UserID] = true
	} else {
		bucket.summary.UnassociatedEvents++
	}
	if event.Source == networkSourceCall && event.TokenID > 0 {
		bucket.tokens[event.TokenID] = true
	}
	if event.Source != networkSourceCall && event.Result == "success" && event.UserID > 0 && event.BrowserID != "" {
		bucket.browsers[event.BrowserID] = true
	}
}

func (bucket *networkInvestigationBucket) finish() networkIPDetailSummary {
	summary := bucket.summary
	summary.Users, summary.Tokens, summary.Browsers = len(bucket.users), len(bucket.tokens), len(bucket.browsers)
	return summary
}

func projectNetworkInvestigationEvent(event networkEvent) networkIPDetailEvent {
	result := networkIPDetailEvent{
		ID: event.ID, Source: event.Source, OccurredAt: event.OccurredAt,
		Result: event.Result, UserID: event.UserID, Device: map[string]string{},
	}
	if event.Source == networkSourceCall {
		result.TokenID, result.Model = event.TokenID, event.Metadata["model"]
	} else {
		result.Method, result.BrowserID = event.Method, event.BrowserID
		result.Device = userObservationDeviceFields(event.Device)
	}
	return result
}

func (n *networkStore) networkInvestigationEventsPage(ctx context.Context, filter networkEventFilter, offset, limit int) ([]networkIPDetailEvent, error) {
	result := []networkIPDetailEvent{}
	if n.service.config.Mode == "demo" {
		events, err := n.filteredNetworkEvents(ctx, filter)
		if err != nil {
			return nil, err
		}
		sort.Slice(events, func(i, j int) bool {
			if events[i].OccurredAt != events[j].OccurredAt {
				return events[i].OccurredAt > events[j].OccurredAt
			}
			return events[i].ID > events[j].ID
		})
		if offset < len(events) {
			for _, event := range events[offset:min(offset+limit, len(events))] {
				result = append(result, projectNetworkInvestigationEvent(event))
			}
		}
		return result, nil
	}
	where, args := filter.predicate()
	args = append(args, limit, offset)
	rows, err := n.service.management.query(ctx,
		"SELECT payload FROM siteadmin_network_events"+where+" ORDER BY occurred_at DESC,id DESC LIMIT ? OFFSET ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var payload string
		var event networkEvent
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		if json.Unmarshal([]byte(payload), &event) != nil {
			return nil, errors.New("stored network observation is invalid")
		}
		if filter.matches(event) {
			result = append(result, projectNetworkInvestigationEvent(event))
		}
	}
	return result, rows.Err()
}

func networkInvestigationUserRows(buckets map[int64]*networkInvestigationBucket) []networkIPDetailUser {
	items := make([]networkIPDetailUser, 0, len(buckets))
	for id, bucket := range buckets {
		value := bucket.finish()
		items = append(items, networkIPDetailUser{
			User: riskIPMonitorUser{ID: id}, Events: value.Events, Success: value.Success, Failure: value.Failure,
			FirstSeenAt: value.FirstSeenAt, LastSeenAt: value.LastSeenAt, TokenCount: value.Tokens, BrowserCount: value.Browsers,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Events != items[j].Events {
			return items[i].Events > items[j].Events
		}
		return items[i].User.ID < items[j].User.ID
	})
	return items
}

func networkInvestigationTokenRows(buckets map[int64]*networkInvestigationBucket) []networkIPDetailToken {
	items := make([]networkIPDetailToken, 0, len(buckets))
	for id, bucket := range buckets {
		value := bucket.finish()
		items = append(items, networkIPDetailToken{
			networkTokenLabel: networkTokenLabel{ID: id}, Events: value.Events, Success: value.Success,
			Failure: value.Failure, FirstSeenAt: value.FirstSeenAt, LastSeenAt: value.LastSeenAt,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Events != items[j].Events {
			return items[i].Events > items[j].Events
		}
		return items[i].ID < items[j].ID
	})
	return items
}

func networkInvestigationDeviceRows(buckets map[string]*networkInvestigationBucket) []networkIPDetailDevice {
	items := make([]networkIPDetailDevice, 0, len(buckets))
	for id, bucket := range buckets {
		value := bucket.finish()
		items = append(items, networkIPDetailDevice{
			BrowserID: id, UserIDs: orderedNetworkIDs(bucket.users), Users: []riskIPMonitorUser{},
			Events: value.Events, FirstSeenAt: value.FirstSeenAt, LastSeenAt: value.LastSeenAt,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Events != items[j].Events {
			return items[i].Events > items[j].Events
		}
		return items[i].BrowserID < items[j].BrowserID
	})
	return items
}

func networkInvestigationPage[T any](items []T, offset, limit int) []T {
	if offset >= len(items) {
		return []T{}
	}
	return items[offset:min(offset+limit, len(items))]
}

func (s *dashboardService) ipDetailsHTTP(c *gin.Context) {
	ip, err := networkFilterAddress(c.Param("ip"), true)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "IP 地址无效"})
		return
	}
	source, start, end, err := networkHTTPWindow(c, userClock(s))
	userID, userErr := networkHTTPID(c.Query("user_id"))
	tokenID, tokenErr := networkHTTPID(c.Query("token_id"))
	page, pageErr := positiveLogInt(c.Query("page"), 1, 1000000)
	pageSize, sizeErr := positiveLogInt(c.Query("page_size"), 20, 100)
	view := c.DefaultQuery("view", "users")
	syncOption := c.DefaultQuery("sync", "true")
	if err != nil || userErr != nil || tokenErr != nil || pageErr != nil || sizeErr != nil ||
		syncOption != "true" && syncOption != "false" ||
		tokenID > 0 && source != networkSourceCall ||
		(view != "users" && view != "tokens" && view != "events" && view != "devices") ||
		view == "tokens" && source != networkSourceCall || view == "devices" && source == networkSourceCall {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "IP 明细来源、时间、筛选或分页无效"})
		return
	}
	if s.network == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "error": "IP 事件采集尚未初始化"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()
	warnings := []string{}
	syncComplete, syncAttempted := source != networkSourceCall, source == networkSourceCall && syncOption != "false"
	syncStatus := "not_applicable"
	if source == networkSourceCall {
		syncStatus = "skipped"
	}
	if syncAttempted {
		syncContext, stopSync := context.WithTimeout(ctx, 5*time.Second)
		status, syncErr := s.network.SyncCalls(syncContext)
		stopSync()
		syncComplete = syncErr == nil
		if syncComplete {
			syncComplete, _ = status["complete"].(bool)
		}
		if !syncComplete {
			syncStatus = "incomplete"
			warnings = append(warnings, "调用日志本轮同步未完成，以下仅为已采集事件。")
		} else {
			syncStatus = "complete"
		}
	}
	filter := networkEventFilter{source: source, start: start, end: end, userID: userID, tokenID: tokenID, ip: ip}
	selected := newNetworkInvestigationBucket()
	byUser, byToken := map[int64]*networkInvestigationBucket{}, map[int64]*networkInvestigationBucket{}
	byDevice := map[string]*networkInvestigationBucket{}
	sources := []networkIPDetailSource{}
	for _, currentSource := range []string{networkSourceCall, networkSourceLogin, networkSourceRegistration} {
		if tokenID > 0 && currentSource != networkSourceCall {
			sources = append(sources, networkIPDetailSource{Source: currentSource, Applicable: false})
			continue
		}
		current := newNetworkInvestigationBucket()
		currentFilter := filter
		currentFilter.source = currentSource
		readErr := s.network.visitFilteredNetworkEvents(ctx, currentFilter, func(event networkEvent) {
			current.add(event)
			if currentSource != source {
				return
			}
			selected.add(event)
			if event.UserID > 0 {
				if byUser[event.UserID] == nil {
					byUser[event.UserID] = newNetworkInvestigationBucket()
				}
				byUser[event.UserID].add(event)
			}
			if currentSource == networkSourceCall && event.TokenID > 0 {
				if byToken[event.TokenID] == nil {
					byToken[event.TokenID] = newNetworkInvestigationBucket()
				}
				byToken[event.TokenID].add(event)
			}
			if currentSource != networkSourceCall && event.UserID > 0 && event.Result == "success" && event.BrowserID != "" {
				if byDevice[event.BrowserID] == nil {
					byDevice[event.BrowserID] = newNetworkInvestigationBucket()
				}
				byDevice[event.BrowserID].add(event)
			}
		})
		if readErr != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "error": "IP 活动明细暂不可用"})
			return
		}
		value := current.finish()
		sources = append(sources, networkIPDetailSource{Source: currentSource, Applicable: true, Summary: &value})
	}
	users, tokens, events, devices := []networkIPDetailUser{}, []networkIPDetailToken{}, []networkIPDetailEvent{}, []networkIPDetailDevice{}
	offset := (page - 1) * pageSize
	switch view {
	case "users":
		users = networkInvestigationPage(networkInvestigationUserRows(byUser), offset, pageSize)
	case "tokens":
		tokens = networkInvestigationPage(networkInvestigationTokenRows(byToken), offset, pageSize)
	case "events":
		events, err = s.network.networkInvestigationEventsPage(ctx, filter, offset, pageSize)
	case "devices":
		devices = networkInvestigationPage(networkInvestigationDeviceRows(byDevice), offset, pageSize)
	}
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "error": "IP 事件分页暂不可用"})
		return
	}
	userIDs, tokenIDs := map[int64]bool{}, map[int64]bool{}
	for _, user := range users {
		userIDs[user.User.ID] = true
	}
	for _, token := range tokens {
		tokenIDs[token.ID] = true
	}
	for _, event := range events {
		if event.UserID > 0 {
			userIDs[event.UserID] = true
		}
		if event.TokenID > 0 {
			tokenIDs[event.TokenID] = true
		}
	}
	for _, device := range devices {
		for _, id := range device.UserIDs {
			userIDs[id] = true
		}
	}
	tokenLabels, labelErr := s.networkTokenLabels(ctx, tokenIDs)
	if labelErr != nil {
		warnings = append(warnings, "部分令牌资料暂不可用，事件统计未受影响。")
	}
	for _, token := range tokenLabels {
		if token.UserID > 0 {
			userIDs[token.UserID] = true
		}
	}
	userLabels, labelErr := s.networkUserLabels(ctx, userIDs)
	if labelErr != nil {
		warnings = append(warnings, "部分账号资料暂不可用，事件统计未受影响。")
	}
	for index := range users {
		users[index].User = userLabels[users[index].User.ID]
	}
	for index := range tokens {
		tokens[index].networkTokenLabel = tokenLabels[tokens[index].ID]
	}
	for index := range devices {
		for _, id := range devices[index].UserIDs {
			devices[index].Users = append(devices[index].Users, userLabels[id])
		}
	}
	info, infoErr := s.network.Lookup(ctx, ip)
	if infoErr != nil {
		_, class := classifyNetworkIP(ip)
		info = networkIPInfo{IP: ip, AddressClass: class, NetworkType: "unknown",
			Proxy: defaultProxyAttributes(), DataSource: "unavailable", FieldSources: map[string]string{}}
		warnings = append(warnings, "IP 网络画像暂不可用，已保留活动记录。")
	}
	if source != networkSourceCall {
		warnings = append(warnings, "登录与注册历史覆盖率尚未测量；无观测记录不代表没有认证行为。")
	}
	metadata, metadataErr := s.network.Metadata(ctx)
	if metadataErr != nil {
		metadata = map[string]any{
			"history_complete": s.config.NetworkHistoryComplete, "history_scope": "api_call_only",
			"probe_history_complete": false, "metadata_available": false,
		}
		warnings = append(warnings, "采集状态元信息暂不可用，历史完整性无法进一步核实。")
	} else {
		metadata["metadata_available"] = true
	}
	metadata["call_sync_attempted"], metadata["call_sync_status"] = syncAttempted, syncStatus
	metadata["aggregation_scope"], metadata["sample_mode"] = "retained_events", "none"
	metadata["window_is_dataset_snapshot"] = false
	metadata["intelligence"] = s.network.ipIntelligenceStatus()
	warnings = append(warnings, "共享 IP、代理与多个调用出口均为关联线索，不单独作为封禁依据。")
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
		"info": info, "source": source, "window_start": start, "window_end": end, "as_of": end - 1,
		"timezone": userActivityTimezone(s.config.Location), "quota_per_unit": s.config.QuotaPerUnit,
		"summary": selected.finish(), "sources": sources, "users": users, "tokens": tokens,
		"events": events, "devices": devices, "user_labels": userLabels, "token_labels": tokenLabels,
		"totals": gin.H{"users": len(byUser), "tokens": len(byToken), "events": selected.summary.Events, "devices": len(byDevice)},
		"view":   view, "page": page, "page_size": pageSize, "warnings": warnings, "sync_complete": syncComplete,
		"metadata": metadata,
	}})
}
