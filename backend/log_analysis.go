package main

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

type logAnalysisFilters struct {
	period    Period
	status    string
	model     string
	channelID int64
	requestID string
	page      int
	pageSize  int
}

type logAnalysisRecord struct {
	ID               int64     `json:"id"`
	CreatedAt        time.Time `json:"created_at"`
	Status           string    `json:"status"`
	Model            string    `json:"model"`
	ChannelID        int64     `json:"channel_id"`
	RequestID        string    `json:"request_id"`
	Quota            int64     `json:"quota"`
	PromptTokens     int64     `json:"prompt_tokens"`
	CompletionTokens int64     `json:"completion_tokens"`
	DurationSeconds  int64     `json:"duration_seconds"`
}

type logAnalysisSummary struct {
	Total           int64    `json:"total"`
	Success         int64    `json:"success"`
	Errors          int64    `json:"errors"`
	SuccessRate     *float64 `json:"success_rate"`
	AverageDuration *float64 `json:"avg_duration_seconds"`
}

const logRankingLimit = 10

type logUserRank struct {
	UserID   int64  `json:"user_id"`
	Username string `json:"username"`
	Count    int64  `json:"count"`
	Quota    int64  `json:"quota"`
}

type logModelRank struct {
	Model  string `json:"model"`
	Count  int64  `json:"count"`
	Errors int64  `json:"errors"`
	Quota  int64  `json:"quota"`
}

type logRankings struct {
	Requests []logUserRank  `json:"requests"`
	Quota    []logUserRank  `json:"quota"`
	Models   []logModelRank `json:"models"`
}

type logAnalysisData struct {
	Mode         string              `json:"mode"`
	Period       Period              `json:"period"`
	Timezone     string              `json:"timezone"`
	Range        TimeRange           `json:"range"`
	GeneratedAt  time.Time           `json:"generated_at"`
	QuotaPerUnit float64             `json:"quota_per_unit"`
	Summary      logAnalysisSummary  `json:"summary"`
	Trend        []TrendPoint        `json:"trend"`
	Rankings     logRankings         `json:"rankings"`
	Records      []logAnalysisRecord `json:"records"`
	Total        int64               `json:"total"`
	Page         int                 `json:"page"`
	PageSize     int                 `json:"page_size"`
}

func positiveLogInt(raw string, fallback, max int) (int, error) {
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > max {
		return 0, fmt.Errorf("请输入 1 至 %d 的整数", max)
	}
	return value, nil
}

func parseLogAnalysisFilters(c *gin.Context) (logAnalysisFilters, error) {
	f := logAnalysisFilters{
		period:    Period(c.DefaultQuery("period", string(Period24Hours))),
		status:    c.DefaultQuery("status", "all"),
		model:     strings.TrimSpace(c.Query("model")),
		requestID: strings.TrimSpace(c.Query("request_id")),
	}
	if _, err := periodDuration(f.period); err != nil {
		return f, errors.New("时间范围须为 24 小时、7 天或 30 天")
	}
	switch f.status {
	case "all", "success", "error":
	default:
		return f, errors.New("不支持的请求状态")
	}
	if !validLogFilter(f.model, 255) || !validLogFilter(f.requestID, 64) {
		return f, errors.New("模型名或请求 ID 格式无效")
	}
	if raw := c.Query("channel_id"); raw != "" {
		channelID, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || channelID < 1 {
			return f, errors.New("渠道 ID 须为正整数")
		}
		f.channelID = channelID
	}
	var err error
	if f.page, err = positiveLogInt(c.Query("page"), 1, 2147483647); err != nil {
		return f, fmt.Errorf("页码无效：%w", err)
	}
	if f.pageSize, err = positiveLogInt(c.Query("page_size"), 20, 100); err != nil {
		return f, fmt.Errorf("每页数量无效：%w", err)
	}
	return f, nil
}

func validLogFilter(value string, max int) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= max && !strings.ContainsRune(value, 0)
}

