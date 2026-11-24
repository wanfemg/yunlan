package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

// Demo tokens are independent records. Mutations change only this process's
// store; usage and network evidence are always derived from networkStore.
type demoTokenRecord struct {
	ID, UserID, Status                     int64
	Key, Name, Username, Group             string
	UserStatus                             int64
	QuotaLimit, UsedQuota, RemainQuota     int64
	UnlimitedQuota                         bool
	ModelLimits, AllowIPs                  string
	CreatedTime, AccessedTime, ExpiredTime int64
}

type demoTokenStore struct {
	mu    sync.Mutex
	items map[int64]demoTokenRecord
}

var demoTokenInitMu sync.Mutex

func newDemoTokenStore(s *dashboardService) *demoTokenStore {
	now := userClock(s).Unix()
	users, _ := s.managedUsers(context.Background(), 0)
	byID := make(map[int64]managedUserRecord, len(users))
	for _, user := range users {
		byID[user.ID] = user
	}
	store := &demoTokenStore{items: make(map[int64]demoTokenRecord, 180)}
	for userID := int64(101); userID <= 160; userID++ {
		user := byID[userID]
		if user.Username == "" {
			user = managedUserRecord{
				ID: userID, Username: fmt.Sprintf("demo_user_%03d", userID),
				Status: 1, Group: "default", CreatedAt: now - 30*86400,
			}
		}
		for index := int64(0); index < 3; index++ {
			id := userID*10 + index
			record := demoTokenRecord{
				ID: id, UserID: userID, Status: 1, UserStatus: user.Status,
				Key:      fmt.Sprintf("demo-siteadmin-%d", id),
				Name:     []string{"生产调用", "备用服务", "开发调试"}[index] + fmt.Sprintf("-%d", userID),
				Username: user.Username, Group: user.Group,
				QuotaLimit:  (8 + userID%12 + index*3) * 500000,
				CreatedTime: user.CreatedAt + index*60, ExpiredTime: -1,
			}
			if record.Group == "" {
				record.Group = "default"
			}
			if record.CreatedTime <= 0 || record.CreatedTime > now {
				record.CreatedTime = now - (30-index)*86400
			}
			if index == 1 {
				switch userID % 4 {
				case 1:
					record.Status = 2
				case 2:
					record.Status, record.QuotaLimit = 4, 0
				case 3:
					record.Status, record.ExpiredTime = 3, now-2*86400
				}
				record.ModelLimits = "gpt-4.1-mini,deepseek-chat"
			}
			if index == 2 {
				switch userID % 5 {
				case 0:
					record.Status, record.ExpiredTime = 3, now-86400
				case 1:
					record.Status = 2
				case 2:
					record.Status, record.QuotaLimit = 4, 0
				case 3:
					record.ExpiredTime = now + 14*86400
				case 4:
					record.UnlimitedQuota = true
				}
				record.AllowIPs = []string{"1.1.1.1/32", "8.8.8.8/32", "1.0.0.1/32"}[userID%3]
			}
			store.items[id] = record
		}
	}
	return store
}

func (s *dashboardService) demoTokenState() *demoTokenStore {
	demoTokenInitMu.Lock()
	defer demoTokenInitMu.Unlock()
	if s.tokenDemo == nil {
		s.tokenDemo = newDemoTokenStore(s)
	}
	return s.tokenDemo
}

func (store *demoTokenStore) snapshotRecords() []demoTokenRecord {
	store.mu.Lock()
	defer store.mu.Unlock()
	items := make([]demoTokenRecord, 0, len(store.items))
	for _, record := range store.items {
		items = append(items, record)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID > items[j].ID })
	return items
}

func demoTokenExpired(record demoTokenRecord, now int64) bool {
	return record.Status == 3 || record.ExpiredTime > 0 && record.ExpiredTime <= now
}

func (s *dashboardService) demoTokenInventory() (total, active int64) {
	now := userClock(s).Unix()
	for _, record := range s.demoTokenState().snapshotRecords() {
		total++
		if record.Status == 1 && !demoTokenExpired(record, now) {
			active++
		}
	}
	return total, active
}

