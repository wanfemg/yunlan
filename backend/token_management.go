package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const maxTokenBatch = 500

var tokenAnalysisWindows = map[string]int64{
	"1h": 3600, "3h": 3 * 3600, "6h": 6 * 3600, "12h": 12 * 3600,
	"24h": 24 * 3600, "3d": 3 * 86400, "7d": 7 * 86400,
	"30d": 30 * 86400, "90d": 90 * 86400,
}

type tokenListFilters struct {
	page, pageSize                          int
	status, name, key, group, expired, risk string
	userID                                  int64
}

func registerTokenManagementRoutes(router *gin.Engine, service *dashboardService, adminToken string) {
	g := router.Group("/api/tokens", requireAdminToken(adminToken))
	g.GET("", service.tokensListHTTP)
	g.GET("/statistics", service.tokenStatisticsHTTP)
	g.GET("/groups", service.tokenGroupsHTTP)
	g.POST("/lookup", service.tokenLookupHTTP)
	g.POST("/batch-disable", service.tokenBatchHTTP(false))
	g.POST("/batch-enable", service.tokenBatchHTTP(true))
	g.GET("/ip-stats", service.tokenIPStatsHTTP)
	g.GET("/suspected-leaks", service.tokenLeaksHTTP)
	g.GET("/:id/analysis", service.tokenAnalysisHTTP)
}

func (s *dashboardService) tokenManagementReady(c *gin.Context) bool {
	if s.management.db != nil {
		return true
	}
	c.JSON(http.StatusServiceUnavailable, gin.H{"error": "token management database is not configured"})
	return false
}

func tokenIdentifier(driver, column string) string {
	if driver == "postgres" {
		return `"` + column + `"`
	}
	return "`" + column + "`"
}

func (s *dashboardService) parseTokenFilters(c *gin.Context) (tokenListFilters, error) {
	f := tokenListFilters{status: c.DefaultQuery("status", "all"), name: strings.TrimSpace(c.Query("name")),
		key: strings.TrimSpace(c.Query("key")), group: strings.TrimSpace(c.Query("group")),
		expired: c.DefaultQuery("expired", "all"), risk: c.DefaultQuery("risk", "all")}
	var err error
	if f.page, err = positiveLogInt(c.Query("page"), 1, 2147483647); err != nil {
		return f, fmt.Errorf("页码无效：%w", err)
	}
	if f.pageSize, err = positiveLogInt(c.Query("page_size"), 20, 100); err != nil {
		return f, fmt.Errorf("每页数量无效：%w", err)
	}
	if f.page-1 > (2147483647-f.pageSize)/f.pageSize {
		return f, errors.New("分页偏移超出范围")
	}
	switch f.status {
	case "all", "active", "disabled", "expired", "exhausted":
	default:
		return f, errors.New("不支持的令牌状态")
	}
	switch f.expired {
	case "all", "yes", "no":
	default:
		return f, errors.New("不支持的到期筛选")
	}
	switch f.risk {
	case "all", "no_ip_limit", "never_expire", "unlimited":
	default:
		return f, errors.New("不支持的风险筛选")
	}
	if !validLogFilter(f.name, 128) || !validLogFilter(f.key, 128) || !validLogFilter(f.group, 128) {
		return f, errors.New("筛选内容格式无效")
	}
	if raw := c.Query("user_id"); raw != "" {
		f.userID, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || f.userID < 1 {
			return f, errors.New("用户 ID 须为正整数")
		}
	}
	return f, nil
}

