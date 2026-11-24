package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type checkinModule struct {
	mu      sync.Mutex
	loaded  bool
	reviews map[int64]checkinReview
	cache   map[string]checkinCacheEntry
	pending map[string]*checkinPending
	version uint64
}

type checkinCacheEntry struct {
	data      checkinSnapshot
	expiresAt time.Time
}

type checkinPending struct {
	done chan struct{}
	data checkinSnapshot
	err  error
}

var checkinInitMu sync.Mutex

func (s *dashboardService) checkinState() *checkinModule {
	checkinInitMu.Lock()
	defer checkinInitMu.Unlock()
	if s.checkins == nil {
		s.checkins = &checkinModule{reviews: map[int64]checkinReview{}, cache: map[string]checkinCacheEntry{}, pending: map[string]*checkinPending{}}
	}
	return s.checkins
}

type checkinSource struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Message  string `json:"message"`
	Complete bool   `json:"complete"`
}

type checkinRange struct {
	AsOf             int64           `json:"as_of"`
	StartDate        string          `json:"start_date"`
	EndDate          string          `json:"end_date"`
	EndDateExclusive string          `json:"end_date_exclusive"`
	Timezone         string          `json:"timezone"`
	QuotaPerUnit     float64         `json:"quota_per_unit"`
	Mode             string          `json:"mode"`
	SourceStatus     []checkinSource `json:"source_status"`
	Warnings         []string        `json:"warnings"`
	start, end       time.Time
	days             int
}

type checkinFilters struct {
	checkinRange
	scope, category, search, group, status, sort, order string
	minDays, page, pageSize                             int
	maxRequests                                         *int64
	month                                               string
	date                                                string
	userID                                              int64
}

type checkinRecord struct {
	ID           int64  `json:"id"`
	UserID       int64  `json:"user_id"`
	CheckinDate  string `json:"checkin_date"`
	QuotaAwarded int64  `json:"quota_awarded"`
	CreatedAt    int64  `json:"created_at"`
}

type checkinPayment struct {
	WalletMoney             *string `json:"wallet_money"`
	SubscriptionMoney       *string `json:"subscription_money"`
	WindowWalletMoney       *string `json:"window_wallet_money"`
	WindowSubscriptionMoney *string `json:"window_subscription_money"`
	PaidCount               *int64  `json:"paid_count"`
	HistoryComplete         bool    `json:"history_complete"`
	RedemptionCount         *int64  `json:"redemption_count"`
	RedemptionQuota         *int64  `json:"redemption_quota"`
	RedemptionStatus        string  `json:"redemption_status"`
	UnverifiedPayments      int64   `json:"unverified_payments"`
}

type checkinUser struct {
	ID                   int64          `json:"id"`
	Username             string         `json:"username"`
	DisplayName          string         `json:"display_name"`
	Group                string         `json:"group"`
	Status               int64          `json:"status"`
	Quota                int64          `json:"quota"`
	CreatedAt            int64          `json:"created_at"`
	LastUsedAt           int64          `json:"last_used_at"`
	CheckinDays          *int64         `json:"checkin_days"`
	CheckinCount         *int64         `json:"checkin_count"`
	AwardedQuota         *int64         `json:"awarded_quota"`
	ParticipationRate    *float64       `json:"participation_rate"`
	ParticipationBasis   string         `json:"participation_basis"`
	LifetimeCheckinDays  *int64         `json:"lifetime_checkin_days"`
	LifetimeCheckinCount *int64         `json:"lifetime_checkin_count"`
	LifetimeAwardedQuota *int64         `json:"lifetime_awarded_quota"`
	Requests             *int64         `json:"requests"`
	ConsumedQuota        *int64         `json:"consumed_quota"`
	LifetimeRequests     *int64         `json:"lifetime_requests"`
	LifetimeConsumed     *int64         `json:"lifetime_consumed_quota"`
	LastCheckinDate      string         `json:"last_checkin_date"`
	Category             string         `json:"category"`
	CategoryReasons      []string       `json:"category_reasons"`
	Payment              checkinPayment `json:"payment"`
	Review               *checkinReview `json:"review"`
}