func demoTokenInt(event networkEvent, name string) int64 {
	value, err := strconv.ParseInt(event.Metadata[name], 10, 64)
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func demoTokenDuration(event networkEvent) float64 {
	raw := event.Metadata["duration_seconds"]
	if raw == "" {
		raw = event.Metadata["use_time"]
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return value
}

func (s *dashboardService) demoTokenCalls(ctx context.Context, start, end int64) ([]networkEvent, error) {
	if s.network == nil {
		return []networkEvent{}, nil
	}
	events, err := s.network.Events(ctx, networkSourceCall, start, end)
	if err != nil {
		return nil, err
	}
	calls := make([]networkEvent, 0, len(events))
	for _, event := range events {
		if event.Source == networkSourceCall && event.TokenID > 0 && (event.Result == "success" || event.Result == "failure") {
			calls = append(calls, event)
		}
	}
	return calls, nil
}

func (s *dashboardService) demoTokenRecords(ctx context.Context) ([]demoTokenRecord, error) {
	records := s.demoTokenState().snapshotRecords()
	events, err := s.demoTokenCalls(ctx, 0, userClock(s).Unix()+1)
	if err != nil {
		return nil, err
	}
	used, lastUsed := map[int64]int64{}, map[int64]int64{}
	for _, event := range events {
		used[event.TokenID] += demoTokenInt(event, "quota")
		if event.OccurredAt > lastUsed[event.TokenID] {
			lastUsed[event.TokenID] = event.OccurredAt
		}
	}
	users, err := s.managedUsers(ctx, 0)
	if err != nil {
		return nil, err
	}
	byUser := make(map[int64]managedUserRecord, len(users))
	for _, user := range users {
		byUser[user.ID] = user
	}
	for index := range records {
		record := &records[index]
		record.UsedQuota, record.AccessedTime = used[record.ID], lastUsed[record.ID]
		record.RemainQuota = record.QuotaLimit - record.UsedQuota
		if record.RemainQuota < 0 {
			record.RemainQuota = 0
		}
		if user, exists := byUser[record.UserID]; exists {
			record.Username, record.UserStatus = user.Username, user.Status
		}
	}
	return records, nil
}

func demoTokenListItem(record demoTokenRecord) map[string]any {
	return map[string]any{
		"id": record.ID, "key": "sk-" + maskTokenKey(record.Key), "name": record.Name,
		"user_id": record.UserID, "username": record.Username, "status": record.Status,
		"quota": record.UsedQuota + record.RemainQuota, "used_quota": record.UsedQuota,
		"remain_quota": record.RemainQuota, "unlimited_quota": record.UnlimitedQuota,
		"models": record.ModelLimits, "subnet": record.AllowIPs, "group": record.Group,
		"created_time": record.CreatedTime, "accessed_time": record.AccessedTime,
		"expired_time": record.ExpiredTime,
	}
}

func demoTokenMatches(record demoTokenRecord, f tokenListFilters, now int64) bool {
	if f.name != "" && !strings.Contains(strings.ToLower(record.Name), strings.ToLower(f.name)) ||
		f.key != "" && normalizeTokenKey(f.key) != record.Key ||
		f.userID > 0 && f.userID != record.UserID ||
		f.group != "" && f.group != record.Group {
		return false
	}
	expired := demoTokenExpired(record, now)
	switch f.status {
	case "active":
		if record.Status != 1 || expired {
			return false
		}
	case "disabled":
		if record.Status != 2 || expired {
			return false
		}
	case "expired":
		if !expired {
			return false
		}
	case "exhausted":
		if record.Status != 4 || expired {
			return false
		}
	}
	if f.expired == "yes" && !expired || f.expired == "no" && expired {
		return false
	}
	switch f.risk {
	case "no_ip_limit":
		return strings.TrimSpace(record.AllowIPs) == ""
	case "never_expire":
		return record.ExpiredTime <= 0
	case "unlimited":
		return record.UnlimitedQuota
	}
	return true
}

type demoTokenNetworkSummary struct {
	ips      map[string]bool
	requests int64
}

func demoTokenNetworks(events []networkEvent) map[int64]*demoTokenNetworkSummary {
	out := make(map[int64]*demoTokenNetworkSummary)
	for _, event := range events {
		item := out[event.TokenID]
		if item == nil {
			item = &demoTokenNetworkSummary{ips: map[string]bool{}}
			out[event.TokenID] = item
		}
		item.requests++
		if ip := strings.TrimSpace(event.IP); ip != "" {
			item.ips[ip] = true
		}
	}
	return out
}

func (s *dashboardService) demoTokensListHTTP(c *gin.Context) {
	f, err := s.parseTokenFilters(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), queryTimeout)
	defer cancel()
	records, err := s.demoTokenRecords(ctx)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "demo token list is unavailable"})
		return
	}
	now := userClock(s).Unix()
	events, err := s.demoTokenCalls(ctx, now-24*3600, now+1)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "demo token network statistics are unavailable"})
		return
	}
	network := demoTokenNetworks(events)
	filtered := make([]demoTokenRecord, 0, len(records))
	for _, record := range records {
		if demoTokenMatches(record, f, now) {
			filtered = append(filtered, record)
		}
	}
	total := len(filtered)
	start := (f.page - 1) * f.pageSize
	if start > total {
		start = total
	}
	end := start + f.pageSize
	if end > total {
		end = total
	}
	items := make([]map[string]any, 0, end-start)
	for _, record := range filtered[start:end] {
		item := demoTokenListItem(record)
		item["ip_count"], item["request_count"] = 0, int64(0)
		if stats := network[record.ID]; stats != nil {
			item["ip_count"], item["request_count"] = len(stats.ips), stats.requests
		}
		items = append(items, item)
	}
	pages := (total + f.pageSize - 1) / f.pageSize
	if pages < 1 {
		pages = 1
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
		"items": items, "total": total, "page": f.page,
		"page_size": f.pageSize, "total_pages": pages,
	}})
}