func tokenBaseConditions(f tokenListFilters, driver string) ([]string, []any) {
	conditions := []string{"t.deleted_at IS NULL"}
	args := make([]any, 0, 10)
	if f.name != "" {
		conditions = append(conditions, "t.name LIKE ?")
		args = append(args, "%"+f.name+"%")
	}
	if f.key != "" {
		key := strings.TrimPrefix(f.key, "sk-")
		if key != "" {
			conditions = append(conditions, "t."+tokenIdentifier(driver, "key")+" = ?")
			args = append(args, key)
		}
	}
	if f.userID > 0 {
		conditions = append(conditions, "t.user_id = ?")
		args = append(args, f.userID)
	}
	if f.group != "" {
		conditions = append(conditions, "t."+tokenIdentifier(driver, "group")+" = ?")
		args = append(args, f.group)
	}
	now := time.Now().Unix()
	switch f.status {
	case "active":
		conditions = append(conditions, "t.status = 1 AND (t.expired_time <= 0 OR t.expired_time > ?)")
		args = append(args, now)
	case "disabled":
		conditions = append(conditions, "t.status = 2 AND (t.expired_time <= 0 OR t.expired_time > ?)")
		args = append(args, now)
	case "expired":
		conditions = append(conditions, "(t.status = 3 OR (t.expired_time > 0 AND t.expired_time <= ?))")
		args = append(args, now)
	case "exhausted":
		conditions = append(conditions, "t.status = 4")
	}
	if f.expired == "yes" {
		conditions = append(conditions, "(t.status = 3 OR (t.expired_time > 0 AND t.expired_time <= ?))")
		args = append(args, now)
	} else if f.expired == "no" {
		conditions = append(conditions, "(t.status != 3 AND (t.expired_time = 0 OR t.expired_time = -1 OR t.expired_time > ?))")
		args = append(args, now)
	}
	switch f.risk {
	case "no_ip_limit":
		conditions = append(conditions, "(t.allow_ips IS NULL OR t.allow_ips = '')")
	case "never_expire":
		conditions = append(conditions, "(t.expired_time = 0 OR t.expired_time = -1)")
	case "unlimited":
		conditions = append(conditions, "t.unlimited_quota = ?")
		args = append(args, true)
	}
	return conditions, args
}

func (s *dashboardService) tokensListHTTP(c *gin.Context) {
	if s.config.Mode == "demo" {
		s.demoTokensListHTTP(c)
		return
	}
	if !s.tokenManagementReady(c) {
		return
	}
	f, err := s.parseTokenFilters(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	conditions, args := tokenBaseConditions(f, s.management.driver)
	where := strings.Join(conditions, " AND ")
	ctx, cancel := context.WithTimeout(c.Request.Context(), queryTimeout)
	defer cancel()
	var total int64
	if err := s.management.queryRow(ctx, "SELECT COUNT(*) FROM tokens t WHERE "+where, args...).Scan(&total); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "token list query failed"})
		return
	}
	query := fmt.Sprintf(`SELECT t.id, t.%s, t.name, t.user_id, COALESCE(u.username, ''), t.status,
		COALESCE(t.remain_quota, 0), COALESCE(t.used_quota, 0),
		COALESCE(t.unlimited_quota, FALSE), COALESCE(t.model_limits, ''), COALESCE(t.allow_ips, ''),
		COALESCE(t.%s, 'default'), COALESCE(t.created_time, 0), COALESCE(t.accessed_time, 0), COALESCE(t.expired_time, 0)
		FROM tokens t LEFT JOIN users u ON t.user_id = u.id WHERE %s ORDER BY t.id DESC LIMIT ? OFFSET ?`,
		tokenIdentifier(s.management.driver, "key"), tokenIdentifier(s.management.driver, "group"), where)
	listArgs := append(append([]any{}, args...), f.pageSize, (f.page-1)*f.pageSize)
	rows, err := s.management.query(ctx, query, listArgs...)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "token list query failed"})
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0, f.pageSize)
	ids := make([]int64, 0, f.pageSize)
	for rows.Next() {
		var id, userID, status, usedQuota, remainQuota, created, accessed, expired int64
		var key, name, username, models, ips, group string
		var unlimited bool
		if err := rows.Scan(&id, &key, &name, &userID, &username, &status, &remainQuota, &usedQuota, &unlimited, &models, &ips, &group, &created, &accessed, &expired); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "token list query failed"})
			return
		}
		ids = append(ids, id)
		items = append(items, map[string]any{"id": id, "key": "sk-" + maskTokenKey(key), "name": name, "user_id": userID,
			"username": username, "status": status, "quota": remainQuota + usedQuota, "used_quota": usedQuota,
			"remain_quota": remainQuota, "unlimited_quota": unlimited, "models": models, "subnet": ips,
			"group": group, "created_time": created, "accessed_time": accessed, "expired_time": expired})
	}
	if err := rows.Err(); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "token list query failed"})
		return
	}
	if len(ids) > 0 && s.logs.db != nil {
		ipCounts, err := s.readTokenIPStats(ctx, ids, 24)
		if err == nil {
			for _, item := range items {
				item["ip_count"] = ipCounts[item["id"].(int64)]
			}
		}
		lastUsed, err := s.readTokenLastUsed(ctx, ids)
		if err == nil {
			for _, item := range items {
				if lastUsed[item["id"].(int64)] > 0 {
					item["accessed_time"] = lastUsed[item["id"].(int64)]
				}
			}
		}
	}
	pages := (total + int64(f.pageSize) - 1) / int64(f.pageSize)
	if pages < 1 {
		pages = 1
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"items": items, "total": total, "page": f.page, "page_size": f.pageSize, "total_pages": pages}})
}

