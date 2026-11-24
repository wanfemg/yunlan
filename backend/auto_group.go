package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

var autoGroupSources = []string{"github", "wechat", "telegram", "discord", "oidc", "linux_do", "password"}

type autoGroupUser struct {
	ID             int64  `json:"id"`
	Username       string `json:"username"`
	DisplayName    string `json:"display_name"`
	Email          string `json:"email"`
	Group          string `json:"group"`
	Source         string `json:"source"`
	Status         int    `json:"status"`
	CreatedTime    int64  `json:"created_time"`
	TargetGroup    string `json:"target_group,omitempty"`
	deletedAtKnown bool
}

func autoGroupDemoUsers() []autoGroupUser {
	return demoAutoGroupUsers(demoManagedUserFixtures(time.Now().Unix()))
}

func demoAutoGroupSource(id int64) string {
	original := map[int64]string{101: "github", 102: "linux_do", 103: "password", 104: "wechat", 105: "github", 106: "password", 107: "telegram", 108: "discord"}
	if source := original[id]; source != "" {
		return source
	}
	return autoGroupSources[(id-101)%int64(len(autoGroupSources))]
}

func demoAutoGroupUsers(users []managedUserRecord) []autoGroupUser {
	result := make([]autoGroupUser, 0, len(users))
	for _, user := range users {
		if user.Role >= 10 {
			continue
		}
		result = append(result, autoGroupUser{
			ID: user.ID, Username: user.Username, DisplayName: user.DisplayName, Email: user.Email,
			Group: user.Group, Source: demoAutoGroupSource(user.ID), Status: int(user.Status), CreatedTime: user.CreatedAt,
		})
	}
	return result
}

func (s *dashboardService) setDemoManagedUserGroup(id int64, group string) {
	state := s.userState()
	state.mu.Lock()
	defer state.mu.Unlock()
	if user, exists := state.demo[id]; exists {
		user.Group = group
		state.demo[id] = user
	}
}

func validateAutoGroupConfig(config autoGroupConfig) error {
	if config.Mode != "simple" && config.Mode != "by_source" {
		return errors.New("无效的分组模式")
	}
	if config.ScanInterval < 1 || config.ScanInterval > 1440 {
		return errors.New("扫描间隔必须在 1-1440 分钟之间")
	}
	if len(config.TargetGroup) > 64 {
		return errors.New("目标分组长度不能超过 64")
	}
	if len(config.Whitelist) > 10000 {
		return errors.New("白名单最多包含 10000 个用户")
	}
	for _, group := range config.SourceRules {
		if len(group) > 64 {
			return errors.New("来源分组长度不能超过 64")
		}
	}
	return nil
}

func registerAutoGroupRoutes(router *gin.Engine, service *dashboardService, token string) {
	group := router.Group("/api/auto-group", requireAdminToken(token))
	group.GET("/config", service.autoGroupConfigHTTP)
	group.POST("/config", service.saveAutoGroupConfigHTTP)
	group.GET("/stats", service.autoGroupStatsHTTP)
	group.GET("/groups", service.autoGroupGroupsHTTP)
	group.GET("/preview", service.autoGroupPreviewHTTP)
	group.GET("/users", service.autoGroupUsersHTTP)
	group.POST("/scan", service.autoGroupScanHTTP)
	group.POST("/batch-move", service.autoGroupBatchMoveHTTP)
	group.GET("/logs", service.autoGroupLogsHTTP)
	group.POST("/revert", service.autoGroupRevertHTTP)
}

func (s *dashboardService) autoGroupConfigHTTP(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"success": true, "data": s.autoGroup.snapshot().Config})
}

func (s *dashboardService) saveAutoGroupConfigHTTP(c *gin.Context) {
	var updates map[string]json.RawMessage
	if c.ShouldBindJSON(&updates) != nil || len(updates) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "配置内容无效"})
		return
	}
	s.autoGroup.mu.Lock()
	defer s.autoGroup.mu.Unlock()
	next := cloneAutoGroupJournal(s.autoGroup.state)
	for key, value := range updates {
		var err error
		switch key {
		case "enabled":
			err = json.Unmarshal(value, &next.Config.Enabled)
		case "mode":
			err = json.Unmarshal(value, &next.Config.Mode)
		case "target_group":
			err = json.Unmarshal(value, &next.Config.TargetGroup)
		case "source_rules":
			err = json.Unmarshal(value, &next.Config.SourceRules)
		case "scan_interval_minutes":
			err = json.Unmarshal(value, &next.Config.ScanInterval)
		case "auto_scan_enabled":
			err = json.Unmarshal(value, &next.Config.AutoScan)
		case "whitelist_ids":
			err = json.Unmarshal(value, &next.Config.Whitelist)
		default:
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "未知的配置项: " + key})
			return
		}
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "配置项格式错误: " + key})
			return
		}
	}
	if err := validateAutoGroupConfig(next.Config); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	if err := s.autoGroup.save(next); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": next.Config})
}