func (s *dashboardService) demoTokenStatisticsHTTP(c *gin.Context) {
	counts := map[string]int64{"total": 0, "active": 0, "disabled": 0, "expired": 0, "exhausted": 0}
	now := userClock(s).Unix()
	for _, record := range s.demoTokenState().snapshotRecords() {
		counts["total"]++
		if demoTokenExpired(record, now) {
			counts["expired"]++
			continue
		}
		switch record.Status {
		case 1:
			counts["active"]++
		case 2:
			counts["disabled"]++
		case 4:
			counts["exhausted"]++
		}
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": counts})
}

func (s *dashboardService) demoTokenGroupsHTTP(c *gin.Context) {
	type groupCounts struct {
		name          string
		total, active int64
	}
	byName := map[string]*groupCounts{}
	now := userClock(s).Unix()
	for _, record := range s.demoTokenState().snapshotRecords() {
		item := byName[record.Group]
		if item == nil {
			item = &groupCounts{name: record.Group}
			byName[record.Group] = item
		}
		item.total++
		if record.Status == 1 && !demoTokenExpired(record, now) {
			item.active++
		}
	}
	groups := make([]*groupCounts, 0, len(byName))
	for _, group := range byName {
		groups = append(groups, group)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].total == groups[j].total {
			return groups[i].name < groups[j].name
		}
		return groups[i].total > groups[j].total
	})
	items := make([]map[string]any, 0, len(groups))
	for _, group := range groups {
		items = append(items, map[string]any{"group_name": group.name, "token_count": group.total, "active_count": group.active})
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": items})
}

