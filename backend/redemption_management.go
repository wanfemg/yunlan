// Adapted from new_api_tools' redemption management, MIT License.
// See frontend/src/tools-reference-LICENSE.txt. SQL access and demo storage use
// the site-admin service; no separate credentials or NewAPI source changes.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

type redemptionManagedCode struct {
	ID             int64  `json:"id"`
	Key            string `json:"key"`
	Name           string `json:"name"`
	Quota          int64  `json:"quota"`
	CreatedTime    int64  `json:"created_time"`
	RedeemedTime   int64  `json:"redeemed_time"`
	UsedUserID     int64  `json:"used_user_id"`
	UsedUsername   string `json:"used_username"`
	ExpiredTime    int64  `json:"expired_time"`
	Status         string `json:"status"`
	UpstreamStatus int64  `json:"upstream_status"`
}

type redemptionManagementFilters struct {
	page, pageSize int
	name, username string
	status         string
	usedUserID     *int64
	start, end     int64 // Inclusive start, exclusive end in the site timezone.
}

type redemptionManagementStats struct {
	TotalCount    int64   `json:"total_count"`
	UnusedCount   int64   `json:"unused_count"`
	UsedCount     int64   `json:"used_count"`
	ExpiredCount  int64   `json:"expired_count"`
	DisabledCount int64   `json:"disabled_count"`
	UnknownCount  int64   `json:"unknown_count"`
	TotalQuota    int64   `json:"total_quota"`
	UnusedQuota   int64   `json:"unused_quota"`
	UsedQuota     int64   `json:"used_quota"`
	ExpiredQuota  int64   `json:"expired_quota"`
	DisabledQuota int64   `json:"disabled_quota"`
	UnknownQuota  int64   `json:"unknown_quota"`
	QuotaPerUnit  float64 `json:"quota_per_unit"`
	AsOf          int64   `json:"as_of"`
}

type redemptionDemoStore struct {
	mu     sync.Mutex
	nextID int64
	codes  map[int64]redemptionManagedCode
}

var redemptionDemoInitMu sync.Mutex

func (s *dashboardService) redemptionDemoStore() *redemptionDemoStore {
	redemptionDemoInitMu.Lock()
	defer redemptionDemoInitMu.Unlock()
	if s.redemptionDemo != nil {
		return s.redemptionDemo
	}
	now := userClock(s).Unix()
	store := &redemptionDemoStore{nextID: 85, codes: make(map[int64]redemptionManagedCode)}
	usernames := map[int64]string{101: "demo_alice", 102: "demo_bob", 103: "demo_carol"}
	for id := int64(107); id <= 160; id++ {
		usernames[id] = fmt.Sprintf("demo_user_%03d", id)
	}
	for id := int64(1); id <= 84; id++ {
		item := redemptionManagedCode{
			ID: id, Key: fmt.Sprintf("%032x", 0x100000+id),
			Name:  []string{"新用户活动", "周末福利", "会员充值", "运营补偿"}[id%4],
			Quota: (id%12 + 1) * 250000, CreatedTime: now - id*86400,
			UpstreamStatus: 1,
		}
		switch id % 6 {
		case 0, 4:
			item.UpstreamStatus, item.RedeemedTime = 3, item.CreatedTime+3600
			item.UsedUserID = 107 + id%54
			item.UsedUsername = usernames[item.UsedUserID]
		case 1:
			item.ExpiredTime = now - id*1800
		case 3:
			item.ExpiredTime = now + id*86400
		}
		if id%29 == 0 {
			item.UpstreamStatus, item.RedeemedTime, item.UsedUserID, item.UsedUsername = 2, 0, 0, ""
		}
		store.codes[id] = item
	}
	s.redemptionDemo = store
	return store
}

func (s *dashboardService) saveDemoRedemptionBatch(plan redemptionPlan, keys []string, quotas []int64, created int64) {
	store := s.redemptionDemoStore()
	store.mu.Lock()
	defer store.mu.Unlock()
	for index, key := range keys {
		id := store.nextID
		store.nextID++
		store.codes[id] = redemptionManagedCode{
			ID: id, Key: key, Name: plan.name, Quota: quotas[index],
			CreatedTime: created, ExpiredTime: plan.expiredTime, UpstreamStatus: 1,
		}
	}
}

func classifyRedemptionCode(code redemptionManagedCode, now int64) string {
	switch code.UpstreamStatus {
	case 3:
		return "used"
	case 2:
		return "disabled"
	case 1:
		// Inconsistent legacy rows must not be advertised as redeemable.
		if code.RedeemedTime != 0 || code.ExpiredTime < 0 {
			return "unknown"
		}
		if code.ExpiredTime > 0 && code.ExpiredTime < now {
			return "expired"
		}
		return "unused"
	default:
		return "unknown"
	}
}