func maskTokenKey(key string) string {
	if len(key) <= 8 {
		return key + "****"
	}
	return key[:8] + "****"
}

func (s *dashboardService) tokenStatisticsHTTP(c *gin.Context) {
	if s.config.Mode == "demo" {
		s.demoTokenStatisticsHTTP(c)
		return
	}
	if !s.tokenManagementReady(c) {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), queryTimeout)
	defer cancel()
	var total, active, disabled, expired, exhausted int64
	now := time.Now().Unix()
	err := s.management.queryRow(ctx, `SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN status = 1 AND (expired_time <= 0 OR expired_time > ?) THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status = 2 AND (expired_time <= 0 OR expired_time > ?) THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status = 3 OR (expired_time > 0 AND expired_time <= ?) THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status = 4 AND (expired_time <= 0 OR expired_time > ?) THEN 1 ELSE 0 END),0)
		FROM tokens WHERE deleted_at IS NULL`, now, now, now, now).Scan(&total, &active, &disabled, &expired, &exhausted)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "token statistics query failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"total": total, "active": active, "disabled": disabled, "expired": expired, "exhausted": exhausted}})
}

func (s *dashboardService) tokenGroupsHTTP(c *gin.Context) {
	if s.config.Mode == "demo" {
		s.demoTokenGroupsHTTP(c)
		return
	}
	if !s.tokenManagementReady(c) {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), queryTimeout)
	defer cancel()
	group := tokenIdentifier(s.management.driver, "group")
	rows, err := s.management.query(ctx, fmt.Sprintf(`SELECT COALESCE(NULLIF(%s, ''), 'default'), COUNT(*),
		COALESCE(SUM(CASE WHEN status = 1 THEN 1 ELSE 0 END),0) FROM tokens WHERE deleted_at IS NULL GROUP BY COALESCE(NULLIF(%s, ''), 'default') ORDER BY COUNT(*) DESC`, group, group))
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "token groups query failed"})
		return
	}
	defer rows.Close()
	groups := make([]map[string]any, 0)
	for rows.Next() {
		var name string
		var count, active int64
		if err := rows.Scan(&name, &count, &active); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "token groups query failed"})
			return
		}
		groups = append(groups, map[string]any{"group_name": name, "token_count": count, "active_count": active})
	}
	if err := rows.Err(); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "token groups query failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": groups})
}

func normalizeTokenKey(raw string) string { return strings.TrimPrefix(strings.TrimSpace(raw), "sk-") }

