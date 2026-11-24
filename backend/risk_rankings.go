// Adapted from new_api_tools' realtime risk leaderboard, MIT License.
// See frontend/src/tools-reference-LICENSE.txt.
package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const riskRankingLimit = 10
const riskRankingIPGroupLimit = 10000

type riskRankingItem struct {
	UserID           int64    `json:"user_id"`
	Username         string   `json:"username"`
	DisplayName      string   `json:"display_name"`
	UserStatus       int64    `json:"user_status"`
	Role             int64    `json:"role"`
	Group            string   `json:"group"`
	IdentityKnown    bool     `json:"identity_known"`
	RequestCount     int64    `json:"request_count"`
	FailureRequests  int64    `json:"failure_requests"`
	FailureRate      *float64 `json:"failure_rate"`
	QuotaUsed        int64    `json:"quota_used"`
	PromptTokens     *int64   `json:"prompt_tokens"`
	CompletionTokens *int64   `json:"completion_tokens"`
	UniqueIPs        *int64   `json:"unique_ips"`
	IPCountComplete  bool     `json:"ip_count_complete"`
}

type riskRankingWindow struct {
	Window      string            `json:"window"`
	WindowStart int64             `json:"window_start"`
	WindowEnd   int64             `json:"window_end"`
	Items       []riskRankingItem `json:"items"`
}

type riskRankingsData struct {
	AsOf         int64               `json:"as_of"`
	Timezone     string              `json:"timezone"`
	QuotaPerUnit float64             `json:"quota_per_unit"`
	Sort         string              `json:"sort"`
	Windows      []riskRankingWindow `json:"windows"`
	Warnings     []string            `json:"warnings"`
	DataNotes    []string            `json:"data_notes"`
}

type riskRankingFilters struct {
	sort    string
	windows []string
}

func parseRiskRankingFilters(c *gin.Context) (riskRankingFilters, error) {
	f := riskRankingFilters{sort: c.DefaultQuery("sort", c.DefaultQuery("sort_by", "requests"))}
	switch f.sort {
	case "requests", "quota", "failure_rate":
	default:
		return f, riskFailure(http.StatusBadRequest, "排行排序须为请求次数、额度消耗或失败率。")
	}
	if raw := c.Query("window"); raw != "" {
		f.windows = []string{raw}
	} else if raw := c.Query("windows"); raw != "" {
		f.windows = strings.Split(raw, ",")
	} else {
		long := c.DefaultQuery("long_window", "24h")
		if long != "24h" && long != "3d" && long != "7d" {
			return f, riskFailure(http.StatusBadRequest, "长期排行窗口须为 24 小时、3 天或 7 天。")
		}
		f.windows = []string{"1h", "3h", "6h", "12h", long}
	}
	if len(f.windows) < 1 || len(f.windows) > len(tokenAnalysisWindows) {
		return f, riskFailure(http.StatusBadRequest, "排行窗口数量无效。")
	}
	seen := make(map[string]bool, len(f.windows))
	for _, window := range f.windows {
		if _, valid := tokenAnalysisWindows[window]; !valid || seen[window] {
			return f, riskFailure(http.StatusBadRequest, "排行窗口无效或重复。")
		}
		seen[window] = true
	}
	return f, nil
}

func (s *dashboardService) riskLeaderboardsHTTP(c *gin.Context) {
	filter, err := parseRiskRankingFilters(c)
	if err != nil {
		riskHTTPError(c, err)
		return
	}
	now := userClock(s).Unix()
	location := s.config.Location
	if location == nil {
		location = time.UTC
	}
	data := riskRankingsData{
		AsOf: now, Timezone: s.config.Timezone, QuotaPerUnit: s.config.QuotaPerUnit, Sort: filter.sort,
		Windows:  make([]riskRankingWindow, 0, len(filter.windows)),
		Warnings: []string{},
		DataNotes: []string{
			"按 NewAPI 消费与错误日志记录统计，包含渠道重试；失败率不是去重后的最终请求失败率。",
			"IP 数只统计当前窗口实际记录的有效调用出口，未采集的地址无法补回；调用 IP 不等同于登录 IP。",
		},
	}
	if data.Timezone == "" {
		data.Timezone = location.String()
	}
	for _, window := range filter.windows {
		data.Windows = append(data.Windows, riskRankingWindow{
			Window: window, WindowStart: now - tokenAnalysisWindows[window], WindowEnd: now,
			Items: []riskRankingItem{},
		})
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), queryTimeout)
	defer cancel()
	if s.config.Mode == "demo" {
		err = s.readDemoRiskRankings(ctx, &data)
	} else {
		err = s.readRiskRankings(ctx, &data)
	}
	if err != nil {
		riskHTTPError(c, riskFailure(http.StatusServiceUnavailable, "实时排行暂不可用，请检查日志库连接及统计字段。"))
		return
	}
	s.enrichRiskRankingUsers(ctx, &data)
	c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
}

