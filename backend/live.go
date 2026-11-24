package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const (
	logConsume    = 2
	logError      = 5
	enabledStatus = 1
	nonExpiring   = -1
)

type inventoryResult struct {
	inventory Inventory
	status    SourceStatus
	warnings  []string
}

type logsResult struct {
	current, previous UsageStats
	trend             []TrendPoint
	models            []ModelUsage
	status            SourceStatus
	warnings          []string
}

func (s *dashboardService) live(ctx context.Context, data DashboardData, previous TimeRange, points []TrendPoint) (DashboardData, error) {
	mainResult := make(chan inventoryResult, 1)
	logResult := make(chan logsResult, 1)
	go func() {
		mainResult <- readInventory(ctx, s.main, data.GeneratedAt)
	}()
	go func() {
		logResult <- readLogs(ctx, s.logs, s.separateLogs, data.Range, previous, points)
	}()
	resources, logs := <-mainResult, <-logResult
	if err := ctx.Err(); err != nil {
		return DashboardData{}, err
	}
	data.Inventory = resources.inventory
	data.Usage, data.PreviousUsage = logs.current, logs.previous
	data.Trend, data.Models = logs.trend, logs.models
	data.Sources = []SourceStatus{resources.status, logs.status}
	data.Warnings = append(data.Warnings, resources.warnings...)
	data.Warnings = append(data.Warnings, logs.warnings...)
	if data.Warnings == nil {
		data.Warnings = []string{}
	}
	return data, nil
}

func readInventory(parent context.Context, db queryDB, now time.Time) inventoryResult {
	result := inventoryResult{status: SourceStatus{ID: "main", Name: "NewAPI 主库", Status: "ok", Message: "已读取用户、令牌、渠道和模型资料"}}
	if db.db == nil {
		result.status.Status = "error"
		result.status.Message = "主库连接不可用"
		result.warnings = []string{"主库不可用，资源规模暂无法统计。"}
		return result
	}

	ctx, cancel := context.WithTimeout(parent, queryTimeout)
	defer cancel()
	markFailure := func(message string) {
		result.status.Status = "error"
		result.status.Message = "部分或全部资源指标暂不可用"
		result.warnings = append(result.warnings, message)
	}

	var total, active int64
	if err := db.queryRow(ctx, `SELECT COUNT(*) FROM users WHERE deleted_at IS NULL`).Scan(&total); err != nil {
		markFailure("主库用户数量暂无法读取。")
	} else {
		result.inventory.TotalUsers = int64Pointer(total)
	}
	if err := db.queryRow(ctx, `SELECT COUNT(*), COUNT(CASE WHEN status = ? AND (expired_time = ? OR expired_time >= ?) THEN 1 END) FROM tokens WHERE deleted_at IS NULL`, enabledStatus, nonExpiring, now.Unix()).Scan(&total, &active); err != nil {
		markFailure("主库令牌数量暂无法读取。")
	} else {
		result.inventory.TotalTokens, result.inventory.ActiveTokens = int64Pointer(total), int64Pointer(active)
	}
	if err := db.queryRow(ctx, `SELECT COUNT(*), COUNT(CASE WHEN status = ? THEN 1 END) FROM channels`, enabledStatus).Scan(&total, &active); err != nil {
		markFailure("主库渠道数量暂无法读取。")
	} else {
		result.inventory.TotalChannels, result.inventory.ActiveChannels = int64Pointer(total), int64Pointer(active)
	}
	if err := db.queryRow(ctx, `SELECT COUNT(*) FROM models WHERE deleted_at IS NULL`).Scan(&total); err != nil {
		markFailure("主库模型资料数量暂无法读取。")
	} else {
		result.inventory.TotalModels = int64Pointer(total)
	}
	return result
}

func readLogs(parent context.Context, db queryDB, separate bool, window, previous TimeRange, points []TrendPoint) logsResult {
	result := logsResult{
		status: SourceStatus{ID: "logs", Name: "NewAPI 日志库", Status: "ok", Message: "从主库读取请求日志"},
		trend:  []TrendPoint{},
		models: []ModelUsage{},
	}
	if separate {
		result.status.Message = "从独立日志库读取请求日志"
	}
	if db.db == nil {
		result.status.Status = "error"
		result.status.Message = "日志库连接不可用"
		result.warnings = []string{"日志库不可用，请求、额度和模型用量暂无法统计。"}
		return result
	}

	ctx, cancel := context.WithTimeout(parent, queryTimeout)
	defer cancel()
	markFailure := func(message string) {
		result.status.Status = "error"
		result.status.Message = "部分或全部请求日志指标暂不可用"
		result.warnings = append(result.warnings, message)
	}
	var err error
	if result.current, err = readUsage(ctx, db, window); err != nil {
		markFailure("本期请求统计暂无法读取。")
	}
	if result.previous, err = readUsage(ctx, db, previous); err != nil {
		markFailure("上一时段请求统计暂无法读取。")
	}
	if trend, err := readTrend(ctx, db, window, points); err != nil {
		markFailure("请求趋势暂无法读取。")
	} else {
		result.trend = trend
	}
	if models, err := readTopModels(ctx, db, window); err != nil {
		markFailure("模型用量排行暂无法读取。")
	} else {
		result.models = models
	}
	return result
}