func parseRedemptionManagementFilters(c *gin.Context, location *time.Location, dateOnly bool) (redemptionManagementFilters, error) {
	f := redemptionManagementFilters{page: 1, pageSize: 20}
	if location == nil {
		location = time.UTC
	}
	if !dateOnly {
		var err error
		f.page, err = positiveLogInt(c.Query("page"), 1, 2147483647)
		if err != nil {
			return f, redemptionInvalid("页码须为正整数。")
		}
		f.pageSize, err = positiveLogInt(c.Query("page_size"), 20, 100)
		if err != nil || f.page-1 > (2147483647-f.pageSize)/f.pageSize {
			return f, redemptionInvalid("每页数量或分页偏移无效。")
		}
		f.name, f.username, f.status = strings.TrimSpace(c.Query("name")), strings.TrimSpace(c.Query("username")), c.Query("status")
		if !utf8.ValidString(f.name) || !utf8.ValidString(f.username) || len(f.name) > 512 || len(f.username) > 512 ||
			strings.ContainsRune(f.name, 0) || strings.ContainsRune(f.username, 0) {
			return f, redemptionInvalid("名称或使用用户搜索内容无效。")
		}
		switch f.status {
		case "", "unused", "used", "expired", "disabled", "unknown":
		default:
			return f, redemptionInvalid("不支持的兑换码状态。")
		}
		userID := c.Query("used_user_id")
		if userID == "" {
			userID = c.Query("user_id")
		}
		if userID != "" {
			parsed, err := strconv.ParseInt(userID, 10, 64)
			if err != nil || parsed < 0 {
				return f, redemptionInvalid("使用用户 ID 无效。")
			}
			f.usedUserID = &parsed
		}
	}
	if raw := c.Query("start_date"); raw != "" {
		start, err := time.ParseInLocation("2006-01-02", raw, location)
		if err != nil {
			return f, redemptionInvalid("开始日期须为 YYYY-MM-DD。")
		}
		f.start = start.Unix()
	}
	if raw := c.Query("end_date"); raw != "" {
		end, err := time.ParseInLocation("2006-01-02", raw, location)
		if err != nil {
			return f, redemptionInvalid("结束日期须为 YYYY-MM-DD。")
		}
		f.end = end.AddDate(0, 0, 1).Unix()
	}
	if f.start != 0 && f.end != 0 && f.start >= f.end {
		return f, redemptionInvalid("开始日期不能晚于结束日期。")
	}
	return f, nil
}

func redemptionManagementError(c *gin.Context, err error) {
	var input *redemptionInputError
	if errors.As(err, &input) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": input.Error()})
		return
	}
	c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "error": "兑换码管理暂不可用，请检查数据表、字段及管理数据库权限。"})
}

func (s *dashboardService) redemptionDemoCodes(f redemptionManagementFilters, now int64) []redemptionManagedCode {
	store := s.redemptionDemoStore()
	store.mu.Lock()
	defer store.mu.Unlock()
	items := make([]redemptionManagedCode, 0, len(store.codes))
	for _, code := range store.codes {
		code.Status = classifyRedemptionCode(code, now)
		if f.name != "" && !strings.Contains(strings.ToLower(code.Name), strings.ToLower(f.name)) ||
			f.username != "" && !strings.Contains(strings.ToLower(code.UsedUsername), strings.ToLower(f.username)) ||
			f.usedUserID != nil && code.UsedUserID != *f.usedUserID ||
			f.status != "" && code.Status != f.status ||
			f.start != 0 && code.CreatedTime < f.start || f.end != 0 && code.CreatedTime >= f.end {
			continue
		}
		items = append(items, code)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedTime != items[j].CreatedTime {
			return items[i].CreatedTime > items[j].CreatedTime
		}
		return items[i].ID > items[j].ID
	})
	return items
}

type redemptionManagementQuery struct {
	from, where, projection, status string
	args                            []any
}