type checkinTrend struct {
	Date         string `json:"date"`
	Users        int64  `json:"users"`
	Records      int64  `json:"records"`
	AwardedQuota int64  `json:"awarded_quota"`
}

type checkinSnapshot struct {
	checkinRange
	users      []checkinUser
	records    []checkinRecord
	checkinsOK bool
	logsOK     bool
	quality    map[string]int64
}

func registerCheckinRoutes(router *gin.Engine, service *dashboardService, token string) {
	g := router.Group("/api/checkins", requireAdminToken(token))
	g.GET("/summary", service.checkinSummaryHTTP)
	g.GET("/users", service.checkinUsersHTTP)
	g.GET("/users/:id", service.checkinDetailHTTP)
	g.POST("/users/:id/review", service.checkinReviewHTTP)
	g.GET("/export", service.checkinExportHTTP)
}

func (s *dashboardService) parseCheckinFilters(c *gin.Context) (checkinFilters, error) {
	location := s.config.Location
	if location == nil {
		location = time.UTC
	}
	now := userClock(s).In(location)
	asOf := now.Unix()
	if raw := c.Query("as_of"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 1 || value > asOf+5 {
			return checkinFilters{}, errors.New("统计时点无效")
		}
		asOf = value
	}
	at := time.Unix(asOf, 0).In(location)
	today := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, location)
	days, err := positiveLogInt(c.Query("days"), 30, 90)
	if err != nil || days != 7 && days != 30 && days != 90 {
		return checkinFilters{}, errors.New("统计天数须为 7、30 或 90")
	}
	start, last := today.AddDate(0, 0, 1-days), today
	rawStart, rawEnd := c.Query("start_date"), c.Query("end_date")
	if rawStart != "" || rawEnd != "" {
		if rawStart == "" || rawEnd == "" {
			return checkinFilters{}, errors.New("开始和结束日期须同时填写")
		}
		start, err = time.ParseInLocation("2006-01-02", rawStart, location)
		if err != nil {
			return checkinFilters{}, errors.New("开始日期格式无效")
		}
		last, err = time.ParseInLocation("2006-01-02", rawEnd, location)
		if err != nil || last.Before(start) || last.After(today) {
			return checkinFilters{}, errors.New("结束日期须不早于开始日期且不晚于统计当日")
		}
		days = 0
		for day := start; !day.After(last) && days <= 366; day = day.AddDate(0, 0, 1) {
			days++
		}
		if days > 366 {
			return checkinFilters{}, errors.New("自定义日期范围最多 366 天")
		}
	}
	endExclusive := last.AddDate(0, 0, 1)
	end := endExclusive
	if !end.Before(at) {
		end = at.Add(time.Second)
	}
	f := checkinFilters{
		checkinRange: checkinRange{AsOf: asOf, StartDate: start.Format("2006-01-02"),
			EndDate: last.Format("2006-01-02"), EndDateExclusive: endExclusive.Format("2006-01-02"),
			Timezone: location.String(), QuotaPerUnit: s.config.QuotaPerUnit, Mode: s.config.Mode,
			start: start, end: end, days: days, SourceStatus: []checkinSource{}, Warnings: []string{}},
		scope: c.DefaultQuery("scope", "participants"), category: c.DefaultQuery("category", "all"),
		search: strings.TrimSpace(c.Query("search")), group: strings.TrimSpace(c.Query("group")),
		status: c.DefaultQuery("status", "all"), sort: c.DefaultQuery("sort", "consumed_quota"),
		order: c.DefaultQuery("order", "desc"), month: c.DefaultQuery("month", at.Format("2006-01")), date: c.Query("date"),
	}
	if f.scope != "participants" && f.scope != "all" {
		return f, errors.New("不支持的用户范围")
	}
	switch f.category {
	case "all", "pure_free", "unpaid_usage", "reward_only", "unused", "paid", "unknown":
	default:
		return f, errors.New("不支持的用户分类")
	}
	if f.status != "all" && f.status != "enabled" && f.status != "disabled" {
		return f, errors.New("不支持的账号状态")
	}
	if f.order != "asc" && f.order != "desc" {
		return f, errors.New("不支持的排序方向")
	}
	switch f.sort {
	case "consumed_quota", "awarded_quota", "checkin_days", "lifetime_consumed_quota", "created_at":
	default:
		return f, errors.New("不支持的排序字段")
	}
	if !validLogFilter(f.search, 128) || !validLogFilter(f.group, 128) {
		return f, errors.New("搜索条件过长或无效")
	}
	if f.page, err = positiveLogInt(c.Query("page"), 1, 1000000); err != nil {
		return f, err
	}
	if f.pageSize, err = positiveLogInt(c.Query("page_size"), 20, 100); err != nil {
		return f, err
	}
	if raw := c.Query("min_checkin_days"); raw != "" {
		f.minDays, err = strconv.Atoi(raw)
		if err != nil || f.minDays < 0 || f.minDays > 366 {
			return f, errors.New("最低签到天数无效")
		}
	}
	if raw := c.Query("max_requests"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 0 {
			return f, errors.New("最大请求数无效")
		}
		f.maxRequests = &value
	}
	if _, err := time.ParseInLocation("2006-01", f.month, location); err != nil {
		return f, errors.New("日历月份格式须为 YYYY-MM")
	}
	if f.date != "" {
		date, err := time.ParseInLocation("2006-01-02", f.date, location)
		if err != nil || date.After(today) {
			return f, errors.New("记录日期须为不晚于统计当日的 YYYY-MM-DD")
		}
	}
	if raw := c.Query("user_id"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value <= 0 {
			return f, errors.New("账号 ID 无效")
		}
		f.userID = value
	}
	return f, nil
}