func (s *dashboardService) autoGroupStatsHTTP(c *gin.Context) {
	state := s.autoGroup.snapshot()
	users, err := s.autoGroupUsers(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "自动分组数据暂不可用"})
		return
	}
	pending := 0
	for _, user := range users {
		if user.Status == 1 && normalizedUserGroup(user.Group) == "default" && !containsAutoGroupUser(state.Config.Whitelist, user.ID) {
			pending++
		}
	}
	// The reference counts assignment records, not manual group moves or
	// restoration records, in the cumulative assignment card.
	assigned := int64(0)
	for _, entry := range state.Logs {
		if entry.Action == "assign" {
			assigned++
		}
	}
	nextScan := int64(0)
	if state.Config.AutoScan {
		nextScan = state.Config.LastScan + int64(state.Config.ScanInterval)*60
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"pending_count": pending, "total_assigned": assigned, "last_scan_time": state.Config.LastScan, "next_scan_time": nextScan, "enabled": state.Config.Enabled, "auto_scan_enabled": state.Config.AutoScan, "write_enabled": s.management.db != nil || s.config.Mode == "demo", "demo": s.config.Mode == "demo"}})
}

func (s *dashboardService) autoGroupGroupsHTTP(c *gin.Context) {
	users, err := s.autoGroupUsers(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "分组数据暂不可用"})
		return
	}
	counts := map[string]int64{"default": 0}
	for _, user := range users {
		counts[normalizedUserGroup(user.Group)]++
	}
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if counts[names[i]] != counts[names[j]] {
			return counts[names[i]] > counts[names[j]]
		}
		return names[i] < names[j]
	})
	items := make([]gin.H, 0, len(names))
	for _, name := range names {
		items = append(items, gin.H{"group_name": name, "user_count": counts[name]})
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"items": items, "total": len(items)}})
}

func (s *dashboardService) autoGroupPreviewHTTP(c *gin.Context) { s.autoGroupListHTTP(c, true) }
func (s *dashboardService) autoGroupUsersHTTP(c *gin.Context)   { s.autoGroupListHTTP(c, false) }

func (s *dashboardService) autoGroupListHTTP(c *gin.Context, preview bool) {
	users, err := s.autoGroupUsers(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "用户分组数据暂不可用"})
		return
	}
	config := s.autoGroup.snapshot().Config
	groupFilter, sourceFilter, keyword := c.Query("group"), c.Query("source"), strings.ToLower(strings.TrimSpace(c.Query("keyword")))
	if sourceFilter != "" && !validAutoGroupSource(sourceFilter) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的注册来源"})
		return
	}
	items := make([]autoGroupUser, 0, len(users))
	for _, user := range users {
		if preview {
			if user.Status != 1 || normalizedUserGroup(user.Group) != "default" || containsAutoGroupUser(config.Whitelist, user.ID) {
				continue
			}
			user.TargetGroup = autoGroupTarget(config, user.Source)
		}
		if groupFilter != "" && normalizedUserGroup(user.Group) != groupFilter {
			continue
		}
		if sourceFilter != "" && user.Source != sourceFilter {
			continue
		}
		if keyword != "" && !strings.Contains(strings.ToLower(fmt.Sprintf("%d %s %s %s", user.ID, user.Username, user.DisplayName, user.Email)), keyword) {
			continue
		}
		items = append(items, user)
	}
	page := boundedAutoGroupInt(c.Query("page"), 1, 1, 1000000)
	size := boundedAutoGroupInt(c.DefaultQuery("page_size", "20"), 20, 1, 100)
	total := len(items)
	start := (page - 1) * size
	if start > total {
		start = total
	}
	end := start + size
	if end > total {
		end = total
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"items": items[start:end], "total": total, "page": page, "page_size": size, "total_pages": (total + size - 1) / size}})
}