func (s *dashboardService) redemptionQuery(ctx context.Context, f redemptionManagementFilters, now int64) (redemptionManagementQuery, error) {
	db := s.management
	has, err := inspectCheckinColumns(ctx, db, "redemptions", "id", "key", "name", "quota", "created_time", "status")
	if err != nil {
		return redemptionManagementQuery{}, err
	}
	column := func(name, fallback string) string {
		if !has[name] {
			return fallback
		}
		return "COALESCE(r." + tokenIdentifier(db.driver, name) + ", " + fallback + ")"
	}
	q := redemptionManagementQuery{from: "redemptions r"}
	status, redeemed, expiry := column("status", "0"), column("redeemed_time", "0"), column("expired_time", "0")
	q.status = "CASE WHEN " + status + " = 3 THEN 'used' WHEN " + status + " = 2 THEN 'disabled' " +
		"WHEN " + status + " = 1 AND " + redeemed + " = 0 AND " + expiry + " >= 0 THEN " +
		"CASE WHEN " + expiry + " > 0 AND " + expiry + " < " + strconv.FormatInt(now, 10) +
		" THEN 'expired' ELSE 'unused' END ELSE 'unknown' END"
	username := "''"
	_, userErr := inspectCheckinColumns(ctx, db, "users", "id", "username")
	if userErr == nil && has["used_user_id"] {
		q.from += " LEFT JOIN users u ON u.id = r.used_user_id AND r.used_user_id > 0"
		username = "COALESCE(u.username, '')"
	} else if f.username != "" {
		return q, errors.New("used username data is unavailable")
	}
	where := []string{"1 = 1"}
	if has["deleted_at"] {
		where = append(where, "r.deleted_at IS NULL")
	}
	if f.name != "" {
		where, q.args = append(where, column("name", "''")+" LIKE ?"), append(q.args, "%"+f.name+"%")
	}
	if f.username != "" {
		where, q.args = append(where, username+" LIKE ?"), append(q.args, "%"+f.username+"%")
	}
	if f.usedUserID != nil {
		if !has["used_user_id"] {
			return q, errors.New("used user ID data is unavailable")
		}
		where, q.args = append(where, column("used_user_id", "0")+" = ?"), append(q.args, *f.usedUserID)
	}
	if f.status != "" {
		where, q.args = append(where, "("+q.status+") = ?"), append(q.args, f.status)
	}
	if f.start != 0 {
		where, q.args = append(where, column("created_time", "0")+" >= ?"), append(q.args, f.start)
	}
	if f.end != 0 {
		where, q.args = append(where, column("created_time", "0")+" < ?"), append(q.args, f.end)
	}
	q.where = strings.Join(where, " AND ")
	q.projection = "r.id, " + column("key", "''") + ", " + column("name", "''") + ", " +
		column("quota", "0") + ", " + column("created_time", "0") + ", " + redeemed + ", " +
		column("used_user_id", "0") + ", " + username + ", " + expiry + ", " + status + ", " + q.status
	return q, nil
}

func (s *dashboardService) redemptionListHTTP(c *gin.Context) {
	f, err := parseRedemptionManagementFilters(c, s.config.Location, false)
	if err != nil {
		redemptionManagementError(c, err)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), queryTimeout)
	defer cancel()
	now := userClock(s).Unix()
	items := make([]redemptionManagedCode, 0)
	var total int64
	offset := (f.page - 1) * f.pageSize
	if s.config.Mode == "demo" {
		all := s.redemptionDemoCodes(f, now)
		total = int64(len(all))
		if offset < len(all) {
			end := offset + f.pageSize
			if end > len(all) {
				end = len(all)
			}
			items = all[offset:end]
		}
	} else {
		query, queryErr := s.redemptionQuery(ctx, f, now)
		if queryErr != nil {
			redemptionManagementError(c, queryErr)
			return
		}
		if err := s.management.queryRow(ctx, "SELECT COUNT(*) FROM "+query.from+" WHERE "+query.where, query.args...).Scan(&total); err != nil {
			redemptionManagementError(c, err)
			return
		}
		args := append(append([]any{}, query.args...), f.pageSize, offset)
		rows, err := s.management.query(ctx, "SELECT "+query.projection+" FROM "+query.from+" WHERE "+query.where+
			" ORDER BY r.created_time DESC, r.id DESC LIMIT ? OFFSET ?", args...)
		if err != nil {
			redemptionManagementError(c, err)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var item redemptionManagedCode
			if err := rows.Scan(&item.ID, &item.Key, &item.Name, &item.Quota, &item.CreatedTime, &item.RedeemedTime,
				&item.UsedUserID, &item.UsedUsername, &item.ExpiredTime, &item.UpstreamStatus, &item.Status); err != nil {
				redemptionManagementError(c, err)
				return
			}
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			redemptionManagementError(c, err)
			return
		}
	}
	pages := (total + int64(f.pageSize) - 1) / int64(f.pageSize)
	if pages < 1 {
		pages = 1
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
		"items": items, "total": total, "page": f.page, "page_size": f.pageSize, "total_pages": pages,
		"quota_per_unit": s.config.QuotaPerUnit, "as_of": now, "mode": s.config.Mode,
	}})
}