func (s *dashboardService) checkinHTTPData(c *gin.Context) (checkinFilters, checkinSnapshot, bool) {
	f, err := s.parseCheckinFilters(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return f, checkinSnapshot{}, false
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	data, err := s.cachedCheckinSnapshot(ctx, f)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "error": "签到分析暂不可用，请检查主数据源"})
		return f, data, false
	}
	return f, data, true
}

func checkinValue(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func filterCheckinUsers(users []checkinUser, f checkinFilters, includeCategory bool) []checkinUser {
	items := make([]checkinUser, 0, len(users))
	search := strings.ToLower(f.search)
	for _, user := range users {
		if f.userID > 0 && user.ID != f.userID ||
			f.scope == "participants" && checkinValue(user.CheckinCount) < 1 ||
			includeCategory && f.category != "all" && user.Category != f.category ||
			f.group != "" && user.Group != f.group ||
			f.status == "enabled" && user.Status != 1 ||
			f.status == "disabled" && user.Status != 2 ||
			f.minDays > 0 && (user.CheckinDays == nil || *user.CheckinDays < int64(f.minDays)) ||
			f.maxRequests != nil && (user.Requests == nil || *user.Requests > *f.maxRequests) {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(fmt.Sprintf("%d %s %s", user.ID, user.Username, user.DisplayName)), search) {
			continue
		}
		items = append(items, user)
	}
	number := func(user checkinUser) *int64 {
		switch f.sort {
		case "awarded_quota":
			return user.AwardedQuota
		case "checkin_days":
			return user.CheckinDays
		case "lifetime_consumed_quota":
			return user.LifetimeConsumed
		case "created_at":
			return &user.CreatedAt
		default:
			return user.ConsumedQuota
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := number(items[i]), number(items[j])
		if a == nil || b == nil {
			if a == nil && b == nil {
				return items[i].ID < items[j].ID
			}
			return a != nil
		}
		if *a == *b {
			return items[i].ID < items[j].ID
		}
		if f.order == "asc" {
			return *a < *b
		}
		return *a > *b
	})
	return items
}

func checkinCategories(users []checkinUser) map[string]int64 {
	counts := map[string]int64{"pure_free": 0, "unpaid_usage": 0, "reward_only": 0, "unused": 0, "paid": 0, "unknown": 0}
	for _, user := range users {
		counts[user.Category]++
	}
	return counts
}

func (s *dashboardService) checkinSummaryHTTP(c *gin.Context) {
	f, data, ok := s.checkinHTTPData(c)
	if !ok {
		return
	}
	base := filterCheckinUsers(data.users, f, false)
	users := base
	allowed := make(map[int64]bool, len(users))
	var participantCount, active, paid, pure, pureConsumed, rewardOnly, unknown int64
	for _, user := range users {
		allowed[user.ID] = true
		if checkinValue(user.CheckinCount) > 0 {
			participantCount++
			if checkinValue(user.Requests) > 0 {
				active++
			}
			if user.Category == "paid" {
				paid++
			}
		}
		switch user.Category {
		case "pure_free":
			pure++
			pureConsumed += checkinValue(user.ConsumedQuota)
		case "reward_only":
			rewardOnly++
		case "unknown":
			unknown++
		}
	}
	trend := make([]checkinTrend, 0, f.days)
	byDate := map[string]int{}
	distinct := map[string]map[int64]bool{}
	for day := f.start; day.Format("2006-01-02") < f.EndDateExclusive; day = day.AddDate(0, 0, 1) {
		key := day.Format("2006-01-02")
		byDate[key] = len(trend)
		distinct[key] = map[int64]bool{}
		trend = append(trend, checkinTrend{Date: key})
	}
	var records, awarded, todayUsers int64
	todaySet := map[int64]bool{}
	lifetimeUsers := map[int64]bool{}
	var lifetimeRecords, lifetimeAwarded int64
	seen := map[string]bool{}
	today := time.Unix(f.AsOf, 0).In(f.start.Location()).Format("2006-01-02")
	for _, record := range data.records {
		if !allowed[record.UserID] {
			continue
		}
		lifetimeUsers[record.UserID] = true
		lifetimeRecords++
		lifetimeAwarded += record.QuotaAwarded
		if record.CheckinDate == today {
			todaySet[record.UserID] = true
		}
		index, inWindow := byDate[record.CheckinDate]
		if !inWindow {
			continue
		}
		awarded += record.QuotaAwarded
		trend[index].AwardedQuota += record.QuotaAwarded
		key := fmt.Sprintf("%d:%s", record.UserID, record.CheckinDate)
		if !seen[key] {
			seen[key] = true
			records++
			trend[index].Records++
		}
		distinct[record.CheckinDate][record.UserID] = true
	}
	for index := range trend {
		trend[index].Users = int64(len(distinct[trend[index].Date]))
	}
	todayUsers = int64(len(todaySet))
	var checkinNumber, amount, todayNumber, distinctNumber, historicalUsers, historicalCount, historicalQuota *int64
	var avgUser, avgCheckin, activeRate, paidRate *float64
	if data.checkinsOK {
		checkinNumber, amount, todayNumber, distinctNumber = &records, &awarded, &todayUsers, &participantCount
		lu := int64(len(lifetimeUsers))
		historicalUsers, historicalCount, historicalQuota = &lu, &lifetimeRecords, &lifetimeAwarded
		if participantCount > 0 {
			avgUser = float64Pointer(float64(awarded) / float64(participantCount))
			if data.logsOK {
				activeRate = float64Pointer(float64(active) / float64(participantCount))
			}
			paidRate = float64Pointer(float64(paid) / float64(participantCount))
		}
		if records > 0 {
			avgCheckin = float64Pointer(float64(awarded) / float64(records))
		}
	} else {
		trend = []checkinTrend{}
	}
	var activeNumber *int64
	if data.logsOK {
		activeNumber = &active
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": struct {
		checkinRange
		Summary    any              `json:"summary"`
		Trend      []checkinTrend   `json:"trend"`
		Categories map[string]int64 `json:"categories"`
		Quality    map[string]int64 `json:"quality"`
	}{data.checkinRange, gin.H{
		"today_users": todayNumber, "users": distinctNumber, "records": checkinNumber, "awarded_quota": amount,
		"avg_quota_per_user": avgUser, "avg_quota_per_checkin": avgCheckin,
		"active_users": activeNumber, "active_rate": activeRate, "paid_users": paid, "paid_rate": paidRate,
		"lifetime_users": historicalUsers, "lifetime_records": historicalCount, "lifetime_awarded_quota": historicalQuota,
		"pure_free_users": pure, "pure_free_consumed_quota": func() *int64 {
			if data.logsOK {
				return &pureConsumed
			}
			return nil
		}(), "reward_only_users": rewardOnly, "unknown_users": unknown,
	}, trend, checkinCategories(base), data.quality}})
}

