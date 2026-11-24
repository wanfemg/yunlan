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

// These counts follow new_api_tools' model-statistics contract. A consume log
// remains successful when its output token count is zero; the zero-token
// indicator is a subset of successes, not a third mutually exclusive outcome.
type modelLogCounts struct {
	TotalRequests     int64    `json:"total_requests"`
	SuccessCount      int64    `json:"success_count"`
	FailureCount      int64    `json:"failure_count"`
	EmptyCount        *int64   `json:"empty_count"`
	KnownEmptyCount   int64    `json:"known_empty_count"`
	EmptyUnknownCount int64    `json:"empty_unknown_count"`
	SuccessRate       *float64 `json:"success_rate"`
	EmptyRate         *float64 `json:"empty_rate"`
}

type modelLogStatistics struct {
	ModelName string `json:"model_name"`
	modelLogCounts
	Health string `json:"health"`
}

type modelLogSummary struct {
	modelLogCounts
	ModelCount     int64 `json:"model_count"`
	HealthyModels  int64 `json:"healthy_models"`
	WarningModels  int64 `json:"warning_models"`
	CriticalModels int64 `json:"critical_models"`
	UnknownModels  int64 `json:"unknown_models"`
}

type modelLogTrendPoint struct {
	At time.Time `json:"at"`
	modelLogCounts
}

type modelLogRankings struct {
	Requests []logUserRank `json:"requests"`
	Quota    []logUserRank `json:"quota"`
}

type modelLogAnalysisData struct {
	Mode                    string               `json:"mode"`
	Period                  Period               `json:"period"`
	Timezone                string               `json:"timezone"`
	Range                   TimeRange            `json:"range"`
	GeneratedAt             time.Time            `json:"generated_at"`
	QuotaPerUnit            float64              `json:"quota_per_unit"`
	Summary                 modelLogSummary      `json:"summary"`
	Models                  []modelLogStatistics `json:"models"`
	TotalModels             int64                `json:"total_models"`
	Page                    int                  `json:"page"`
	PageSize                int                  `json:"page_size"`
	Trend                   []modelLogTrendPoint `json:"trend"`
	Rankings                modelLogRankings     `json:"rankings"`
	EmptyDetectionAvailable bool                 `json:"empty_detection_available"`
	TokensPresent           bool                 `json:"tokens_present"`
	DataNotes               []string             `json:"data_notes"`
}

type modelLogFilters struct {
	period         Period
	model          string
	channelID      int64
	page, pageSize int
	sort, order    string
	health         string
}

func parseModelLogFilters(c *gin.Context) (modelLogFilters, error) {
	f := modelLogFilters{
		period: Period(c.DefaultQuery("period", string(Period30Days))),
		model:  strings.TrimSpace(c.Query("model")),
		sort:   c.DefaultQuery("sort", "request_count"), order: c.DefaultQuery("order", "desc"),
		health: c.DefaultQuery("health", "all"),
	}
	if _, err := periodDuration(f.period); err != nil {
		return f, errors.New("时间范围须为 24 小时、7 天或 30 天。")
	}
	if !validLogFilter(f.model, 255) {
		return f, errors.New("模型搜索内容无效或超过 255 字。")
	}
	// A status-only input would make all success/failure rates misleading.
	if c.Query("status") != "" || c.Query("request_id") != "" {
		return f, errors.New("模型状态分析按完整成功与失败样本统计，不支持日志类型或单请求筛选。")
	}
	switch f.sort {
	case "request_count", "success_rate", "empty_rate", "failure_count":
	default:
		return f, errors.New("模型排序字段无效。")
	}
	if f.order != "asc" && f.order != "desc" {
		return f, errors.New("排序方向须为 asc 或 desc。")
	}
	switch f.health {
	case "all", "healthy", "warning", "critical", "unknown":
	default:
		return f, errors.New("模型指标状态筛选无效。")
	}
	if raw := c.Query("channel_id"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id < 1 {
			return f, errors.New("渠道 ID 须为正整数。")
		}
		f.channelID = id
	}
	var err error
	if f.page, err = positiveLogInt(c.Query("page"), 1, 2147483647); err != nil {
		return f, err
	}
	if f.pageSize, err = positiveLogInt(c.Query("page_size"), 20, 100); err != nil ||
		f.page-1 > (2147483647-f.pageSize)/f.pageSize {
		return f, errors.New("模型分页参数无效。")
	}
	return f, nil
}