func (s *dashboardService) tokenLookupHTTP(c *gin.Context) {
	if s.config.Mode == "demo" {
		s.demoTokenLookupHTTP(c)
		return
	}
	if !s.tokenManagementReady(c) {
		return
	}
	var req struct {
		Keys []string `json:"keys"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 256*1024))
	if err := decoder.Decode(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求内容无效"})
		return
	}
	seen := make(map[string]bool)
	keys := make([]string, 0, len(req.Keys))
	for _, raw := range req.Keys {
		key := normalizeTokenKey(raw)
		if key == "" || seen[key] {
			continue
		}
		if len(key) > 128 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "令牌 key 长度无效"})
			return
		}
		seen[key] = true
		keys = append(keys, key)
	}
	if len(keys) == 0 || len(keys) > maxTokenBatch {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("每次需查询 1 至 %d 个不同的 key", maxTokenBatch)})
		return
	}
	marks := strings.TrimRight(strings.Repeat("?,", len(keys)), ",")
	query := fmt.Sprintf(`SELECT t.id, t.%s, t.name, t.user_id, COALESCE(u.username,''), t.status,
		COALESCE(t.expired_time,0), COALESCE(t.%s,'default'), COALESCE(t.used_quota,0)
		FROM tokens t LEFT JOIN users u ON t.user_id=u.id WHERE t.deleted_at IS NULL AND t.%s IN (%s)`,
		tokenIdentifier(s.management.driver, "key"), tokenIdentifier(s.management.driver, "group"), tokenIdentifier(s.management.driver, "key"), marks)
	ctx, cancel := context.WithTimeout(c.Request.Context(), queryTimeout)
	defer cancel()
	args := make([]any, len(keys))
	for i := range keys {
		args[i] = keys[i]
	}
	rows, err := s.management.query(ctx, query, args...)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "token lookup query failed"})
		return
	}
	defer rows.Close()
	found := make(map[string]map[string]any, len(keys))
	for rows.Next() {
		var id, userID, status, expired, used int64
		var key, name, username, group string
		if err := rows.Scan(&id, &key, &name, &userID, &username, &status, &expired, &group, &used); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "token lookup query failed"})
			return
		}
		found[key] = map[string]any{"found": true, "id": id, "name": name, "user_id": userID, "username": username, "status": status, "expired_time": expired, "group": group, "used_quota": used}
	}
	if err := rows.Err(); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "token lookup query failed"})
		return
	}
	items := make([]map[string]any, 0, len(keys))
	foundCount := 0
	for _, key := range keys {
		item := map[string]any{"key_masked": "sk-" + maskTokenKey(key), "found": false}
		if match, ok := found[key]; ok {
			for k, v := range match {
				item[k] = v
			}
			foundCount++
		}
		items = append(items, item)
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"items": items, "total": len(items), "found": foundCount, "not_found": len(items) - foundCount}})
}

func (s *dashboardService) tokenBatchHTTP(enable bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if s.config.Mode == "demo" {
			s.demoTokenBatchHTTP(c, enable)
			return
		}
		if !s.tokenManagementReady(c) {
			return
		}
		var req struct {
			IDs []int64 `json:"ids"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 64*1024))
		if err := decoder.Decode(&req); err != nil || len(req.IDs) == 0 || len(req.IDs) > maxTokenBatch {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("每次需提交 1 至 %d 个令牌 ID", maxTokenBatch)})
			return
		}
		seen := map[int64]bool{}
		ids := make([]int64, 0, len(req.IDs))
		for _, id := range req.IDs {
			if id <= 0 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "令牌 ID 须为正整数"})
				return
			}
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
		marks := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
		target, expectedStatus, action := tokenBatchStatus(enable)
		query := fmt.Sprintf("UPDATE tokens SET status = %d WHERE deleted_at IS NULL AND status = %d AND id IN (%s)", target, expectedStatus, marks)
		args := make([]any, len(ids))
		for i := range ids {
			args[i] = ids[i]
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), queryTimeout)
		defer cancel()
		result, err := s.management.db.ExecContext(ctx, bindQuery(s.management.driver, query), args...)
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "token batch update failed"})
			return
		}
		affected, _ := result.RowsAffected()
		c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"requested": len(ids), action: affected, "cache_notice": "若 NewAPI 启用 Redis，令牌状态可能在其缓存过期前延迟生效。"}})
	}
}