func (s *dashboardService) checkinUsersHTTP(c *gin.Context) {
	f, data, ok := s.checkinHTTPData(c)
	if !ok {
		return
	}
	items := filterCheckinUsers(data.users, f, true)
	categories := checkinCategories(filterCheckinUsers(data.users, f, false))
	total := len(items)
	pages := max(1, (total+f.pageSize-1)/f.pageSize)
	page := min(f.page, pages)
	start, end := (page-1)*f.pageSize, min(page*f.pageSize, total)
	var requests, consumed, award, checkinDays, checkinCount int64
	for _, user := range items {
		requests += checkinValue(user.Requests)
		consumed += checkinValue(user.ConsumedQuota)
		award += checkinValue(user.AwardedQuota)
		checkinDays += checkinValue(user.CheckinDays)
		checkinCount += checkinValue(user.CheckinCount)
	}
	groupSet := map[string]bool{}
	for _, user := range data.users {
		groupSet[user.Group] = true
	}
	groups := make([]string, 0, len(groupSet))
	for name := range groupSet {
		groups = append(groups, name)
	}
	sort.Strings(groups)
	var req, used, rewarded, days, count *int64
	if data.logsOK {
		req, used = &requests, &consumed
	}
	if data.checkinsOK {
		rewarded = &award
		days, count = &checkinDays, &checkinCount
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": struct {
		checkinRange
		Items      []checkinUser    `json:"items"`
		Total      int              `json:"total"`
		Page       int              `json:"page"`
		PageSize   int              `json:"page_size"`
		TotalPages int              `json:"total_pages"`
		Totals     any              `json:"totals"`
		Categories map[string]int64 `json:"categories"`
		Groups     []string         `json:"groups"`
	}{data.checkinRange, items[start:end], total, page, f.pageSize, pages,
		gin.H{"users": total, "requests": req, "consumed_quota": used, "awarded_quota": rewarded, "checkin_days": days, "checkin_count": count}, categories, groups}})
}