func (counts *modelLogCounts) finish(tokensPresent bool) {
	if counts.TotalRequests > 0 {
		counts.SuccessRate = float64Pointer(float64(counts.SuccessCount) * 100 / float64(counts.TotalRequests))
	}
	if tokensPresent && counts.EmptyUnknownCount == 0 {
		counts.EmptyCount = int64Pointer(counts.KnownEmptyCount)
		if counts.SuccessCount > 0 {
			counts.EmptyRate = float64Pointer(float64(counts.KnownEmptyCount) * 100 / float64(counts.SuccessCount))
		}
	}
}

func (counts modelLogCounts) health() string {
	if counts.SuccessRate == nil {
		return "unknown"
	}
	if *counts.SuccessRate < 80 || counts.EmptyRate != nil && *counts.EmptyRate > 15 {
		return "critical"
	}
	if *counts.SuccessRate < 95 || counts.EmptyRate != nil && *counts.EmptyRate > 5 {
		return "warning"
	}
	if counts.EmptyRate == nil {
		return "unknown"
	}
	return "healthy"
}

func (s *dashboardService) modelLogAnalysisHTTP(c *gin.Context) {
	filter, err := parseModelLogFilters(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	location := s.config.Location
	if location == nil {
		location = time.UTC
	}
	window, _, buckets, err := dashboardRanges(userClock(s), filter.period, location)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "模型分析时间范围无效。"})
		return
	}
	data := modelLogAnalysisData{
		Mode: s.config.Mode, Period: filter.period, Timezone: s.config.Timezone,
		Range: window, GeneratedAt: window.End, QuotaPerUnit: s.config.QuotaPerUnit,
		Models: []modelLogStatistics{}, Page: filter.page, PageSize: filter.pageSize,
		Rankings: modelLogRankings{Requests: []logUserRank{}, Quota: []logUserRank{}},
		Trend:    make([]modelLogTrendPoint, len(buckets)),
		DataNotes: []string{
			"成功数为 type=2，失败数为 type=5；空回复指标是成功日志中输出 Tokens 为 0 的子集，不与成功数、失败数相加。",
			"空回复率按空回复数 ÷ 成功数计算；零输出 Tokens 仅提示疑似空回复，图片、语音、重排等调用可能有实际结果。",
			"按使用日志记录统计，渠道重试可能产生多条记录；这里不冒充去重后的用户请求或主动可用性探测。",
			"总览、趋势和用户排行使用时间、模型、渠道筛选；指标状态仅筛选模型表，分页不会改变总览分母。",
		},
	}
	if data.Timezone == "" {
		data.Timezone = location.String()
	}
	for index, bucket := range buckets {
		data.Trend[index].At = bucket.At
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), queryTimeout)
	defer cancel()
	if s.config.Mode == "demo" {
		err = s.readDemoModelLogAnalysis(ctx, &data, filter, location)
	} else {
		err = data.read(ctx, s.logs, filter)
	}
	if err != nil {
		var input *managedUserError
		if errors.As(err, &input) {
			c.JSON(input.Status, gin.H{"error": input.Message})
			return
		}
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "模型状态统计暂不可用，请检查日志数据库连接和字段。"})
		return
	}
	if !data.TokensPresent {
		data.DataNotes = append(data.DataNotes, "当前日志表没有输出 Tokens 字段，空回复数及空回复率标记为未知。")
	} else if data.Summary.EmptyUnknownCount > 0 {
		data.DataNotes = append(data.DataNotes, "部分成功日志未记录有效输出 Tokens；不把缺失值当作 0，完整空回复数和比例显示未知，另保留已知零输出样本数。")
	}
	c.JSON(http.StatusOK, data)
}

type modelLogQuery struct {
	driver string
	has    map[string]bool
	where  string
	args   []any
}

func (query modelLogQuery) column(column string) string {
	return tokenIdentifier(query.driver, column)
}