func (s *dashboardService) autoGroupUsers(ctx context.Context) ([]autoGroupUser, error) {
	if s.config.Mode == "demo" {
		users, err := s.managedUsers(ctx, 0)
		if err != nil {
			return nil, err
		}
		items := demoAutoGroupUsers(users)
		sort.Slice(items, func(i, j int) bool { return items[i].ID > items[j].ID })
		return items, nil
	}
	db := s.main
	if s.management.db != nil {
		db = s.management
	}
	if db.db == nil {
		return nil, errors.New("用户分组数据库未配置")
	}
	queryCtx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	// NewAPI versions do not all have the same OAuth or timestamp columns.
	// Match the reference's available-column projection instead of requiring
	// every supported provider to exist in the connected users table.
	schema, err := db.query(queryCtx, "SELECT * FROM users WHERE 1 = 0")
	if err != nil {
		return nil, errors.New("用户分组字段信息不可用")
	}
	columns, err := schema.Columns()
	_ = schema.Close()
	if err != nil {
		return nil, errors.New("用户分组字段信息不可用")
	}
	has := make(map[string]bool, len(columns))
	for _, column := range columns {
		has[strings.ToLower(column)] = true
	}
	for _, required := range []string{"id", "username", "group", "status"} {
		if !has[required] {
			return nil, errors.New("当前用户表缺少自动分组所需字段")
		}
	}
	stringColumn := func(name string) string {
		if !has[name] {
			return "''"
		}
		castType := "TEXT"
		if db.driver == "mysql" {
			castType = "CHAR"
		}
		return "COALESCE(CAST(" + tokenIdentifier(db.driver, name) + " AS " + castType + "),'')"
	}
	created := "0"
	for _, column := range []string{"created_at", "created_time"} {
		if has[column] {
			created = "COALESCE(" + tokenIdentifier(db.driver, column) + ",0)"
			break
		}
	}
	group := tokenIdentifier(db.driver, "group")
	query := "SELECT id, username, " + stringColumn("display_name") + ", " + stringColumn("email") +
		", COALESCE(NULLIF(" + group + ",''),'default'), status, " + created
	for _, provider := range autoGroupSources[:6] {
		query += ", " + stringColumn(provider+"_id")
	}
	query += " FROM users WHERE 1 = 1"
	if has["deleted_at"] {
		query += " AND deleted_at IS NULL"
	}
	query += " ORDER BY id DESC"
	rows, err := db.query(queryCtx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := make([]autoGroupUser, 0)
	for rows.Next() {
		var u autoGroupUser
		var oauth [6]string
		if err := rows.Scan(&u.ID, &u.Username, &u.DisplayName, &u.Email, &u.Group, &u.Status, &u.CreatedTime, &oauth[0], &oauth[1], &oauth[2], &oauth[3], &oauth[4], &oauth[5]); err != nil {
			return nil, err
		}
		for i, source := range autoGroupSources[:6] {
			if oauth[i] != "" {
				u.Source = source
				break
			}
		}
		if u.Source == "" {
			u.Source = "password"
		}
		u.deletedAtKnown = has["deleted_at"]
		users = append(users, u)
	}
	return users, rows.Err()
}

func (s *dashboardService) autoGroupScanHTTP(c *gin.Context) {
	dryRun := c.DefaultQuery("dry_run", "true") != "false"
	s.autoGroup.mu.Lock()
	defer s.autoGroup.mu.Unlock()
	state := s.autoGroup.state
	if !state.Config.Enabled {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "自动分组功能未启用", "data": gin.H{"message": "自动分组功能未启用"}})
		return
	}
	if !dryRun && s.management.db == nil && s.config.Mode != "demo" {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "数据库写入连接不可用，当前仅开放预览", "data": gin.H{"message": "数据库写入连接不可用，当前仅开放预览"}})
		return
	}
	if state.Config.Mode == "simple" && strings.TrimSpace(state.Config.TargetGroup) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "未配置目标分组", "data": gin.H{"message": "未配置目标分组"}})
		return
	}
	if state.Config.Mode == "by_source" {
		hasRule := false
		for _, source := range autoGroupSources {
			if strings.TrimSpace(state.Config.SourceRules[source]) != "" {
				hasRule = true
				break
			}
		}
		if !hasRule {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "未配置任何来源分组规则", "data": gin.H{"message": "未配置任何来源分组规则"}})
			return
		}
	}
	startedAt := time.Now()
	users, err := s.autoGroupUsers(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "用户分组数据暂不可用", "data": gin.H{"message": "用户分组数据暂不可用"}})
		return
	}
	next := cloneAutoGroupJournal(state)
	affected := 0
	total := 0
	skipped := 0
	results := make([]gin.H, 0)
	now := s.now().Unix()
	quote := "\x60"
	if s.management.driver == "postgres" {
		quote = "\""
	}
	var tx *sql.Tx
	if !dryRun && s.config.Mode != "demo" {
		tx, err = s.management.db.BeginTx(c.Request.Context(), nil)
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "无法开启分组事务", "data": gin.H{"message": "无法开启分组事务"}})
			return
		}
		defer tx.Rollback()
	}
	for _, user := range users {
		if user.Status != 1 || normalizedUserGroup(user.Group) != "default" || containsAutoGroupUser(next.Config.Whitelist, user.ID) {
			continue
		}
		total++
		target := autoGroupTarget(next.Config, user.Source)
		if target == "" {
			skipped++
			results = append(results, gin.H{"user_id": user.ID, "username": user.Username, "source": user.Source, "action": "skipped", "message": "来源 " + user.Source + " 未配置目标分组"})
			continue
		}
		if dryRun {
			affected++
			results = append(results, gin.H{"user_id": user.ID, "username": user.Username, "source": user.Source, "target_group": target, "action": "would_assign", "message": "[试运行] 将分配到 " + target})
			continue
		}
		if s.config.Mode != "demo" {
			query := "UPDATE users SET " + quote + "group" + quote + " = ? WHERE id = ? AND status = 1"
			if user.deletedAtKnown {
				query += " AND deleted_at IS NULL"
			}
			query += " AND COALESCE(NULLIF(" + quote + "group" + quote + ",''),'default') = 'default'"
			result, err := tx.ExecContext(c.Request.Context(), bindQuery(s.management.driver, query), target, user.ID)
			if err != nil {
				c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "更新分组失败", "data": gin.H{"message": "更新分组失败"}})
				return
			}
			changed, err := result.RowsAffected()
			if err != nil {
				c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "用户分组更新结果未能确认，事务已取消"})
				return
			}
			if changed == 0 {
				skipped++
				results = append(results, gin.H{"user_id": user.ID, "username": user.Username, "source": user.Source, "action": "skipped", "message": "用户分组或状态已变更，未覆盖"})
				continue
			}
		} else {
			s.setDemoManagedUserGroup(user.ID, target)
			for i := range s.autoGroup.users {
				if s.autoGroup.users[i].ID == user.ID {
					s.autoGroup.users[i].Group = target
					break
				}
			}
		}
		addAutoGroupLog(&next, autoGroupLog{UserID: user.ID, Username: user.Username, OldGroup: "default", NewGroup: target, Action: "assign", Source: user.Source, Operator: "system", CreatedAt: now})
		affected++
		results = append(results, gin.H{"user_id": user.ID, "username": user.Username, "source": user.Source, "target_group": target, "action": "assigned", "message": "已分配到 " + target})
	}
	if !dryRun {
		if tx != nil {
			if err := tx.Commit(); err != nil {
				c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "提交分组事务失败", "data": gin.H{"message": "提交分组事务失败"}})
				return
			}
		}
	}
	// A dry run is still a completed scan in the reference UI. Persist its
	// timestamp without writing users or producing assignment records.
	next.Config.LastScan = now
	next.NextScan = 0
	if next.Config.AutoScan {
		next.NextScan = now + int64(next.Config.ScanInterval*60)
	}
	if err := s.autoGroup.save(next); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error(), "data": gin.H{"message": err.Error()}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"success": true, "dry_run": dryRun, "stats": gin.H{"total": total, "assigned": affected, "skipped": skipped, "errors": 0}, "elapsed_seconds": fmt.Sprintf("%.2f", time.Since(startedAt).Seconds()), "results": results, "message": "扫描完成"}})
}