func (s *dashboardService) demoTokenLookupHTTP(c *gin.Context) {
	var req struct {
		Keys []string `json:"keys"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 256*1024))
	if err := decoder.Decode(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求内容无效"})
		return
	}
	keys, seen := make([]string, 0, len(req.Keys)), map[string]bool{}
	for _, raw := range req.Keys {
		key := normalizeTokenKey(raw)
		if key == "" || seen[key] {
			continue
		}
		if len(key) > 128 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "令牌 key 长度无效"})
			return
		}
		seen[key], keys = true, append(keys, key)
	}
	if len(keys) == 0 || len(keys) > maxTokenBatch {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("每次需查询 1 至 %d 个不同的 key", maxTokenBatch)})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), queryTimeout)
	defer cancel()
	records, err := s.demoTokenRecords(ctx)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "demo token lookup is unavailable"})
		return
	}
	byKey := make(map[string]demoTokenRecord, len(records))
	for _, record := range records {
		byKey[record.Key] = record
	}
	items, found := make([]map[string]any, 0, len(keys)), 0
	for _, key := range keys {
		item := map[string]any{"key_masked": "sk-" + maskTokenKey(key), "found": false}
		if record, exists := byKey[key]; exists {
			item["found"], item["id"], item["name"] = true, record.ID, record.Name
			item["user_id"], item["username"], item["status"] = record.UserID, record.Username, record.Status
			item["expired_time"], item["group"], item["used_quota"] = record.ExpiredTime, record.Group, record.UsedQuota
			found++
		}
		items = append(items, item)
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
		"items": items, "total": len(items), "found": found, "not_found": len(items) - found,
	}})
}

func (s *dashboardService) demoTokenBatchHTTP(c *gin.Context, enable bool) {
	var req struct {
		IDs []int64 `json:"ids"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, 64*1024))
	if err := decoder.Decode(&req); err != nil || len(req.IDs) == 0 || len(req.IDs) > maxTokenBatch {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("每次需提交 1 至 %d 个令牌 ID", maxTokenBatch)})
		return
	}
	ids := make(map[int64]bool, len(req.IDs))
	for _, id := range req.IDs {
		if id < 1 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "令牌 ID 须为正整数"})
			return
		}
		ids[id] = true
	}
	target, expected, action := tokenBatchStatus(enable)
	now, changed := userClock(s).Unix(), int64(0)
	store := s.demoTokenState()
	store.mu.Lock()
	for id := range ids {
		record, exists := store.items[id]
		if !exists || record.Status != int64(expected) || demoTokenExpired(record, now) {
			continue
		}
		record.Status = int64(target)
		store.items[id] = record
		changed++
	}
	store.mu.Unlock()
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
		"requested": len(ids), action: changed,
		"cache_notice": "演示变更已保存到当前进程内存，刷新页面后保留，重启演示服务后恢复初始状态。",
	}})
}

func demoTokenQueryInt(c *gin.Context, name string, fallback, min, max int) (int, error) {
	if raw := c.Query(name); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < min || value > max {
			return 0, fmt.Errorf("%s 须为 %d 至 %d", name, min, max)
		}
		return value, nil
	}
	return fallback, nil
}

func (s *dashboardService) demoTokenIPStatsHTTP(c *gin.Context) {
	ids, err := parseTokenIDs(c.Query("ids"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	hours, err := demoTokenQueryInt(c, "hours", 24, 1, 720)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	out := make([]map[string]any, 0, len(ids))
	if len(ids) > 0 {
		ctx, cancel := context.WithTimeout(c.Request.Context(), queryTimeout)
		defer cancel()
		now := userClock(s).Unix()
		events, err := s.demoTokenCalls(ctx, now-int64(hours)*3600, now+1)
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "demo token IP statistics are unavailable"})
			return
		}
		counts := demoTokenNetworks(events)
		for _, id := range ids {
			if stats := counts[id]; stats != nil && len(stats.ips) > 0 {
				out = append(out, map[string]any{"token_id": id, "ip_count": len(stats.ips)})
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": out})
}

func (s *dashboardService) demoTokenLeaksHTTP(c *gin.Context) {
	f, err := s.parseTokenFilters(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	hours, err := demoTokenQueryInt(c, "hours", 24, 1, 720)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	minIPs, err := demoTokenQueryInt(c, "min_ips", 5, 2, 10000)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	limit, err := demoTokenQueryInt(c, "limit", 100, 1, 200)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), queryTimeout)
	defer cancel()
	records, err := s.demoTokenRecords(ctx)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "demo token leak analysis is unavailable"})
		return
	}
	now := userClock(s).Unix()
	events, err := s.demoTokenCalls(ctx, now-int64(hours)*3600, now+1)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "demo token leak analysis is unavailable"})
		return
	}
	network := demoTokenNetworks(events)
	items := make([]map[string]any, 0)
	for _, record := range records {
		stats := network[record.ID]
		if stats == nil || len(stats.ips) < minIPs || !demoTokenMatches(record, f, now) {
			continue
		}
		ips := make([]string, 0, len(stats.ips))
		for ip := range stats.ips {
			ips = append(ips, ip)
		}
		sort.Strings(ips)
		item := demoTokenListItem(record)
		item["ip_count"], item["request_count"], item["ips"] = len(ips), stats.requests, strings.Join(ips, ",")
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i]["ip_count"].(int) == items[j]["ip_count"].(int) {
			return items[i]["id"].(int64) > items[j]["id"].(int64)
		}
		return items[i]["ip_count"].(int) > items[j]["ip_count"].(int)
	})
	if len(items) > limit {
		items = items[:limit]
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": items})
}