func (query modelLogQuery) emptyExpressions() (string, string) {
	if !query.has["completion_tokens"] {
		return "0", "CASE WHEN type = 2 THEN 1 ELSE 0 END"
	}
	tokens := query.column("completion_tokens")
	return "CASE WHEN type = 2 AND " + tokens + " = 0 THEN 1 ELSE 0 END",
		"CASE WHEN type = 2 AND (" + tokens + " IS NULL OR " + tokens + " < 0) THEN 1 ELSE 0 END"
}

func (query modelLogQuery) aggregate() string {
	empty, unknown := query.emptyExpressions()
	name := "COALESCE(" + query.column("model_name") + ", '')"
	return "SELECT " + name + " AS model_name, COUNT(*) AS total_requests, " +
		"SUM(CASE WHEN type = 2 THEN 1 ELSE 0 END) AS success_count, " +
		"SUM(CASE WHEN type = 5 THEN 1 ELSE 0 END) AS failure_count, " +
		"SUM(" + empty + ") AS known_empty_count, SUM(" + unknown + ") AS empty_unknown_count " +
		"FROM logs" + query.where + " GROUP BY " + name
}

func (query modelLogQuery) healthExpression() string {
	complete := "empty_unknown_count = 0 AND success_count > 0"
	if !query.has["completion_tokens"] {
		complete = "1 = 0"
	}
	return "CASE WHEN total_requests = 0 THEN 'unknown' " +
		"WHEN 100.0 * success_count / total_requests < 80 OR (" + complete +
		" AND 100.0 * known_empty_count / NULLIF(success_count, 0) > 15) THEN 'critical' " +
		"WHEN 100.0 * success_count / total_requests < 95 OR (" + complete +
		" AND 100.0 * known_empty_count / NULLIF(success_count, 0) > 5) THEN 'warning' " +
		"WHEN " + complete + " THEN 'healthy' ELSE 'unknown' END"
}

func inspectModelLogQuery(ctx context.Context, tx *sql.Tx, driver string, window TimeRange, f modelLogFilters) (modelLogQuery, error) {
	query := modelLogQuery{driver: driver, has: map[string]bool{}}
	schema, err := tx.QueryContext(ctx, "SELECT * FROM logs WHERE 1 = 0")
	if err != nil {
		return query, err
	}
	columns, err := schema.Columns()
	_ = schema.Close()
	if err != nil {
		return query, err
	}
	for _, column := range columns {
		query.has[strings.ToLower(column)] = true
	}
	for _, column := range []string{"created_at", "type", "model_name"} {
		if !query.has[column] {
			return query, errors.New("model statistics fields are unavailable")
		}
	}
	query.where = " WHERE created_at >= ? AND created_at < ? AND type IN (2, 5)"
	query.args = []any{unixBoundary(window.Start), unixBoundary(window.End)}
	if query.has["deleted_at"] {
		query.where += " AND deleted_at IS NULL"
	}
	if f.model != "" {
		// Model search is a literal substring, not a user-supplied LIKE pattern.
		literal := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(f.model)
		query.where += " AND LOWER(COALESCE(" + query.column("model_name") + ", '')) LIKE LOWER(?) ESCAPE '!'"
		query.args = append(query.args, "%"+literal+"%")
	}
	if f.channelID > 0 {
		if !query.has["channel"] {
			return query, &managedUserError{http.StatusBadRequest, "当前日志表没有渠道字段，暂不支持渠道筛选。"}
		}
		query.where += " AND " + query.column("channel") + " = ?"
		query.args = append(query.args, f.channelID)
	}
	return query, nil
}

func (data *modelLogAnalysisData) read(ctx context.Context, db queryDB, filter modelLogFilters) error {
	if db.db == nil {
		return errors.New("model statistics database is not configured")
	}
	tx, err := db.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	query, err := inspectModelLogQuery(ctx, tx, db.driver, data.Range, filter)
	if err != nil {
		return err
	}
	data.TokensPresent, data.EmptyDetectionAvailable = query.has["completion_tokens"], query.has["completion_tokens"]
	if err := data.readSummary(ctx, tx, query); err != nil {
		return err
	}
	if err := data.readModels(ctx, tx, query, filter); err != nil {
		return err
	}
	if err := data.readTrend(ctx, tx, query); err != nil {
		return err
	}
	if query.has["id"] && query.has["user_id"] {
		if data.Rankings.Requests, err = readModelLogUserRanking(ctx, tx, query, false); err != nil {
			return err
		}
		if query.has["quota"] {
			if data.Rankings.Quota, err = readModelLogUserRanking(ctx, tx, query, true); err != nil {
				return err
			}
		}
	} else {
		data.DataNotes = append(data.DataNotes, "当前日志表未提供完整的用户关联字段，用户排行暂不可用。")
	}
	return tx.Commit()
}