type riskRankingLogSchema struct {
	driver string
	has    map[string]bool
}

func inspectRiskRankingLogs(ctx context.Context, tx *sql.Tx, driver string) (riskRankingLogSchema, error) {
	schema := riskRankingLogSchema{driver: driver, has: map[string]bool{}}
	rows, err := tx.QueryContext(ctx, "SELECT * FROM logs WHERE 1 = 0")
	if err != nil {
		return schema, err
	}
	columns, err := rows.Columns()
	_ = rows.Close()
	if err != nil {
		return schema, err
	}
	for _, column := range columns {
		schema.has[strings.ToLower(column)] = true
	}
	for _, required := range []string{"created_at", "type", "user_id", "quota"} {
		if !schema.has[required] {
			return schema, errors.New("leaderboard fields are unavailable")
		}
	}
	return schema, nil
}

func (schema riskRankingLogSchema) where(window riskRankingWindow) (string, []any) {
	where := " WHERE created_at >= ? AND created_at < ? AND type IN (2,5) AND user_id > 0"
	if schema.has["deleted_at"] {
		where += " AND deleted_at IS NULL"
	}
	return where, []any{window.WindowStart, window.WindowEnd}
}

func (schema riskRankingLogSchema) tokenAggregate(column string) (string, string) {
	if !schema.has[column] {
		return "0", "SUM(CASE WHEN type = 2 THEN 1 ELSE 0 END)"
	}
	quoted := tokenIdentifier(schema.driver, column)
	return "COALESCE(SUM(CASE WHEN type = 2 AND " + quoted + " >= 0 THEN " + quoted + " ELSE 0 END),0)",
		"SUM(CASE WHEN type = 2 AND (" + quoted + " IS NULL OR " + quoted + " < 0) THEN 1 ELSE 0 END)"
}

func (s *dashboardService) readRiskRankings(ctx context.Context, data *riskRankingsData) error {
	db := s.logs
	if db.db == nil {
		return errors.New("leaderboard log database is not configured")
	}
	tx, err := db.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	schema, err := inspectRiskRankingLogs(ctx, tx, db.driver)
	if err != nil {
		return err
	}
	if !schema.has["ip"] {
		data.Warnings = append(data.Warnings, "日志表没有 IP 字段，调用 IP 数显示未知。")
	}
	if !schema.has["prompt_tokens"] || !schema.has["completion_tokens"] {
		data.Warnings = append(data.Warnings, "部分 Token 统计字段缺失，对应 Token 数显示未知。")
	}
	for index := range data.Windows {
		window := &data.Windows[index]
		where, args := schema.where(*window)
		prompt, promptUnknown := schema.tokenAggregate("prompt_tokens")
		completion, completionUnknown := schema.tokenAggregate("completion_tokens")
		username := "''"
		if schema.has["username"] {
			username = "COALESCE(MAX(username),'')"
		}
		query := "SELECT user_id," + username + ",COUNT(*) AS request_count," +
			"SUM(CASE WHEN type = 5 THEN 1 ELSE 0 END) AS failure_requests," +
			"100.0 * SUM(CASE WHEN type = 5 THEN 1 ELSE 0 END) / COUNT(*) AS failure_rate," +
			"COALESCE(SUM(CASE WHEN type = 2 THEN quota ELSE 0 END),0) AS quota_used," +
			prompt + "," + promptUnknown + "," + completion + "," + completionUnknown +
			" FROM logs" + where + " GROUP BY user_id"
		order := "request_count DESC,user_id ASC"
		switch data.Sort {
		case "quota":
			order = "quota_used DESC,request_count DESC,user_id ASC"
		case "failure_rate":
			order = "failure_rate DESC,request_count DESC,user_id ASC"
		}
		query += " ORDER BY " + order + " LIMIT " + strconv.Itoa(riskRankingLimit)
		rows, err := tx.QueryContext(ctx, bindQuery(db.driver, query), args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var item riskRankingItem
			var failureRate float64
			var promptTokens, completionTokens, missingPrompt, missingCompletion int64
			if err := rows.Scan(&item.UserID, &item.Username, &item.RequestCount, &item.FailureRequests, &failureRate,
				&item.QuotaUsed, &promptTokens, &missingPrompt, &completionTokens, &missingCompletion); err != nil {
				_ = rows.Close()
				return err
			}
			item.FailureRate = float64Pointer(failureRate)
			if schema.has["prompt_tokens"] && missingPrompt == 0 {
				item.PromptTokens = int64Pointer(promptTokens)
			}
			if schema.has["completion_tokens"] && missingCompletion == 0 {
				item.CompletionTokens = int64Pointer(completionTokens)
			}
			window.Items = append(window.Items, item)
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return err
		}
		if schema.has["ip"] && len(window.Items) > 0 {
			complete, err := readRiskRankingIPs(ctx, tx, schema, window)
			if err != nil {
				return err
			}
			if !complete {
				data.Warnings = append(data.Warnings, window.Window+" 窗口的调用 IP 关联超过读取上限，IP 数显示未知。")
			}
		}
	}
	return tx.Commit()
}