func (s *dashboardService) checkinDetailHTTP(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "账号 ID 无效"})
		return
	}
	f, data, ok := s.checkinHTTPData(c)
	if !ok {
		return
	}
	var user *checkinUser
	for index := range data.users {
		if data.users[index].ID == id {
			user = &data.users[index]
			break
		}
	}
	if user == nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "该账号不存在或已删除"})
		return
	}
	records, calendar := []checkinRecord{}, []checkinTrend{}
	perDay := map[string]checkinTrend{}
	dates := map[string]bool{}
	for _, record := range data.records {
		if record.UserID != id {
			continue
		}
		if f.date != "" && record.CheckinDate == f.date || f.date == "" && strings.HasPrefix(record.CheckinDate, f.month+"-") {
			records = append(records, record)
		}
		dates[record.CheckinDate] = true
		if strings.HasPrefix(record.CheckinDate, f.month+"-") {
			day := perDay[record.CheckinDate]
			day.Date, day.Users = record.CheckinDate, 1
			day.Records++
			day.AwardedQuota += record.QuotaAwarded
			perDay[record.CheckinDate] = day
		}
	}
	month, _ := time.ParseInLocation("2006-01", f.month, f.start.Location())
	for day := month; day.Month() == month.Month(); day = day.AddDate(0, 0, 1) {
		key := day.Format("2006-01-02")
		value := perDay[key]
		value.Date = key
		calendar = append(calendar, value)
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].CheckinDate != records[j].CheckinDate {
			return records[i].CheckinDate > records[j].CheckinDate
		}
		return records[i].ID > records[j].ID
	})
	current, longest := checkinStreaks(dates, time.Unix(f.AsOf, 0).In(f.start.Location()))
	total := len(records)
	pages := max(1, (total+f.pageSize-1)/f.pageSize)
	page := min(f.page, pages)
	start, end := (page-1)*f.pageSize, min(page*f.pageSize, total)
	var currentStreak, longestStreak *int64
	if data.checkinsOK {
		currentStreak, longestStreak = &current, &longest
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": struct {
		checkinRange
		User           *checkinUser    `json:"user"`
		Month          string          `json:"month"`
		Calendar       []gin.H         `json:"calendar"`
		Records        []checkinRecord `json:"records"`
		Total          int             `json:"total"`
		Page           int             `json:"page"`
		PageSize       int             `json:"page_size"`
		TotalPages     int             `json:"total_pages"`
		CurrentStreak  *int64          `json:"current_streak"`
		LongestStreak  *int64          `json:"longest_streak"`
		StreakComplete bool            `json:"streak_complete"`
		Review         *checkinReview  `json:"review"`
	}{data.checkinRange, user, f.month, checkinCalendarPayload(calendar), records[start:end], total, page, f.pageSize, pages,
		currentStreak, longestStreak, data.checkinsOK && s.config.Mode == "demo", user.Review}})
}