func (data *modelLogAnalysisData) readSummary(ctx context.Context, tx *sql.Tx, query modelLogQuery) error {
	statement := "SELECT COUNT(*), COALESCE(SUM(total_requests),0), COALESCE(SUM(success_count),0), " +
		"COALESCE(SUM(failure_count),0), COALESCE(SUM(known_empty_count),0), COALESCE(SUM(empty_unknown_count),0)"
	for _, health := range []string{"healthy", "warning", "critical", "unknown"} {
		statement += ", COUNT(CASE WHEN (" + query.healthExpression() + ") = '" + health + "' THEN 1 END)"
	}
	statement += " FROM (" + query.aggregate() + ") AS model_statistics"
	summary := &data.Summary
	if err := tx.QueryRowContext(ctx, bindQuery(query.driver, statement), query.args...).Scan(
		&summary.ModelCount, &summary.TotalRequests, &summary.SuccessCount, &summary.FailureCount,
		&summary.KnownEmptyCount, &summary.EmptyUnknownCount, &summary.HealthyModels,
		&summary.WarningModels, &summary.CriticalModels, &summary.UnknownModels,
	); err != nil {
		return err
	}
	summary.finish(data.TokensPresent)
	return nil
}

func (data *modelLogAnalysisData) readModels(ctx context.Context, tx *sql.Tx, query modelLogQuery, filter modelLogFilters) error {
	from := " FROM (" + query.aggregate() + ") AS model_statistics"
	args := append([]any(nil), query.args...)
	if filter.health != "all" {
		from += " WHERE (" + query.healthExpression() + ") = ?"
		args = append(args, filter.health)
	}
	if err := tx.QueryRowContext(ctx, bindQuery(query.driver, "SELECT COUNT(*)"+from), args...).Scan(&data.TotalModels); err != nil {
		return err
	}
	order := "total_requests"
	nulls := ""
	switch filter.sort {
	case "success_rate":
		order = "100.0 * success_count / NULLIF(total_requests, 0)"
	case "empty_rate":
		order = "100.0 * known_empty_count / NULLIF(success_count, 0)"
		nulls = "CASE WHEN empty_unknown_count > 0 OR success_count = 0 THEN 1 ELSE 0 END ASC, "
		if !data.TokensPresent {
			order, nulls = "total_requests", ""
		}
	case "failure_count":
		order = "failure_count"
	}
	statement := "SELECT model_name,total_requests,success_count,failure_count,known_empty_count,empty_unknown_count" +
		from + " ORDER BY " + nulls + order + " " + strings.ToUpper(filter.order) + ", model_name ASC LIMIT ? OFFSET ?"
	args = append(args, filter.pageSize, (filter.page-1)*filter.pageSize)
	rows, err := tx.QueryContext(ctx, bindQuery(query.driver, statement), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var model modelLogStatistics
		if err := rows.Scan(&model.ModelName, &model.TotalRequests, &model.SuccessCount, &model.FailureCount,
			&model.KnownEmptyCount, &model.EmptyUnknownCount); err != nil {
			return err
		}
		model.finish(data.TokensPresent)
		model.Health = model.health()
		data.Models = append(data.Models, model)
	}
	return rows.Err()
}