func demoTokenLogID(event networkEvent) int64 {
	if id := demoTokenInt(event, "log_id"); id > 0 {
		return id
	}
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(networkEventKey(event)))
	// Keep JSON numbers within the JavaScript safe integer range.
	return int64(hash.Sum64() & ((1 << 53) - 1))
}

func (s *dashboardService) demoTokenAnalysisHTTP(c *gin.Context) {
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
	records, err := s.demoTokenRecords(ctx)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "demo token analysis is unavailable"})
		return
	}
	var record demoTokenRecord
	for _, candidate := range records {
		if candidate.ID == id {
			record = candidate
			break
		}
	}
	if record.ID == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "令牌不存在"})
		return
	}
	events, err := s.demoTokenCalls(ctx, start, end+1)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "demo token analysis is unavailable"})
		return
	}
	calls := make([]networkEvent, 0)
	for _, event := range events {
		if event.TokenID == id {
			calls = append(calls, event)
		}
	}
	data, err := s.demoTokenAnalysis(ctx, record, calls, start, end, seconds)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "demo token analysis is unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
}

func (s *dashboardService) demoTokenAnalysis(ctx context.Context, record demoTokenRecord, calls []networkEvent, start, end, seconds int64) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if seconds <= 0 {
		return nil, errors.New("invalid analysis window")
	}
	type modelSummary struct {
		name                      string
		requests, quota, failures int64
	}
	type ipSummary struct {
		ip                    string
		requests, first, last int64
	}
	type hourSummary struct {
		slot, requests, quota int64
	}
	models, ips := map[string]*modelSummary{}, map[string]*ipSummary{}
	slotCount := (seconds + 3599) / 3600
	hours := make([]hourSummary, slotCount)
	for index := range hours {
		hours[index].slot = int64(index)
	}
	var success, failure, quota, prompt, completion int64
	var duration float64
	for _, event := range calls {
		amount := demoTokenInt(event, "quota")
		quota += amount
		prompt += demoTokenInt(event, "prompt_tokens")
		completion += demoTokenInt(event, "completion_tokens")
		if event.Result == "success" {
			success++
			duration += demoTokenDuration(event)
		} else {
			failure++
		}
		model := strings.TrimSpace(event.Metadata["model"])
		if model == "" {
			model = "unknown"
		}
		aggregate := models[model]
		if aggregate == nil {
			aggregate = &modelSummary{name: model}
			models[model] = aggregate
		}
		aggregate.requests++
		aggregate.quota += amount
		if event.Result == "failure" {
			aggregate.failures++
		}
		if ip := strings.TrimSpace(event.IP); ip != "" {
			info := ips[ip]
			if info == nil {
				info = &ipSummary{ip: ip, first: event.OccurredAt, last: event.OccurredAt}
				ips[ip] = info
			}
			info.requests++
			if event.OccurredAt < info.first {
				info.first = event.OccurredAt
			}
			if event.OccurredAt > info.last {
				info.last = event.OccurredAt
			}
		}
		slot := (event.OccurredAt - start) / 3600
		if slot >= slotCount {
			slot = slotCount - 1
		}
		if slot >= 0 {
			hours[slot].requests++
			hours[slot].quota += amount
		}
	}
	total := int64(len(calls))
	failureRate, avg := float64(0), float64(0)
	if total > 0 {
		failureRate = float64(failure) / float64(total)
	}
	if success > 0 {
		avg = duration / float64(success)
	}
	modelList := make([]*modelSummary, 0, len(models))
	for _, item := range models {
		modelList = append(modelList, item)
	}
	sort.Slice(modelList, func(i, j int) bool {
		if modelList[i].requests == modelList[j].requests {
			return modelList[i].name < modelList[j].name
		}
		return modelList[i].requests > modelList[j].requests
	})
	if len(modelList) > 10 {
		modelList = modelList[:10]
	}
	topModels := make([]map[string]any, 0, len(modelList))
	for _, item := range modelList {
		topModels = append(topModels, map[string]any{
			"model_name": item.name, "requests": item.requests,
			"quota_used": item.quota, "failure_requests": item.failures,
		})
	}
	ipList := make([]*ipSummary, 0, len(ips))
	for _, item := range ips {
		ipList = append(ipList, item)
	}
	sort.Slice(ipList, func(i, j int) bool {
		if ipList[i].requests == ipList[j].requests {
			return ipList[i].ip < ipList[j].ip
		}
		return ipList[i].requests > ipList[j].requests
	})
	if len(ipList) > 20 {
		ipList = ipList[:20]
	}
	topIPs := make([]map[string]any, 0, len(ipList))
	for _, item := range ipList {
		row := map[string]any{"ip": item.ip, "requests": item.requests, "first_seen": item.first, "last_seen": item.last}
		if s.network != nil {
			info, err := s.network.Lookup(ctx, item.ip)
			if err != nil {
				return nil, err
			}
			label := make([]string, 0, 3)
			for _, value := range []string{info.Country, info.Region, info.City} {
				if value != "" && (len(label) == 0 || label[len(label)-1] != value) {
					label = append(label, value)
				}
			}
			if len(label) > 0 {
				row["geo_label"] = strings.Join(label, " · ") + "（演示）"
			}
		}
		topIPs = append(topIPs, row)
	}
	hourly := make([]map[string]any, 0, len(hours))
	for _, item := range hours {
		hourly = append(hourly, map[string]any{"slot_idx": item.slot, "requests": item.requests, "quota_used": item.quota})
	}
	recentCalls := append([]networkEvent(nil), calls...)
	sort.Slice(recentCalls, func(i, j int) bool {
		if recentCalls[i].OccurredAt == recentCalls[j].OccurredAt {
			return networkEventKey(recentCalls[i]) > networkEventKey(recentCalls[j])
		}
		return recentCalls[i].OccurredAt > recentCalls[j].OccurredAt
	})
	if len(recentCalls) > 30 {
		recentCalls = recentCalls[:30]
	}
	recent := make([]map[string]any, 0, len(recentCalls))
	for _, event := range recentCalls {
		typ := 2
		if event.Result == "failure" {
			typ = 5
		}
		recent = append(recent, map[string]any{
			"id": demoTokenLogID(event), "event_id": event.ID,
			"request_id": event.Metadata["request_id"], "created_at": event.OccurredAt,
			"type": typ, "model_name": event.Metadata["model"], "quota": demoTokenInt(event, "quota"),
			"prompt_tokens": demoTokenInt(event, "prompt_tokens"), "completion_tokens": demoTokenInt(event, "completion_tokens"),
			"use_time": demoTokenDuration(event), "ip": event.IP,
		})
	}
	riskFlags := make([]string, 0)
	if len(ips) >= 5 && strings.TrimSpace(record.AllowIPs) == "" {
		riskFlags = append(riskFlags, "LEAK_SUSPECT")
	}
	if total > 10 && failureRate > 0.5 {
		riskFlags = append(riskFlags, "HIGH_FAILURE_RATE")
	}
	rpm := float64(total) / (float64(seconds) / 60)
	if rpm > 5 {
		riskFlags = append(riskFlags, "HIGH_RPM")
	}
	token := demoTokenListItem(record)
	token["user_status"], token["allow_ips"], token["model_limits"] = record.UserStatus, record.AllowIPs, record.ModelLimits
	return map[string]any{
		"range": map[string]any{"start_time": start, "end_time": end, "window_seconds": seconds},
		"token": token,
		"summary": map[string]any{
			"total_requests": total, "success_requests": success, "failure_requests": failure,
			"quota_used": quota, "prompt_tokens": prompt, "completion_tokens": completion,
			"avg_use_time": avg, "unique_ips": len(ips), "unique_models": len(models), "failure_rate": failureRate,
		},
		"risk":       map[string]any{"requests_per_minute": rpm, "risk_flags": riskFlags},
		"top_models": topModels, "top_ips": topIPs, "hourly": hourly, "recent_logs": recent,
	}, nil
}