func checkinCalendarPayload(calendar []checkinTrend) []gin.H {
	items := make([]gin.H, 0, len(calendar))
	for _, day := range calendar {
		items = append(items, gin.H{"date": day.Date, "count": day.Records, "awarded_quota": day.AwardedQuota})
	}
	return items
}

func checkinStreaks(dates map[string]bool, asOf time.Time) (int64, int64) {
	keys := make([]string, 0, len(dates))
	for key := range dates {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var longest, run int64
	var previous time.Time
	for _, key := range keys {
		day, err := time.ParseInLocation("2006-01-02", key, asOf.Location())
		if err != nil {
			continue
		}
		if !previous.IsZero() && previous.AddDate(0, 0, 1).Equal(day) {
			run++
		} else {
			run = 1
		}
		if run > longest {
			longest = run
		}
		previous = day
	}
	today := time.Date(asOf.Year(), asOf.Month(), asOf.Day(), 0, 0, 0, 0, asOf.Location())
	if !dates[today.Format("2006-01-02")] {
		today = today.AddDate(0, 0, -1)
	}
	var current int64
	for dates[today.Format("2006-01-02")] {
		current++
		today = today.AddDate(0, 0, -1)
	}
	return current, longest
}

func (s *dashboardService) checkinExportHTTP(c *gin.Context) {
	f, data, ok := s.checkinHTTPData(c)
	if !ok {
		return
	}
	items := filterCheckinUsers(data.users, f, true)
	if len(items) > 100000 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "导出超过 100000 个账号，请缩小筛选范围"})
		return
	}
	var output bytes.Buffer
	output.WriteString("\xef\xbb\xbf")
	writer := csv.NewWriter(&output)
	_ = writer.Write([]string{"账号ID", "用户名", "昵称", "分组", "分类", "分类依据", "签到天数", "签到人次", "发放原始额度",
		"窗口请求", "窗口消费原始额度", "历史观测消费原始额度", "历史钱包支付", "历史外部订阅支付", "最近签到", "统计时点"})
	value := func(number *int64) string {
		if number == nil {
			return ""
		}
		return strconv.FormatInt(*number, 10)
	}
	money := func(number *string) string {
		if number == nil {
			return ""
		}
		return *number
	}
	for _, user := range items {
		_ = writer.Write([]string{strconv.FormatInt(user.ID, 10), safeUserCSV(user.Username), safeUserCSV(user.DisplayName),
			safeUserCSV(user.Group), user.Category, safeUserCSV(strings.Join(user.CategoryReasons, "；")),
			value(user.CheckinDays), value(user.CheckinCount), value(user.AwardedQuota),
			value(user.Requests), value(user.ConsumedQuota), value(user.LifetimeConsumed), money(user.Payment.WalletMoney),
			money(user.Payment.SubscriptionMoney), user.LastCheckinDate, strconv.FormatInt(f.AsOf, 10)})
	}
	writer.Flush()
	if writer.Error() != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "导出失败"})
		return
	}
	c.Header("Content-Disposition", `attachment; filename="checkin-analysis.csv"`)
	c.Header("X-Statistics-As-Of", strconv.FormatInt(f.AsOf, 10))
	c.Data(http.StatusOK, "text/csv; charset=utf-8", output.Bytes())
}