func (data *modelLogAnalysisData) readTrend(ctx context.Context, tx *sql.Tx, query modelLogQuery) error {
	var bucket strings.Builder
	var args []any
	bucket.WriteString("CASE")
	for index, point := range data.Trend[1:] {
		bucket.WriteString(" WHEN created_at < ? THEN " + strconv.Itoa(index))
		args = append(args, unixBoundary(point.At))
	}
	bucket.WriteString(" ELSE " + strconv.Itoa(len(data.Trend)-1) + " END")
	empty, unknown := query.emptyExpressions()
	statement := "SELECT period_bucket,COUNT(*),SUM(CASE WHEN type = 2 THEN 1 ELSE 0 END), " +
		"SUM(CASE WHEN type = 5 THEN 1 ELSE 0 END),SUM(empty_value),SUM(unknown_value) " +
		"FROM (SELECT " + bucket.String() + " AS period_bucket,type," + empty + " AS empty_value," +
		unknown + " AS unknown_value FROM logs" + query.where + ") AS bucketed GROUP BY period_bucket"
	args = append(args, query.args...)
	rows, err := tx.QueryContext(ctx, bindQuery(query.driver, statement), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var index int
		var counts modelLogCounts
		if err := rows.Scan(&index, &counts.TotalRequests, &counts.SuccessCount, &counts.FailureCount,
			&counts.KnownEmptyCount, &counts.EmptyUnknownCount); err != nil {
			return err
		}
		if index < 0 || index >= len(data.Trend) {
			return errors.New("invalid model statistics time bucket")
		}
		data.Trend[index].modelLogCounts = counts
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for index := range data.Trend {
		data.Trend[index].finish(data.TokensPresent)
	}
	return nil
}

func readModelLogUserRanking(ctx context.Context, tx *sql.Tx, query modelLogQuery, byQuota bool) ([]logUserRank, error) {
	quota, username := "0", "''"
	if query.has["quota"] {
		quota = "COALESCE(quota,0)"
	}
	if query.has["username"] {
		username = "COALESCE(latest.username,'')"
	}
	metric, having := "request_count", ""
	if byQuota {
		metric, having = "quota_used", " HAVING SUM(CASE WHEN type = 2 THEN "+quota+" ELSE 0 END) > 0"
	}
	statement := "SELECT ranked.user_id," + username + ",ranked.request_count,ranked.quota_used FROM " +
		"(SELECT user_id,MAX(id) AS latest_id,COUNT(*) AS request_count," +
		"SUM(CASE WHEN type = 2 THEN " + quota + " ELSE 0 END) AS quota_used FROM logs" + query.where +
		" AND user_id > 0 GROUP BY user_id" + having + " ORDER BY " + metric +
		" DESC,user_id ASC LIMIT 10) AS ranked JOIN logs latest ON latest.id = ranked.latest_id " +
		"ORDER BY ranked." + metric + " DESC,ranked.user_id ASC"
	rows, err := tx.QueryContext(ctx, bindQuery(query.driver, statement), query.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []logUserRank{}
	for rows.Next() {
		var user logUserRank
		if err := rows.Scan(&user.UserID, &user.Username, &user.Count, &user.Quota); err != nil {
			return nil, err
		}
		result = append(result, user)
	}
	return result, rows.Err()
}

func (s *dashboardService) readDemoModelLogAnalysis(ctx context.Context, data *modelLogAnalysisData, filter modelLogFilters, location *time.Location) error {
	if s.network == nil {
		return errors.New("demo observations are unavailable")
	}
	events, err := s.network.Events(ctx, networkSourceCall, unixBoundary(data.Range.Start), unixBoundary(data.Range.End))
	if err != nil {
		return err
	}
	users, err := s.managedUsers(ctx, 0)
	if err != nil {
		return err
	}
	names := make(map[int64]string, len(users))
	for _, user := range users {
		names[user.ID] = user.Username
	}
	data.EmptyDetectionAvailable, data.TokensPresent = true, true
	models, ranks := map[string]*modelLogStatistics{}, map[int64]*logUserRank{}
	modelNeedle := strings.ToLower(filter.model)
	for _, event := range events {
		if err := ctx.Err(); err != nil {
			return err
		}
		record := demoCallRecord(event, location)
		// The demo projection may supply a convenience fallback elsewhere. This
		// analysis retains a missing model name as its own explicit group.
		name := event.Metadata["model"]
		if filter.model != "" && !strings.Contains(strings.ToLower(name), modelNeedle) ||
			filter.channelID > 0 && record.ChannelID != filter.channelID {
			continue
		}
		if event.Result != "success" && event.Result != "failure" {
			continue
		}
		model := models[name]
		if model == nil {
			model = &modelLogStatistics{ModelName: name}
			models[name] = model
		}
		bucket := sort.Search(len(data.Trend), func(index int) bool { return data.Trend[index].At.After(record.CreatedAt) }) - 1
		if bucket < 0 || bucket >= len(data.Trend) {
			continue
		}
		targets := []*modelLogCounts{&data.Summary.modelLogCounts, &model.modelLogCounts, &data.Trend[bucket].modelLogCounts}
		for _, target := range targets {
			target.TotalRequests++
			if record.Status == "error" {
				target.FailureCount++
			} else {
				target.SuccessCount++
				rawTokens, known := event.Metadata["completion_tokens"]
				tokens, parseErr := strconv.ParseInt(rawTokens, 10, 64)
				if !known || parseErr != nil || tokens < 0 {
					target.EmptyUnknownCount++
				} else if tokens == 0 {
					target.KnownEmptyCount++
				}
			}
		}
		if event.UserID > 0 {
			user := ranks[event.UserID]
			if user == nil {
				user = &logUserRank{UserID: event.UserID, Username: names[event.UserID]}
				ranks[event.UserID] = user
			}
			user.Count++
			if record.Status != "error" {
				user.Quota += record.Quota
			}
		}
	}
	data.Summary.ModelCount = int64(len(models))
	for _, model := range models {
		model.finish(true)
		model.Health = model.health()
		switch model.Health {
		case "healthy":
			data.Summary.HealthyModels++
		case "warning":
			data.Summary.WarningModels++
		case "critical":
			data.Summary.CriticalModels++
		default:
			data.Summary.UnknownModels++
		}
		if filter.health == "all" || model.Health == filter.health {
			data.Models = append(data.Models, *model)
		}
	}
	data.Summary.finish(true)
	for index := range data.Trend {
		data.Trend[index].finish(true)
	}
	sort.Slice(data.Models, func(left, right int) bool {
		a, b := data.Models[left], data.Models[right]
		var metricA, metricB float64
		switch filter.sort {
		case "success_rate":
			metricA, metricB = *a.SuccessRate, *b.SuccessRate
		case "empty_rate":
			if (a.EmptyRate == nil) != (b.EmptyRate == nil) {
				return a.EmptyRate != nil
			}
			if a.EmptyRate != nil {
				metricA, metricB = *a.EmptyRate, *b.EmptyRate
			}
		case "failure_count":
			metricA, metricB = float64(a.FailureCount), float64(b.FailureCount)
		default:
			metricA, metricB = float64(a.TotalRequests), float64(b.TotalRequests)
		}
		if metricA == metricB {
			return a.ModelName < b.ModelName
		}
		if filter.order == "asc" {
			return metricA < metricB
		}
		return metricA > metricB
	})
	data.TotalModels = int64(len(data.Models))
	offset := (filter.page - 1) * filter.pageSize
	if offset >= len(data.Models) {
		data.Models = []modelLogStatistics{}
	} else {
		data.Models = data.Models[offset:min(len(data.Models), offset+filter.pageSize)]
	}
	for _, user := range ranks {
		data.Rankings.Requests = append(data.Rankings.Requests, *user)
		if user.Quota > 0 {
			data.Rankings.Quota = append(data.Rankings.Quota, *user)
		}
	}
	sort.Slice(data.Rankings.Requests, func(a, b int) bool {
		left, right := data.Rankings.Requests[a], data.Rankings.Requests[b]
		return left.Count > right.Count || left.Count == right.Count && left.UserID < right.UserID
	})
	sort.Slice(data.Rankings.Quota, func(a, b int) bool {
		left, right := data.Rankings.Quota[a], data.Rankings.Quota[b]
		return left.Quota > right.Quota || left.Quota == right.Quota && left.UserID < right.UserID
	})
	data.Rankings.Requests = data.Rankings.Requests[:min(len(data.Rankings.Requests), logRankingLimit)]
	data.Rankings.Quota = data.Rankings.Quota[:min(len(data.Rankings.Quota), logRankingLimit)]
	return nil
}