func (s *dashboardService) autoGroupBatchMoveHTTP(c *gin.Context) {
	var req struct {
		UserIDs     []int64 `json:"user_ids"`
		TargetGroup string  `json:"target_group"`
	}
	if c.ShouldBindJSON(&req) != nil || len(req.UserIDs) == 0 || len(req.UserIDs) > 100 || req.TargetGroup == "" || len(req.TargetGroup) > 64 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "用户列表或目标分组无效"})
		return
	}
	if s.management.db == nil && s.config.Mode != "demo" {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "数据库写入连接不可用，当前仅开放预览"})
		return
	}
	s.autoGroup.mu.Lock()
	defer s.autoGroup.mu.Unlock()
	users, err := s.autoGroupUsers(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "用户数据暂不可用"})
		return
	}
	next := cloneAutoGroupJournal(s.autoGroup.state)
	moved := 0
	now := s.now().Unix()
	seenUsers := make(map[int64]bool, len(req.UserIDs))
	quote := "\x60"
	if s.management.driver == "postgres" {
		quote = "\""
	}
	var tx *sql.Tx
	if s.config.Mode != "demo" {
		tx, err = s.management.db.BeginTx(c.Request.Context(), nil)
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "无法开启分组事务"})
			return
		}
		defer tx.Rollback()
	}
	for _, id := range req.UserIDs {
		if seenUsers[id] {
			continue
		}
		seenUsers[id] = true
		for _, user := range users {
			if user.ID != id {
				continue
			}
			old := normalizedUserGroup(user.Group)
			if old == req.TargetGroup {
				break
			}
			if s.config.Mode != "demo" {
				query := "UPDATE users SET " + quote + "group" + quote + " = ? WHERE id = ?"
				if user.deletedAtKnown {
					query += " AND deleted_at IS NULL"
				}
				query += " AND COALESCE(NULLIF(" + quote + "group" + quote + ",''),'default') = ?"
				result, e := tx.ExecContext(c.Request.Context(), bindQuery(s.management.driver, query), req.TargetGroup, user.ID, old)
				if e != nil {
					c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "更新分组失败"})
					return
				}
				n, e := result.RowsAffected()
				if e != nil || n == 0 {
					break
				}
			} else {
				s.setDemoManagedUserGroup(id, req.TargetGroup)
				for i := range s.autoGroup.users {
					if s.autoGroup.users[i].ID == id {
						s.autoGroup.users[i].Group = req.TargetGroup
					}
				}
			}
			addAutoGroupLog(&next, autoGroupLog{UserID: id, Username: user.Username, OldGroup: old, NewGroup: req.TargetGroup, Action: "move", Source: user.Source, Operator: "admin", CreatedAt: now})
			moved++
			break
		}
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "提交分组事务失败"})
			return
		}
	}
	if err := s.autoGroup.save(next); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"moved": moved}})
}