func tokenBatchStatus(enable bool) (target, expected int, action string) {
	if enable {
		return 1, 2, "enabled"
	}
	return 2, 1, "disabled"
}

func (s *dashboardService) readTokenIPStats(ctx context.Context, ids []int64, hours int) (map[int64]int64, error) {
	if len(ids) == 0 {
		return map[int64]int64{}, nil
	}
	if hours < 1 || hours > 24*30 {
		hours = 24
	}
	end := userClock(s).Unix() + 1
	counts, err := s.tokenNetworkCounts(ctx, end-int64(hours)*3600, end, ids)
	if err != nil {
		return nil, err
	}
	out := map[int64]int64{}
	for _, id := range ids {
		if stats := counts[id]; stats != nil {
			out[id] = int64(len(stats.IPs))
		}
	}
	return out, nil
}

func (s *dashboardService) readTokenLastUsed(ctx context.Context, ids []int64) (map[int64]int64, error) {
	if s.logs.db == nil || len(ids) == 0 {
		return map[int64]int64{}, nil
	}
	marks := make([]string, len(ids))
	args := make([]any, 0, len(ids)+1)
	args = append(args, time.Now().Unix()-90*86400)
	for i, id := range ids {
		marks[i] = "?"
		args = append(args, id)
	}
	rows, err := s.logs.query(ctx, fmt.Sprintf(`SELECT token_id, MAX(created_at) FROM logs WHERE created_at >= ? AND type IN (2,5) AND token_id IN (%s) GROUP BY token_id`, strings.Join(marks, ",")), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]int64, len(ids))
	for rows.Next() {
		var id, at int64
		if err := rows.Scan(&id, &at); err != nil {
			return nil, err
		}
		out[id] = at
	}
	return out, rows.Err()
}

func parseTokenIDs(raw string) ([]int64, error) {
	if strings.TrimSpace(raw) == "" {
		return []int64{}, nil
	}
	parts := strings.Split(raw, ",")
	if len(parts) > 200 {
		return nil, errors.New("最多统计 200 个令牌")
	}
	ids := make([]int64, 0, len(parts))
	seen := map[int64]bool{}
	for _, part := range parts {
		id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err != nil || id < 1 {
			return nil, errors.New("令牌 ID 列表格式无效")
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (s *dashboardService) tokenIPStatsHTTP(c *gin.Context) {
	if s.config.Mode == "demo" {
		s.demoTokenIPStatsHTTP(c)
		return
	}
	if !s.tokenManagementReady(c) {
		return
	}
	ids, err := parseTokenIDs(c.Query("ids"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(ids) == 0 {
		c.JSON(http.StatusOK, gin.H{"success": true, "data": []any{}})
		return
	}
	if s.logs.db == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "log database is not configured"})
		return
	}
	hours := 24
	if raw := c.Query("hours"); raw != "" {
		hours, err = strconv.Atoi(raw)
		if err != nil || hours < 1 || hours > 24*30 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "hours 须为 1 至 720"})
			return
		}
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), queryTimeout)
	defer cancel()
	counts, err := s.readTokenIPStats(ctx, ids, hours)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "token IP statistics query failed"})
		return
	}
	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		if count := counts[id]; count > 0 {
			out = append(out, map[string]any{"token_id": id, "ip_count": count})
		}
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": out})
}