func (s *dashboardService) logAnalysisHTTP(c *gin.Context) {
	f, err := parseLogAnalysisFilters(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	window, _, points, err := dashboardRanges(s.now(), f.period, s.config.Location)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	data := logAnalysisData{
		Mode: s.config.Mode, Period: f.period, Timezone: s.config.Timezone, Range: window,
		GeneratedAt: window.End, QuotaPerUnit: s.config.QuotaPerUnit,
		Trend: points, Records: []logAnalysisRecord{}, Page: f.page, PageSize: f.pageSize,
		Rankings: logRankings{Requests: []logUserRank{}, Quota: []logUserRank{}, Models: []logModelRank{}},
	}
	if s.config.Mode == "demo" {
		if err := s.readDemoLogAnalysis(c.Request.Context(), &data, f); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "演示调用数据暂不可用"})
			return
		}
	} else {
		ctx, cancel := context.WithTimeout(c.Request.Context(), queryTimeout)
		defer cancel()
		if err := data.read(ctx, s.logs, f); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "暂无法读取请求日志，请检查日志库连接和表结构"})
			return
		}
	}
	c.JSON(http.StatusOK, data)
}

func (f logAnalysisFilters) where(window TimeRange) (string, []any) {
	where := " WHERE created_at >= ? AND created_at < ?"
	args := []any{unixBoundary(window.Start), unixBoundary(window.End)}
	switch f.status {
	case "success":
		where += " AND type = ?"
		args = append(args, logConsume)
	case "error":
		where += " AND type = ?"
		args = append(args, logError)
	default:
		where += " AND type IN (?, ?)"
		args = append(args, logConsume, logError)
	}
	if f.model != "" {
		where += " AND model_name = ?"
		args = append(args, f.model)
	}
	if f.channelID > 0 {
		where += " AND channel = ?"
		args = append(args, f.channelID)
	}
	if f.requestID != "" {
		where += " AND request_id = ?"
		args = append(args, f.requestID)
	}
	return where, args
}

func (data *logAnalysisData) finish(durationTotal, durationSamples int64) {
	for _, p := range data.Trend {
		data.Summary.Total += p.Requests
		data.Summary.Errors += p.Errors
	}
	data.Total = data.Summary.Total
	data.Summary.Success = data.Summary.Total - data.Summary.Errors
	if data.Total > 0 {
		data.Summary.SuccessRate = float64Pointer(100 * float64(data.Summary.Success) / float64(data.Total))
	}
	if durationSamples > 0 {
		data.Summary.AverageDuration = float64Pointer(float64(durationTotal) / float64(durationSamples))
	}
}

func (data *logAnalysisData) offset() int64 {
	return int64(data.Page-1) * int64(data.PageSize)
}