func (s *dashboardService) autoGroupLogsHTTP(c *gin.Context) {
	logs := append([]autoGroupLog(nil), s.autoGroup.snapshot().Logs...)
	sort.Slice(logs, func(i, j int) bool { return logs[i].ID > logs[j].ID })
	action := c.Query("action")
	userID, _ := strconv.ParseInt(c.Query("user_id"), 10, 64)
	filtered := make([]autoGroupLog, 0, len(logs))
	for _, entry := range logs {
		if action != "" && entry.Action != action || userID != 0 && entry.UserID != userID {
			continue
		}
		filtered = append(filtered, entry)
	}
	paginateAutoGroup(c, filtered)
}

func (s *dashboardService) autoGroupRevertHTTP(c *gin.Context) {
	var req struct {
		LogID int64 `json:"log_id"`
	}
	if c.ShouldBindJSON(&req) != nil || req.LogID < 1 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "日志记录编号无效"})
		return
	}
	if s.management.db == nil && s.config.Mode != "demo" {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "数据库写入连接不可用，当前仅开放预览"})
		return
	}
	s.autoGroup.mu.Lock()
	defer s.autoGroup.mu.Unlock()
	next := cloneAutoGroupJournal(s.autoGroup.state)
	index := -1
	for i := range next.Logs {
		if next.Logs[i].ID == req.LogID && next.Logs[i].Action != "revert" {
			index = i
			break
		}
	}
	if index < 0 || next.Logs[index].Reverted {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "操作记录不存在或已经回滚"})
		return
	}
	entry := next.Logs[index]
	user, found := s.autoGroupUserByID(c.Request.Context(), entry.UserID)
	if !found || normalizedUserGroup(user.Group) != entry.NewGroup {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "用户当前分组与记录不符，未执行回滚"})
		return
	}
	if s.config.Mode != "demo" {
		quote := "\x60"
		if s.management.driver == "postgres" {
			quote = "\""
		}
		query := "UPDATE users SET " + quote + "group" + quote + " = ? WHERE id = ?"
		if user.deletedAtKnown {
			query += " AND deleted_at IS NULL"
		}
		query += " AND COALESCE(NULLIF(" + quote + "group" + quote + ",''),'default') = ?"
		result, err := s.management.db.ExecContext(c.Request.Context(), bindQuery(s.management.driver, query), entry.OldGroup, entry.UserID, entry.NewGroup)
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "恢复用户分组失败"})
			return
		}
		rows, err := result.RowsAffected()
		if err != nil || rows == 0 {
			c.JSON(http.StatusConflict, gin.H{"success": false, "message": "用户分组已发生变化，未执行回滚"})
			return
		}
	} else {
		s.setDemoManagedUserGroup(entry.UserID, entry.OldGroup)
		for i := range s.autoGroup.users {
			if s.autoGroup.users[i].ID == entry.UserID {
				s.autoGroup.users[i].Group = entry.OldGroup
			}
		}
	}
	next.Logs[index].Reverted = true
	addAutoGroupLog(&next, autoGroupLog{UserID: entry.UserID, Username: entry.Username, OldGroup: entry.NewGroup, NewGroup: entry.OldGroup, Action: "revert", Source: entry.Source, Operator: "admin", CreatedAt: s.now().Unix()})
	if err := s.autoGroup.save(next); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"reverted": true}})
}