func (s *dashboardService) tokenLeaksHTTP(c *gin.Context) {
	if s.config.Mode == "demo" {
		s.demoTokenLeaksHTTP(c)
		return
	}
	if !s.tokenManagementReady(c) {
		return
	}
	if s.logs.db == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "log database is not configured"})
		return
	}
	parse := func(name string, fallback, min, max int) (int, error) {
		raw := c.Query(name)
		if raw == "" {
			return fallback, nil
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n < min || n > max {
			return 0, fmt.Errorf("%s 须为 %d 至 %d", name, min, max)
		}
		return n, nil
	}
	hours, err := parse("hours", 24, 1, 720)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	minIPs, err := parse("min_ips", 5, 2, 10000)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	limit, err := parse("limit", 100, 1, 200)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), queryTimeout)
	defer cancel()
	end := userClock(s).Unix() + 1
	start := end - int64(hours)*3600
	networkCounts, err := s.tokenNetworkCounts(ctx, start, end, nil)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "token leak IP statistics are unavailable", "network_statistics": s.tokenNetworkMetadata(start, end, err)})
		return
	}
	type leak struct {
		id, ipsCount, requests int64
		ips                    string
	}
	leaks := make([]leak, 0, limit)
	for id, stats := range networkCounts {
		if len(stats.IPs) < minIPs {
			continue
		}
		ips := make([]string, 0, len(stats.IPs))
		for ip := range stats.IPs {
			ips = append(ips, ip)
		}
		sort.Strings(ips)
		leaks = append(leaks, leak{id: id, ipsCount: int64(len(ips)), requests: stats.Requests, ips: strings.Join(ips, ",")})
	}
	sort.Slice(leaks, func(i, j int) bool {
		if leaks[i].ipsCount != leaks[j].ipsCount {
			return leaks[i].ipsCount > leaks[j].ipsCount
		}
		return leaks[i].id < leaks[j].id
	})
	leaks = leaks[:min(len(leaks), limit)]
	ids := make([]int64, 0, len(leaks))
	for _, item := range leaks {
		ids = append(ids, item.id)
	}
	if len(ids) == 0 {
		c.JSON(http.StatusOK, gin.H{"success": true, "data": []any{}, "network_statistics": s.tokenNetworkMetadata(start, end, nil)})
		return
	}
	marks := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	query := fmt.Sprintf(`SELECT t.id,t.%s,t.name,t.user_id,COALESCE(u.username,''),t.status,COALESCE(t.remain_quota,0),COALESCE(t.unlimited_quota,FALSE),COALESCE(t.used_quota,0),COALESCE(t.allow_ips,''),COALESCE(t.%s,'default'),COALESCE(t.expired_time,0) FROM tokens t LEFT JOIN users u ON t.user_id=u.id WHERE t.deleted_at IS NULL AND t.id IN (%s)`, tokenIdentifier(s.management.driver, "key"), tokenIdentifier(s.management.driver, "group"), marks)
	args := make([]any, len(ids))
	for i := range ids {
		args[i] = ids[i]
	}
	rows, err := s.management.query(ctx, query, args...)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "token leak query failed"})
		return
	}
	defer rows.Close()
	byID := map[int64]map[string]any{}
	for rows.Next() {
		var id, uid, status, remain, used, expired int64
		var key, name, user, ips, group string
		var unlimited bool
		if err := rows.Scan(&id, &key, &name, &uid, &user, &status, &remain, &unlimited, &used, &ips, &group, &expired); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "token leak query failed"})
			return
		}
		byID[id] = map[string]any{"id": id, "key": "sk-" + maskTokenKey(key), "name": name, "user_id": uid, "username": user, "status": status, "remain_quota": remain, "unlimited_quota": unlimited, "used_quota": used, "subnet": ips, "group": group, "expired_time": expired}
	}
	if err := rows.Err(); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "token leak query failed"})
		return
	}
	out := make([]map[string]any, 0, len(leaks))
	for _, l := range leaks {
		if token, ok := byID[l.id]; ok {
			token["ip_count"] = l.ipsCount
			token["request_count"] = l.requests
			token["ips"] = l.ips
			out = append(out, token)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i]["ip_count"].(int64) > out[j]["ip_count"].(int64) })
	c.JSON(http.StatusOK, gin.H{"success": true, "data": out, "network_statistics": s.tokenNetworkMetadata(start, end, nil)})
}