// SQL reduces log volume to distinct (ranked account, raw IP) pairs. Only
// these pairs enter memory, and a cap prevents adversarial address strings
// from turning a Top10 query into an unbounded read. Canonicalize IPv6 and
// mapped IPv4 before counting. Never substitute a capped sample for the total.
func readRiskRankingIPs(ctx context.Context, tx *sql.Tx, schema riskRankingLogSchema, window *riskRankingWindow) (bool, error) {
	where, args := schema.where(*window)
	positions := make(map[int64]int, len(window.Items))
	for index, item := range window.Items {
		positions[item.UserID] = index
		args = append(args, item.UserID)
	}
	where += " AND user_id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(window.Items)), ",") + ")" +
		" AND ip IS NOT NULL AND ip <> ''"
	statement := "SELECT user_id,ip FROM logs" + where + " GROUP BY user_id,ip LIMIT " + strconv.Itoa(riskRankingIPGroupLimit+1)
	rows, err := tx.QueryContext(ctx, bindQuery(schema.driver, statement), args...)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	ips := make(map[int64]map[string]struct{}, len(window.Items))
	count := 0
	for rows.Next() {
		count++
		if count > riskRankingIPGroupLimit {
			return false, nil
		}
		var userID int64
		var raw string
		if err := rows.Scan(&userID, &raw); err != nil {
			return false, err
		}
		canonical, kind := classifyNetworkIP(raw)
		if kind == "missing" || kind == "invalid" {
			continue
		}
		if ips[userID] == nil {
			ips[userID] = map[string]struct{}{}
		}
		ips[userID][canonical] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	for id, index := range positions {
		window.Items[index].UniqueIPs = int64Pointer(int64(len(ips[id])))
		window.Items[index].IPCountComplete = true
	}
	return true, nil
}

type riskDemoRankingState struct {
	item              riskRankingItem
	ips               map[string]struct{}
	promptTokens      int64
	completionTokens  int64
	promptUnknown     bool
	completionUnknown bool
}

func (s *dashboardService) readDemoRiskRankings(ctx context.Context, data *riskRankingsData) error {
	if s.network == nil {
		return errors.New("demo usage events are unavailable")
	}
	start := data.AsOf
	for _, window := range data.Windows {
		if window.WindowStart < start {
			start = window.WindowStart
		}
	}
	events, err := s.network.Events(ctx, networkSourceCall, start, data.AsOf)
	if err != nil {
		return err
	}
	for index := range data.Windows {
		window := &data.Windows[index]
		users := make(map[int64]*riskDemoRankingState)
		for _, event := range events {
			if err := ctx.Err(); err != nil {
				return err
			}
			if event.UserID < 1 || event.OccurredAt < window.WindowStart || event.OccurredAt >= window.WindowEnd ||
				(event.Result != "success" && event.Result != "failure") {
				continue
			}
			state := users[event.UserID]
			if state == nil {
				state = &riskDemoRankingState{item: riskRankingItem{UserID: event.UserID}, ips: map[string]struct{}{}}
				users[event.UserID] = state
			}
			state.item.RequestCount++
			if event.Result == "failure" {
				state.item.FailureRequests++
			} else {
				state.item.QuotaUsed += demoTokenInt(event, "quota")
				prompt, promptErr := strconv.ParseInt(event.Metadata["prompt_tokens"], 10, 64)
				if promptErr != nil || prompt < 0 {
					state.promptUnknown = true
				} else {
					state.promptTokens += prompt
				}
				completion, completionErr := strconv.ParseInt(event.Metadata["completion_tokens"], 10, 64)
				if completionErr != nil || completion < 0 {
					state.completionUnknown = true
				} else {
					state.completionTokens += completion
				}
			}
			canonical, kind := classifyNetworkIP(event.IP)
			if kind != "missing" && kind != "invalid" {
				state.ips[canonical] = struct{}{}
			}
		}
		for _, state := range users {
			item := state.item
			item.FailureRate = float64Pointer(float64(item.FailureRequests) * 100 / float64(item.RequestCount))
			item.UniqueIPs, item.IPCountComplete = int64Pointer(int64(len(state.ips))), true
			if !state.promptUnknown {
				item.PromptTokens = int64Pointer(state.promptTokens)
			}
			if !state.completionUnknown {
				item.CompletionTokens = int64Pointer(state.completionTokens)
			}
			window.Items = append(window.Items, item)
		}
		sort.Slice(window.Items, func(left, right int) bool {
			a, b := window.Items[left], window.Items[right]
			switch data.Sort {
			case "quota":
				if a.QuotaUsed != b.QuotaUsed {
					return a.QuotaUsed > b.QuotaUsed
				}
			case "failure_rate":
				if *a.FailureRate != *b.FailureRate {
					return *a.FailureRate > *b.FailureRate
				}
			}
			if a.RequestCount != b.RequestCount {
				return a.RequestCount > b.RequestCount
			}
			return a.UserID < b.UserID
		})
		window.Items = window.Items[:min(len(window.Items), riskRankingLimit)]
	}
	return nil
}