func readUsage(ctx context.Context, db queryDB, window TimeRange) (UsageStats, error) {
	var requests, successes, quota, prompt, completion, users int64
	var duration sql.NullFloat64
	err := db.queryRow(ctx, `SELECT
		COUNT(*), COUNT(CASE WHEN type = ? THEN 1 END),
		COALESCE(SUM(CASE WHEN type = ? THEN quota END), 0),
		COALESCE(SUM(CASE WHEN type = ? THEN prompt_tokens END), 0),
		COALESCE(SUM(CASE WHEN type = ? THEN completion_tokens END), 0),
		COUNT(DISTINCT CASE WHEN user_id > 0 THEN user_id END),
		AVG(CASE WHEN type = ? THEN use_time END)
		FROM logs WHERE created_at >= ? AND created_at < ? AND type IN (?, ?)`,
		logConsume, logConsume, logConsume, logConsume, logConsume,
		unixBoundary(window.Start), unixBoundary(window.End), logConsume, logError,
	).Scan(&requests, &successes, &quota, &prompt, &completion, &users, &duration)
	if err != nil {
		return UsageStats{}, err
	}
	usage := UsageStats{
		Requests: int64Pointer(requests), SuccessfulRequests: int64Pointer(successes), FailedRequests: int64Pointer(requests - successes),
		Quota: int64Pointer(quota), PromptTokens: int64Pointer(prompt), CompletionTokens: int64Pointer(completion), ActiveUsers: int64Pointer(users),
	}
	if requests > 0 {
		usage.SuccessRate = float64Pointer(100 * float64(successes) / float64(requests))
	}
	if duration.Valid {
		usage.AvgDurationSeconds = float64Pointer(duration.Float64)
	}
	return usage, nil
}

func readTrend(ctx context.Context, db queryDB, window TimeRange, points []TrendPoint) ([]TrendPoint, error) {
	if len(points) == 0 {
		return []TrendPoint{}, nil
	}
	// Compare Unix seconds to the caller's timezone-aware bucket boundaries.
	// Only the internally generated bucket indices are placed into the SQL text.
	var bucketSQL strings.Builder
	args := make([]any, 0, len(points)+5)
	args = append(args, logError, logConsume)
	if len(points) == 1 {
		bucketSQL.WriteByte('0')
	} else {
		bucketSQL.WriteString("CASE")
		for i, point := range points[1:] {
			fmt.Fprintf(&bucketSQL, " WHEN created_at < ? THEN %d", i)
			args = append(args, unixBoundary(point.At))
		}
		fmt.Fprintf(&bucketSQL, " ELSE %d END", len(points)-1)
	}
	args = append(args, unixBoundary(window.Start), unixBoundary(window.End), logConsume, logError)
	rows, err := db.query(ctx, `SELECT log_bucket, COUNT(*),
		COUNT(CASE WHEN type = ? THEN 1 END), COALESCE(SUM(CASE WHEN type = ? THEN quota END), 0)
		FROM (SELECT `+bucketSQL.String()+` AS log_bucket, type, quota
		FROM logs WHERE created_at >= ? AND created_at < ? AND type IN (?, ?)) AS bucketed
		GROUP BY log_bucket`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	trend := append([]TrendPoint(nil), points...)
	for rows.Next() {
		var bucket int
		var count, errors, quota int64
		if err := rows.Scan(&bucket, &count, &errors, &quota); err != nil {
			return nil, err
		}
		if bucket < 0 || bucket >= len(trend) {
			return nil, fmt.Errorf("unexpected trend bucket index")
		}
		trend[bucket].Requests, trend[bucket].Errors, trend[bucket].Quota = count, errors, quota
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return trend, nil
}

func readTopModels(ctx context.Context, db queryDB, window TimeRange) ([]ModelUsage, error) {
	rows, err := db.query(ctx, `SELECT model_name, COUNT(*), COALESCE(SUM(quota), 0) AS total_quota,
		COALESCE(SUM(prompt_tokens), 0) + COALESCE(SUM(completion_tokens), 0)
		FROM logs WHERE created_at >= ? AND created_at < ? AND type = ? AND model_name <> ''
		GROUP BY model_name ORDER BY total_quota DESC, COUNT(*) DESC, model_name ASC LIMIT 5`,
		unixBoundary(window.Start), unixBoundary(window.End), logConsume,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	models := make([]ModelUsage, 0, 5)
	for rows.Next() {
		var model ModelUsage
		if err := rows.Scan(&model.Model, &model.Requests, &model.Quota, &model.Tokens); err != nil {
			return nil, err
		}
		models = append(models, model)
	}
	return models, rows.Err()
}