func (s *dashboardService) tokenAnalysisHTTP(c *gin.Context) {
	if s.config.Mode == "demo" {
		s.demoTokenAnalysisHTTP(c)
		return
	}
	if !s.tokenManagementReady(c) {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id < 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "令牌 ID 无效"})
		return
	}
	start, end, seconds, err := parseTokenAnalysisRange(c, userClock(s).Unix())
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), queryTimeout)
	defer cancel()
	keyCol, groupCol := tokenIdentifier(s.management.driver, "key"), tokenIdentifier(s.management.driver, "group")
	query := fmt.Sprintf(`SELECT t.id,t.%s,t.name,t.user_id,COALESCE(u.username,''),COALESCE(u.status,1),t.status,COALESCE(t.remain_quota,0),COALESCE(t.used_quota,0),COALESCE(t.unlimited_quota,FALSE),COALESCE(t.allow_ips,''),COALESCE(t.model_limits,''),COALESCE(t.%s,'default'),COALESCE(t.created_time,0),COALESCE(t.expired_time,0) FROM tokens t LEFT JOIN users u ON t.user_id=u.id WHERE t.id=? AND t.deleted_at IS NULL`, keyCol, groupCol)
	var uid, status, userStatus, remain, used, created, expired int64
	var key, name, username, allowIPs, modelLimits, group string
	var unlimited bool
	if err := s.management.queryRow(ctx, query, id).Scan(&id, &key, &name, &uid, &username, &userStatus, &status, &remain, &used, &unlimited, &allowIPs, &modelLimits, &group, &created, &expired); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "令牌不存在"})
		} else {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "token analysis query failed"})
		}
		return
	}
	summary := map[string]any{"total_requests": int64(0), "success_requests": int64(0), "failure_requests": int64(0), "quota_used": int64(0), "prompt_tokens": int64(0), "completion_tokens": int64(0), "avg_use_time": float64(0), "unique_ips": int64(0), "unique_models": int64(0)}
	if s.logs.db == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "log database is not configured"})
		return
	}
	var total, success, failure, quota, prompt, completion, models int64
	var avg float64
	statsQuery := `SELECT COUNT(*),COALESCE(SUM(CASE WHEN type=2 THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN type=5 THEN 1 ELSE 0 END),0),COALESCE(SUM(quota),0),COALESCE(SUM(prompt_tokens),0),COALESCE(SUM(completion_tokens),0),COALESCE(AVG(CASE WHEN type=2 THEN use_time END),0),COUNT(DISTINCT model_name) FROM logs WHERE token_id=? AND created_at>=? AND created_at<=? AND type IN (2,5)`
	if err := s.logs.queryRow(ctx, statsQuery, id, start, end).Scan(&total, &success, &failure, &quota, &prompt, &completion, &avg, &models); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "token analysis query failed"})
		return
	}
	summary = map[string]any{"total_requests": total, "success_requests": success, "failure_requests": failure, "quota_used": quota, "prompt_tokens": prompt, "completion_tokens": completion, "avg_use_time": avg, "unique_ips": nil, "unique_models": models, "failure_rate": float64(0)}
	if total > 0 {
		summary["failure_rate"] = float64(failure) / float64(total)
	}
	topModels := make([]map[string]any, 0)
	rows, err := s.logs.query(ctx, `SELECT COALESCE(model_name,'unknown'),COUNT(*),COALESCE(SUM(quota),0),COALESCE(SUM(CASE WHEN type=5 THEN 1 ELSE 0 END),0) FROM logs WHERE token_id=? AND created_at>=? AND created_at<=? AND type IN (2,5) GROUP BY COALESCE(model_name,'unknown') ORDER BY COUNT(*) DESC LIMIT 10`, id, start, end)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "token analysis query failed"})
		return
	}
	for rows.Next() {
		var model string
		var requests, amount, errors int64
		if err := rows.Scan(&model, &requests, &amount, &errors); err != nil {
			rows.Close()
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "token analysis query failed"})
			return
		}
		topModels = append(topModels, map[string]any{"model_name": model, "requests": requests, "quota_used": amount, "failure_requests": errors})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "token analysis query failed"})
		return
	}
	rows.Close()
	topIPs := make([]tokenNetworkExit, 0)
	networkCounts, networkErr := s.tokenNetworkCounts(ctx, start, end+1, []int64{id})
	ips := int64(0)
	if networkErr == nil {
		if counts := networkCounts[id]; counts != nil {
			ips = int64(len(counts.IPs))
			topIPs = sortedTokenNetworkExits(counts)
			topIPs = topIPs[:min(len(topIPs), 20)]
		}
		summary["unique_ips"] = ips
	}
	recent := make([]map[string]any, 0)
	rows, err = s.logs.query(ctx, `SELECT id,created_at,type,COALESCE(model_name,''),COALESCE(quota,0),COALESCE(prompt_tokens,0),COALESCE(completion_tokens,0),COALESCE(use_time,0),COALESCE(ip,'') FROM logs WHERE token_id=? AND created_at>=? AND created_at<=? AND type IN (2,5) ORDER BY id DESC LIMIT 30`, id, start, end)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "token analysis query failed"})
		return
	}
	for rows.Next() {
		var logID, at, typ, amount, promptTokens, completionTokens int64
		var duration float64
		var model, ip string
		if err := rows.Scan(&logID, &at, &typ, &model, &amount, &promptTokens, &completionTokens, &duration, &ip); err != nil {
			rows.Close()
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "token analysis query failed"})
			return
		}
		recent = append(recent, map[string]any{"id": logID, "created_at": at, "type": typ, "model_name": model, "quota": amount, "prompt_tokens": promptTokens, "completion_tokens": completionTokens, "use_time": duration, "ip": ip})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "token analysis query failed"})
		return
	}
	rows.Close()
	riskFlags := make([]string, 0)
	if ips >= 5 && allowIPs == "" {
		riskFlags = append(riskFlags, "LEAK_SUSPECT")
	}
	if total > 10 && float64(failure)/float64(total) > 0.5 {
		riskFlags = append(riskFlags, "HIGH_FAILURE_RATE")
	}
	rpm := float64(total) / (float64(seconds) / 60)
	if rpm > 5 {
		riskFlags = append(riskFlags, "HIGH_RPM")
	}
	token := map[string]any{"id": id, "key": "sk-" + maskTokenKey(key), "name": name, "user_id": uid, "username": username, "user_status": userStatus, "status": status, "remain_quota": remain, "used_quota": used, "unlimited_quota": unlimited, "allow_ips": allowIPs, "model_limits": modelLimits, "group": group, "created_time": created, "expired_time": expired}
	hourly := make([]map[string]any, 0)
	bucketExpr := fmt.Sprintf("FLOOR((created_at - %d) / 3600)", start)
	hourRows, err := s.logs.query(ctx, fmt.Sprintf(`SELECT %s,COUNT(*),COALESCE(SUM(quota),0) FROM logs WHERE token_id=? AND created_at>=? AND created_at<=? AND type IN (2,5) GROUP BY %s ORDER BY %s`, bucketExpr, bucketExpr, bucketExpr), id, start, end)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "token analysis query failed"})
		return
	}
	for hourRows.Next() {
		var slot, requests, amount int64
		if err := hourRows.Scan(&slot, &requests, &amount); err != nil {
			hourRows.Close()
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "token analysis query failed"})
			return
		}
		hourly = append(hourly, map[string]any{"slot_idx": slot, "requests": requests, "quota_used": amount})
	}
	if err := hourRows.Err(); err != nil {
		hourRows.Close()
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "token analysis query failed"})
		return
	}
	hourRows.Close()
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"range": gin.H{"start_time": start, "end_time": end, "window_seconds": seconds}, "token": token, "summary": summary, "risk": gin.H{"requests_per_minute": rpm, "risk_flags": riskFlags}, "top_models": topModels, "top_ips": topIPs, "hourly": hourly, "recent_logs": recent, "network_statistics": s.tokenNetworkMetadata(start, end+1, networkErr)}})
}