func (s *dashboardService) autoGroupUserByID(ctx context.Context, id int64) (autoGroupUser, bool) {
	users, err := s.autoGroupUsers(ctx)
	if err != nil {
		return autoGroupUser{}, false
	}
	for _, u := range users {
		if u.ID == id {
			return u, true
		}
	}
	return autoGroupUser{}, false
}
func (s *autoGroupState) snapshot() autoGroupJournal {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneAutoGroupJournal(s.state)
}
func addAutoGroupLog(state *autoGroupJournal, entry autoGroupLog) {
	entry.ID = state.NextID
	state.NextID++
	state.Logs = append(state.Logs, entry)
	if entry.Action == "assign" {
		state.TotalAssigned++
	}
	if len(state.Logs) > 2000 {
		state.Logs = append([]autoGroupLog(nil), state.Logs[len(state.Logs)-2000:]...)
	}
}
func autoGroupTarget(c autoGroupConfig, source string) string {
	if c.Mode == "simple" {
		return strings.TrimSpace(c.TargetGroup)
	}
	return strings.TrimSpace(c.SourceRules[source])
}
func containsAutoGroupUser(ids []int64, id int64) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}
func validAutoGroupSource(source string) bool {
	for _, candidate := range autoGroupSources {
		if source == candidate {
			return true
		}
	}
	return false
}
func normalizedUserGroup(group string) string {
	if group == "" {
		return "default"
	}
	return group
}
func boundedAutoGroupInt(raw string, fallback, min, max int) int {
	value, err := strconv.Atoi(raw)
	if err != nil || value < min || value > max {
		return fallback
	}
	return value
}
func paginateAutoGroup(c *gin.Context, items []autoGroupLog) {
	total := len(items)
	page := boundedAutoGroupInt(c.Query("page"), 1, 1, 1000000)
	size := boundedAutoGroupInt(c.DefaultQuery("page_size", "20"), 20, 1, 100)
	start := (page - 1) * size
	if start > total {
		start = total
	}
	end := start + size
	if end > total {
		end = total
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"items": items[start:end], "total": total, "page": page, "page_size": size, "total_pages": (total + size - 1) / size}})
}

func (s *dashboardService) runAutoGroupScheduler(ctx context.Context, router http.Handler) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		state := s.autoGroup.snapshot()
		if !state.Config.Enabled || !state.Config.AutoScan || state.Config.LastScan+int64(state.Config.ScanInterval*60) > s.now().Unix() {
			continue
		}
		internalCtx := context.WithValue(ctx, internalTaskAuthKey{}, true)
		request, err := http.NewRequestWithContext(internalCtx, http.MethodPost, "/api/auto-group/scan?dry_run=false", nil)
		if err != nil {
			continue
		}
		if s.config.AdminToken != "" {
			request.Header.Set("Authorization", "Bearer "+s.config.AdminToken)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
	}
}