func (data *logAnalysisData) read(ctx context.Context, db queryDB, f logAnalysisFilters) error {
	if db.db == nil {
		return errors.New("log database is not configured")
	}
	// Keep the counts and the page on the same snapshot while new logs are appended.
	tx, err := db.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return err
	}
	defer tx.Rollback()

	where, args := f.where(data.Range)
	if err := data.readTrend(ctx, tx, db.driver, where, args); err != nil {
		return err
	}
	if data.Total > 0 {
		if err := data.readRankings(ctx, tx, db.driver, where, args); err != nil {
			return err
		}
	}
	if data.offset() < data.Total {
		if err := data.readPage(ctx, tx, db.driver, where, args); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (data *logAnalysisData) readRankings(ctx context.Context, tx *sql.Tx, driver, where string, whereArgs []any) error {
	requests, err := readLogUserRanking(ctx, tx, driver, where, whereArgs, false)
	if err != nil {
		return err
	}
	data.Rankings.Requests = requests
	if data.Summary.Success > 0 {
		quota, err := readLogUserRanking(ctx, tx, driver, where, whereArgs, true)
		if err != nil {
			return err
		}
		data.Rankings.Quota = quota
	}
	query := `SELECT model_name, COUNT(*), COUNT(CASE WHEN type = ? THEN 1 END),
		COALESCE(SUM(CASE WHEN type = ? THEN quota END), 0)
		FROM logs` + where + ` AND model_name <> ''
		GROUP BY model_name ORDER BY COUNT(*) DESC, model_name ASC LIMIT ?`
	args := append([]any{logError, logConsume}, whereArgs...)
	rows, err := tx.QueryContext(ctx, bindQuery(driver, query), append(args, logRankingLimit)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var entry logModelRank
		if err := rows.Scan(&entry.Model, &entry.Count, &entry.Errors, &entry.Quota); err != nil {
			return err
		}
		data.Rankings.Models = append(data.Rankings.Models, entry)
	}
	return rows.Err()
}

func readLogUserRanking(ctx context.Context, tx *sql.Tx, driver, where string, whereArgs []any, byQuota bool) ([]logUserRank, error) {
	metric := "request_count"
	having := ""
	if byQuota {
		metric = "quota_used"
		having = " HAVING COALESCE(SUM(CASE WHEN type = ? THEN quota END), 0) > 0"
	}
	// Rank by user ID, then display the name on that user's latest matching log.
	order := " ORDER BY " + metric + " DESC, user_id ASC"
	query := `SELECT ranked.user_id, COALESCE(latest.username, ''), ranked.request_count, ranked.quota_used
		FROM (SELECT user_id, MAX(id) AS latest_id, COUNT(*) AS request_count,
			COALESCE(SUM(CASE WHEN type = ? THEN quota END), 0) AS quota_used
			FROM logs` + where + ` AND user_id > 0
			GROUP BY user_id` + having + order + ` LIMIT ?) AS ranked
		JOIN logs AS latest ON latest.id = ranked.latest_id
		ORDER BY ranked.` + metric + " DESC, ranked.user_id ASC"
	args := append([]any{logConsume}, whereArgs...)
	if byQuota {
		args = append(args, logConsume)
	}
	rows, err := tx.QueryContext(ctx, bindQuery(driver, query), append(args, logRankingLimit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []logUserRank{}
	for rows.Next() {
		var entry logUserRank
		if err := rows.Scan(&entry.UserID, &entry.Username, &entry.Count, &entry.Quota); err != nil {
			return nil, err
		}
		result = append(result, entry)
	}
	return result, rows.Err()
}

func (data *logAnalysisData) readTrend(ctx context.Context, tx *sql.Tx, driver, where string, whereArgs []any) error {
	var buckets strings.Builder
	args := []any{logError, logConsume, logConsume, logConsume}
	if len(data.Trend) == 1 {
		buckets.WriteString("0")
	} else {
		buckets.WriteString("CASE")
		for i, point := range data.Trend[1:] {
			fmt.Fprintf(&buckets, " WHEN created_at < ? THEN %d", i)
			args = append(args, unixBoundary(point.At))
		}
		fmt.Fprintf(&buckets, " ELSE %d END", len(data.Trend)-1)
	}
	query := `SELECT period_bucket, COUNT(*), COUNT(CASE WHEN type = ? THEN 1 END),
		COALESCE(SUM(CASE WHEN type = ? THEN quota END), 0),
		COALESCE(SUM(CASE WHEN type = ? THEN use_time END), 0),
		COUNT(CASE WHEN type = ? THEN use_time END)
		FROM (SELECT ` + buckets.String() + ` AS period_bucket, type, quota, use_time
			FROM logs` + where + `) AS bucketed GROUP BY period_bucket`
	rows, err := tx.QueryContext(ctx, bindQuery(driver, query), append(args, whereArgs...)...)
	if err != nil {
		return err
	}
	defer rows.Close()

	var durationTotal, durationSamples int64
	for rows.Next() {
		var index int
		var count, failures, quota, duration, samples int64
		if err := rows.Scan(&index, &count, &failures, &quota, &duration, &samples); err != nil {
			return err
		}
		if index < 0 || index >= len(data.Trend) {
			return errors.New("invalid log trend bucket")
		}
		data.Trend[index].Requests = count
		data.Trend[index].Errors = failures
		data.Trend[index].Quota = quota
		durationTotal += duration
		durationSamples += samples
	}
	if err := rows.Err(); err != nil {
		return err
	}
	data.finish(durationTotal, durationSamples)
	return nil
}

func (data *logAnalysisData) readPage(ctx context.Context, tx *sql.Tx, driver, where string, args []any) error {
	// Only return structured operational fields. Raw log content and metadata may contain secrets.
	query := `SELECT id, created_at, type, COALESCE(model_name, ''), COALESCE(channel, 0),
		COALESCE(request_id, ''), COALESCE(quota, 0), COALESCE(prompt_tokens, 0),
		COALESCE(completion_tokens, 0), COALESCE(use_time, 0)
		FROM logs` + where + " ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?"
	rows, err := tx.QueryContext(ctx, bindQuery(driver, query), append(args, data.PageSize, data.offset())...)
	if err != nil {
		return err
	}
	defer rows.Close()
	location := data.Range.End.Location()
	for rows.Next() {
		var r logAnalysisRecord
		var at int64
		var logType int
		if err := rows.Scan(&r.ID, &at, &logType, &r.Model, &r.ChannelID, &r.RequestID,
			&r.Quota, &r.PromptTokens, &r.CompletionTokens, &r.DurationSeconds); err != nil {
			return err
		}
		r.CreatedAt = time.Unix(at, 0).In(location)
		r.Status = "success"
		if logType == logError {
			r.Status = "error"
		}
		data.Records = append(data.Records, r)
	}
	return rows.Err()
}

func (f logAnalysisFilters) matches(r logAnalysisRecord) bool {
	return (f.status == "all" || f.status == r.Status) &&
		(f.model == "" || f.model == r.Model) &&
		(f.channelID == 0 || f.channelID == r.ChannelID) &&
		(f.requestID == "" || f.requestID == r.RequestID)
}

func (data *logAnalysisData) demo(f logAnalysisFilters) {
	const sampleInterval = 15 * time.Minute
	models := [...]string{"gpt-5-mini", "claude-sonnet-4", "gemini-2.5-flash", "deepseek-v3.2"}
	users := [...]string{"晨星", "北辰", "木棉", "未央", "山雀", "清流", "风铃", "牧云", "青禾", "映山", "知微", "南舟"}
	userStats := make([]logUserRank, len(users))
	modelStats := make(map[string]logModelRank, len(models))
	var matched, durationTotal, durationSamples int64
	bucket := len(data.Trend) - 1
	for at := data.Range.End.Truncate(sampleInterval); !at.Before(data.Range.Start); at = at.Add(-sampleInterval) {
		if !at.Before(data.Range.End) {
			continue
		}
		slot := at.Unix() / int64(sampleInterval/time.Second)
		// Vary traffic by hour while keeping each sample stable across overlapping queries.
		samples := 1 + at.Hour()/5
		for i := samples - 1; i >= 0; i-- {
			id := slot*8 + int64(i)
			r := logAnalysisRecord{
				ID: id, CreatedAt: at, Status: "success",
				Model: models[(slot+int64(i))%int64(len(models))], ChannelID: 1 + id%3,
				RequestID: fmt.Sprintf("demo-%d", id), DurationSeconds: 1 + id%9,
			}
			if id%11 == 0 || (at.Hour()%7 == 0 && i == 0) {
				r.Status = "error"
			} else {
				r.PromptTokens = 100 + id%3200
				r.CompletionTokens = 25 + id%780
				r.Quota = 2*r.PromptTokens + 6*r.CompletionTokens
			}
			if !f.matches(r) {
				continue
			}
			for bucket > 0 && at.Before(data.Trend[bucket].At) {
				bucket--
			}
			point := &data.Trend[bucket]
			point.Requests++
			// Split traffic unevenly across stable, synthetic accounts.
			userSlot := int((slot + int64(i*7)) % 78)
			userIndex := 0
			for weight := len(users); userSlot >= weight; weight-- {
				userSlot -= weight
				userIndex++
			}
			user := &userStats[userIndex]
			user.UserID = int64(100 + userIndex)
			user.Username = users[userIndex]
			user.Count++
			model := modelStats[r.Model]
			model.Model = r.Model
			model.Count++
			if r.Status == "error" {
				point.Errors++
				model.Errors++
			} else {
				point.Quota += r.Quota
				user.Quota += r.Quota
				model.Quota += r.Quota
				durationTotal += r.DurationSeconds
				durationSamples++
			}
			modelStats[r.Model] = model
			if matched >= data.offset() && len(data.Records) < data.PageSize {
				data.Records = append(data.Records, r)
			}
			matched++
		}
	}
	data.finish(durationTotal, durationSamples)
	for _, user := range userStats {
		if user.Count > 0 {
			data.Rankings.Requests = append(data.Rankings.Requests, user)
		}
		if user.Quota > 0 {
			data.Rankings.Quota = append(data.Rankings.Quota, user)
		}
	}
	for _, model := range modelStats {
		data.Rankings.Models = append(data.Rankings.Models, model)
	}
	slices.SortFunc(data.Rankings.Requests, func(a, b logUserRank) int {
		if a.Count != b.Count {
			return cmp.Compare(b.Count, a.Count)
		}
		return cmp.Compare(a.UserID, b.UserID)
	})
	slices.SortFunc(data.Rankings.Quota, func(a, b logUserRank) int {
		if a.Quota != b.Quota {
			return cmp.Compare(b.Quota, a.Quota)
		}
		return cmp.Compare(a.UserID, b.UserID)
	})
	slices.SortFunc(data.Rankings.Models, func(a, b logModelRank) int {
		if a.Count != b.Count {
			return cmp.Compare(b.Count, a.Count)
		}
		return strings.Compare(a.Model, b.Model)
	})
	data.Rankings.Requests = data.Rankings.Requests[:min(len(data.Rankings.Requests), logRankingLimit)]
	data.Rankings.Quota = data.Rankings.Quota[:min(len(data.Rankings.Quota), logRankingLimit)]
}