func (s *dashboardService) redemptionStatisticsHTTP(c *gin.Context) {
	f, err := parseRedemptionManagementFilters(c, s.config.Location, true)
	if err != nil {
		redemptionManagementError(c, err)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), queryTimeout)
	defer cancel()
	now := userClock(s).Unix()
	stats := redemptionManagementStats{QuotaPerUnit: s.config.QuotaPerUnit, AsOf: now}
	if s.config.Mode == "demo" {
		for _, code := range s.redemptionDemoCodes(f, now) {
			stats.TotalCount++
			stats.TotalQuota += code.Quota
			switch code.Status {
			case "unused":
				stats.UnusedCount++
				stats.UnusedQuota += code.Quota
			case "used":
				stats.UsedCount++
				stats.UsedQuota += code.Quota
			case "expired":
				stats.ExpiredCount++
				stats.ExpiredQuota += code.Quota
			case "disabled":
				stats.DisabledCount++
				stats.DisabledQuota += code.Quota
			default:
				stats.UnknownCount++
				stats.UnknownQuota += code.Quota
			}
		}
	} else {
		query, err := s.redemptionQuery(ctx, f, now)
		if err != nil {
			redemptionManagementError(c, err)
			return
		}
		statement := "SELECT COUNT(*), COALESCE(SUM(quota_value),0)"
		for _, status := range []string{"unused", "used", "expired", "disabled", "unknown"} {
			statement += ", COALESCE(SUM(CASE WHEN status_label = '" + status + "' THEN 1 ELSE 0 END),0)" +
				", COALESCE(SUM(CASE WHEN status_label = '" + status + "' THEN quota_value ELSE 0 END),0)"
		}
		statement += " FROM (SELECT COALESCE(r.quota,0) AS quota_value, " + query.status +
			" AS status_label FROM " + query.from + " WHERE " + query.where + ") redemption_summary"
		if err := s.management.queryRow(ctx, statement, query.args...).Scan(&stats.TotalCount, &stats.TotalQuota,
			&stats.UnusedCount, &stats.UnusedQuota, &stats.UsedCount, &stats.UsedQuota, &stats.ExpiredCount, &stats.ExpiredQuota,
			&stats.DisabledCount, &stats.DisabledQuota, &stats.UnknownCount, &stats.UnknownQuota); err != nil {
			redemptionManagementError(c, err)
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": stats})
}

func normalizeRedemptionDeleteIDs(ids []int64) ([]int64, error) {
	if len(ids) < 1 || len(ids) > maxRedemptionBatch {
		return nil, redemptionInvalid("每次须选择 1–1000 个兑换码。")
	}
	seen := make(map[int64]bool, len(ids))
	result := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id < 1 {
			return nil, redemptionInvalid("兑换码 ID 须为正整数。")
		}
		if !seen[id] {
			seen[id] = true
			result = append(result, id)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, nil
}

func (s *dashboardService) deleteManagedRedemptions(ctx context.Context, requested []int64) (int64, error) {
	ids, err := normalizeRedemptionDeleteIDs(requested)
	if err != nil {
		return 0, err
	}
	if s.config.Mode == "demo" {
		store := s.redemptionDemoStore()
		store.mu.Lock()
		defer store.mu.Unlock()
		var deleted int64
		for _, id := range ids {
			if _, found := store.codes[id]; found {
				delete(store.codes, id)
				deleted++
			}
		}
		return deleted, nil
	}
	db := s.management
	has, err := inspectCheckinColumns(ctx, db, "redemptions", "id")
	if err != nil {
		return 0, err
	}
	where := "id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + ")"
	statement := "DELETE FROM redemptions WHERE " + where
	if has["deleted_at"] {
		statement = "UPDATE redemptions SET deleted_at = CURRENT_TIMESTAMP WHERE " + where + " AND deleted_at IS NULL"
	}
	args := make([]any, len(ids))
	for index, id := range ids {
		args[index] = id
	}
	tx, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, bindQuery(db.driver, statement), args...)
	if err != nil {
		return 0, err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return deleted, nil
}

func (s *dashboardService) redemptionDeleteHTTP(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id < 1 {
		redemptionManagementError(c, redemptionInvalid("兑换码 ID 无效。"))
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), queryTimeout)
	defer cancel()
	deleted, err := s.deleteManagedRedemptions(ctx, []int64{id})
	if err != nil {
		redemptionManagementError(c, err)
		return
	}
	if deleted == 0 {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "兑换码不存在或已删除。"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"deleted_count": deleted, "deleted": deleted}})
}

func (s *dashboardService) redemptionBatchDeleteHTTP(c *gin.Context) {
	var body struct {
		IDs []int64 `json:"ids"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64*1024)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || decoder.Decode(new(any)) != io.EOF {
		redemptionManagementError(c, redemptionInvalid("批量删除参数须为包含 ids 的单个 JSON 对象。"))
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), queryTimeout)
	defer cancel()
	deleted, err := s.deleteManagedRedemptions(ctx, body.IDs)
	if err != nil {
		redemptionManagementError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
		"deleted_count": deleted, "deleted": deleted, "requested_count": len(body.IDs),
	}})
}