// The logs database can be separate from the management database. Read only
// the at-most 50 ranked account IDs once; current identity and status must not
// be inferred from log history or queried separately for every leaderboard row.
func (s *dashboardService) enrichRiskRankingUsers(ctx context.Context, data *riskRankingsData) {
	ids := make(map[int64]bool)
	for _, window := range data.Windows {
		for _, item := range window.Items {
			ids[item.UserID] = true
		}
	}
	if len(ids) == 0 {
		return
	}
	byID, err := s.riskRankingUserLabels(ctx, ids)
	if err != nil {
		data.Warnings = append(data.Warnings, "当前用户资料暂不可用，保留历史排行；资料未知的账号不能快捷封禁。")
		return
	}
	for index := range data.Windows {
		for itemIndex := range data.Windows[index].Items {
			item := &data.Windows[index].Items[itemIndex]
			user, known := byID[item.UserID]
			if !known {
				continue
			}
			item.Username, item.DisplayName = user.Username, user.DisplayName
			item.UserStatus, item.Role, item.Group, item.IdentityKnown = user.Status, user.Role, user.Group, true
		}
	}
}

func (s *dashboardService) riskRankingUserLabels(ctx context.Context, ids map[int64]bool) (map[int64]managedUserRecord, error) {
	result := make(map[int64]managedUserRecord, len(ids))
	if s.config.Mode == "demo" {
		users, err := s.managedUsers(ctx, 0)
		if err != nil {
			return nil, err
		}
		for _, user := range users {
			if ids[user.ID] {
				result[user.ID] = user
			}
		}
		return result, nil
	}
	db := s.management
	if db.db == nil {
		return nil, errors.New("management database is unavailable")
	}
	schema, err := db.query(ctx, "SELECT * FROM users WHERE 1 = 0")
	if err != nil {
		return nil, err
	}
	columns, err := schema.Columns()
	_ = schema.Close()
	if err != nil {
		return nil, err
	}
	has := make(map[string]bool, len(columns))
	for _, column := range columns {
		has[strings.ToLower(column)] = true
	}
	for _, column := range []string{"id", "username", "role", "status"} {
		if !has[column] {
			return nil, errors.New("user identity fields are unavailable")
		}
	}
	column := func(name, fallback string) string {
		if !has[name] {
			return fallback
		}
		return "COALESCE(" + tokenIdentifier(db.driver, name) + "," + fallback + ")"
	}
	orderedIDs := make([]int64, 0, len(ids))
	for id := range ids {
		orderedIDs = append(orderedIDs, id)
	}
	sort.Slice(orderedIDs, func(left, right int) bool { return orderedIDs[left] < orderedIDs[right] })
	args := make([]any, len(orderedIDs))
	for index, id := range orderedIDs {
		args[index] = id
	}
	query := "SELECT id,username," + column("display_name", "''") + ",role,status," + column("group", "'default'") +
		" FROM users WHERE id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(args)), ",") + ")"
	if has["deleted_at"] {
		query += " AND deleted_at IS NULL"
	}
	rows, err := db.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var user managedUserRecord
		if err := rows.Scan(&user.ID, &user.Username, &user.DisplayName, &user.Role, &user.Status, &user.Group); err != nil {
			return nil, err
		}
		result[user.ID] = user
	}
	return result, rows.Err()
}
