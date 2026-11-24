package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

const taskKnownStatusesSQL = "'SUCCESS', 'FAILURE', 'IN_PROGRESS', 'QUEUED', 'SUBMITTED', 'NOT_START'"
const taskMilliseconds = 100_000_000_000

type taskLogFilter struct {
	period   Period
	status   string
	platform string
	taskID   string
	user     string
	userID   int64
	page     int
	pageSize int
}

type taskLogItem struct {
	ID         int64  `json:"id"`
	TaskID     string `json:"task_id"`
	Platform   string `json:"platform"`
	Action     string `json:"action"`
	UserID     int64  `json:"user_id"`
	Username   string `json:"username"`
	ChannelID  int64  `json:"channel_id"`
	Status     string `json:"status"`
	Progress   string `json:"progress"`
	Quota      int64  `json:"quota"`
	SubmitTime int64  `json:"submit_time"`
	StartTime  int64  `json:"start_time"`
	FinishTime int64  `json:"finish_time"`
}

type taskLogSummary struct {
	Total      int64 `json:"total"`
	Succeeded  int64 `json:"succeeded"`
	Failed     int64 `json:"failed"`
	InProgress int64 `json:"in_progress"`
	Pending    int64 `json:"pending"`
	Unknown    int64 `json:"unknown"`
	Quota      int64 `json:"quota"`
}

type taskLogData struct {
	Mode         string         `json:"mode"`
	Period       Period         `json:"period"`
	Timezone     string         `json:"timezone"`
	QuotaPerUnit float64        `json:"quota_per_unit"`
	Range        TimeRange      `json:"range"`
	Summary      taskLogSummary `json:"summary"`
	Items        []taskLogItem  `json:"items"`
	Total        int64          `json:"total"`
	Page         int            `json:"page"`
	PageSize     int            `json:"page_size"`
	TotalPages   int64          `json:"total_pages"`
}

func (s *dashboardService) taskLogsHTTP(c *gin.Context) {
	filter, err := parseTaskLogFilter(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	duration, _ := periodDuration(filter.period)
	end := s.now().In(s.config.Location)
	data := taskLogData{
		Mode: s.config.Mode, Period: filter.period, Timezone: s.config.Timezone, QuotaPerUnit: s.config.QuotaPerUnit,
		Range: TimeRange{Start: end.Add(-duration), End: end},
		Items: []taskLogItem{}, Page: filter.page, PageSize: filter.pageSize,
	}
	if s.config.Mode == "demo" {
		users, err := s.managedUsers(c.Request.Context(), 0)
		if err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "演示任务用户暂不可用"})
			return
		}
		names := make(map[int64]string, len(users))
		for _, user := range users {
			names[user.ID] = user.Username
		}
		readDemoTaskLogs(&data, filter, names)
	} else {
		ctx, cancel := context.WithTimeout(c.Request.Context(), queryTimeout)
		defer cancel()
		if err := readTaskLogs(ctx, s.main, &data, filter); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "暂时无法读取任务日志，请检查 New API 主库连接和 tasks 表的读取权限"})
			return
		}
	}
	c.JSON(http.StatusOK, data)
}

func parseTaskLogFilter(c *gin.Context) (taskLogFilter, error) {
	filter := taskLogFilter{
		period:   Period(c.DefaultQuery("period", string(Period24Hours))),
		status:   strings.ToUpper(strings.TrimSpace(c.Query("status"))),
		platform: strings.TrimSpace(c.Query("platform")),
		taskID:   strings.TrimSpace(c.Query("task_id")),
		user:     strings.TrimSpace(c.Query("user")),
	}
	if _, err := periodDuration(filter.period); err != nil {
		return filter, fmt.Errorf("统计周期必须是 24h、7d 或 30d")
	}
	if filter.status != "" && filter.status != "UNKNOWN" && normalizedTaskStatus(filter.status) != filter.status {
		return filter, fmt.Errorf("不支持该任务状态")
	}
	if !utf8.ValidString(filter.platform) || utf8.RuneCountInString(filter.platform) > 30 ||
		!utf8.ValidString(filter.taskID) || utf8.RuneCountInString(filter.taskID) > 191 ||
		strings.ContainsRune(filter.platform, 0) || strings.ContainsRune(filter.taskID, 0) {
		return filter, fmt.Errorf("平台或任务 ID 格式无效")
	}
	if !utf8.ValidString(filter.user) || utf8.RuneCountInString(filter.user) > 64 || strings.ContainsRune(filter.user, 0) {
		return filter, fmt.Errorf("用户搜索格式无效")
	}
	if id, err := strconv.ParseInt(filter.user, 10, 64); err == nil && id > 0 {
		filter.userID = id
	}
	page, err := strconv.ParseInt(c.DefaultQuery("page", "1"), 10, 32)
	if err != nil || page < 1 {
		return filter, fmt.Errorf("页码必须是正整数")
	}
	size, err := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if err != nil || size < 1 || size > 100 {
		return filter, fmt.Errorf("每页条数必须介于 1 和 100 之间")
	}
	filter.page, filter.pageSize = int(page), size
	return filter, nil
}

func normalizedTaskStatus(status string) string {
	switch status {
	case "SUCCESS", "FAILURE", "IN_PROGRESS", "QUEUED", "SUBMITTED", "NOT_START":
		return status
	default:
		return "UNKNOWN"
	}
}

func (summary *taskLogSummary) add(status string, count int64) {
	summary.Total += count
	switch status {
	case "SUCCESS":
		summary.Succeeded += count
	case "FAILURE":
		summary.Failed += count
	case "IN_PROGRESS":
		summary.InProgress += count
	case "NOT_START", "SUBMITTED", "QUEUED":
		summary.Pending += count
	default:
		summary.Unknown += count
	}
}

func (data *taskLogData) reconcilePage() int64 {
	data.Total = data.Summary.Total
	data.TotalPages = max(1, (data.Total-1)/int64(data.PageSize)+1)
	data.Page = int(min(int64(data.Page), data.TotalPages))
	return int64(data.Page-1) * int64(data.PageSize)
}

func taskLogWhere(window TimeRange, filter taskLogFilter) (string, []any) {
	// Query both units using direct comparisons so the submit_time index is usable.
	// For legacy rows lacking submit_time, created_at is the recording time.
	where := "(t.submit_time >= ? AND t.submit_time < ? OR t.submit_time >= ? AND t.submit_time < ? OR " +
		"(t.submit_time IS NULL OR t.submit_time <= 0) AND t.created_at >= ? AND t.created_at < ?)"
	args := []any{unixBoundary(window.Start), unixBoundary(window.End),
		millisBoundary(window.Start), millisBoundary(window.End), unixBoundary(window.Start), unixBoundary(window.End)}
	if filter.status == "UNKNOWN" {
		where += " AND (t.status IS NULL OR UPPER(t.status) NOT IN (" + taskKnownStatusesSQL + "))"
	} else if filter.status != "" {
		where += " AND UPPER(t.status) = ?"
		args = append(args, filter.status)
	}
	if filter.platform != "" {
		where += " AND t.platform = ?"
		args = append(args, filter.platform)
	}
	if filter.taskID != "" {
		where += " AND t.task_id = ?"
		args = append(args, filter.taskID)
	}
	if filter.userID > 0 {
		where += " AND t.user_id = ?"
		args = append(args, filter.userID)
	} else if filter.user != "" {
		where += " AND LOWER(u.username) LIKE ? ESCAPE '!'"
		args = append(args, taskLogContains(strings.ToLower(filter.user)))
	}
	return where, args
}

func millisBoundary(at time.Time) int64 {
	millisecond := at.UnixMilli()
	if at.Nanosecond()%int(time.Millisecond) != 0 {
		millisecond++
	}
	return millisecond
}

func readTaskLogs(ctx context.Context, db queryDB, data *taskLogData, filter taskLogFilter) error {
	// Keep the summary and page consistent while upstream polling updates task states.
	tx, err := db.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	where, args := taskLogWhere(data.Range, filter)
	const tasksWithUser = " FROM tasks t LEFT JOIN users u ON u.id = t.user_id"
	summaryFrom := " FROM tasks t"
	if filter.user != "" && filter.userID == 0 {
		summaryFrom = tasksWithUser
	}
	summary := "SELECT COALESCE(UPPER(t.status), ''), COUNT(*), COALESCE(SUM(t.quota), 0)" +
		summaryFrom + " WHERE " + where + " GROUP BY COALESCE(UPPER(t.status), '')"
	rows, err := tx.QueryContext(ctx, bindQuery(db.driver, summary), args...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var status string
		var count, quota int64
		if err := rows.Scan(&status, &count, &quota); err != nil {
			_ = rows.Close()
			return err
		}
		data.Summary.add(status, count)
		data.Summary.Quota += quota
	}
	if err := rows.Err(); err != nil {
		return err
	}
	offset := data.reconcilePage()
	if data.Total == 0 {
		return tx.Commit()
	}
	query := fmt.Sprintf(`SELECT t.id, COALESCE(t.task_id, ''), COALESCE(t.platform, ''), COALESCE(t.action, ''),
		COALESCE(t.user_id, 0), COALESCE(u.username, ''), COALESCE(t.channel_id, 0), COALESCE(UPPER(t.status), ''), COALESCE(t.progress, ''),
		COALESCE(t.quota, 0), CASE WHEN t.submit_time > 0 THEN t.submit_time ELSE COALESCE(t.created_at, 0) END,
		COALESCE(t.start_time, 0), COALESCE(t.finish_time, 0)%s WHERE %s
		ORDER BY CASE WHEN t.submit_time >= %d THEN t.submit_time
			WHEN t.submit_time > 0 THEN t.submit_time * 1000 ELSE t.created_at * 1000 END DESC, t.id DESC
		LIMIT ? OFFSET ?`, tasksWithUser, where, taskMilliseconds)
	rows, err = tx.QueryContext(ctx, bindQuery(db.driver, query), append(args, data.PageSize, offset)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var item taskLogItem
		if err := rows.Scan(&item.ID, &item.TaskID, &item.Platform, &item.Action, &item.UserID, &item.Username, &item.ChannelID,
			&item.Status, &item.Progress, &item.Quota, &item.SubmitTime, &item.StartTime, &item.FinishTime); err != nil {
			return err
		}
		item.Status = normalizedTaskStatus(item.Status)
		item.Progress = normalizedTaskProgress(item.Progress)
		item.SubmitTime = taskTimeSeconds(item.SubmitTime)
		item.StartTime = taskTimeSeconds(item.StartTime)
		item.FinishTime = taskTimeSeconds(item.FinishTime)
		data.Items = append(data.Items, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return tx.Commit()
}

func taskTimeSeconds(value int64) int64 {
	if value >= taskMilliseconds {
		return value / 1000
	}
	return max(0, value)
}

func normalizedTaskProgress(raw string) string {
	progress, ok := strings.CutSuffix(strings.TrimSpace(raw), "%")
	if !ok {
		return ""
	}
	value, err := strconv.ParseFloat(progress, 64)
	if err != nil || !(value >= 0 && value <= 100) {
		return ""
	}
	return strconv.FormatFloat(value, 'f', -1, 64) + "%"
}

const demoTaskInterval int64 = 18 * 60

func demoTaskLog(id, now int64) taskLogItem {
	type demoTaskPlatform struct{ platform, action string }
	platforms := []demoTaskPlatform{
		{"kling", "text_to_video"}, {"mj", "IMAGINE"}, {"suno", "song"}, {"kling", "image_to_video"},
	}
	statuses := []string{"IN_PROGRESS", "QUEUED", "SUBMITTED", "SUCCESS", "FAILURE", "SUCCESS",
		"SUCCESS", "SUCCESS", "FAILURE", "NOT_START", "UNKNOWN"}
	submit := id * demoTaskInterval
	source := platforms[id%int64(len(platforms))]
	// Only accounts with generated API history own async tasks.
	taskUsers := [...]int64{101, 102, 107, 108, 109, 110, 117, 118, 119, 120, 127, 128,
		129, 130, 137, 138, 139, 140, 147, 148, 149, 150, 153, 157, 158, 159, 160}
	userID := taskUsers[id%int64(len(taskUsers))]
	item := taskLogItem{
		ID: id, TaskID: fmt.Sprintf("demo-task-%d", id), Platform: source.platform, Action: source.action,
		UserID: userID, Username: fmt.Sprintf("demo-user-%d", userID), ChannelID: 11 + id%5,
		Status: statuses[id%int64(len(statuses))], Progress: "0%", SubmitTime: submit,
	}
	switch item.Status {
	case "SUCCESS", "FAILURE":
		item.Progress = "100%"
		item.StartTime = min(now, submit+10)
		item.FinishTime = min(now, submit+30+id%162)
		if item.Status == "SUCCESS" {
			item.Quota = 45000 + (id%13)*5000
		}
	case "IN_PROGRESS":
		item.StartTime = min(now, submit+8)
		item.Progress = fmt.Sprintf("%d%%", 10+id%85)
	}
	return item
}

func readDemoTaskLogs(data *taskLogData, filter taskLogFilter, userNames ...map[int64]string) {
	var matches []taskLogItem
	now := data.Range.End.Unix()
	start, end := unixBoundary(data.Range.Start), unixBoundary(data.Range.End)
	// Align synthetic tasks to absolute 18-minute slots for stable IDs and pagination.
	for id := (end - 1) / demoTaskInterval; id > 0 && id*demoTaskInterval >= start; id-- {
		item := demoTaskLog(id, now)
		if len(userNames) > 0 {
			if username := userNames[0][item.UserID]; username != "" {
				item.Username = username
			}
		}
		if filter.status != "" && item.Status != filter.status ||
			filter.platform != "" && item.Platform != filter.platform ||
			filter.taskID != "" && item.TaskID != filter.taskID ||
			filter.userID > 0 && item.UserID != filter.userID ||
			filter.userID == 0 && !strings.Contains(strings.ToLower(item.Username), strings.ToLower(filter.user)) {
			continue
		}
		data.Summary.add(item.Status, 1)
		data.Summary.Quota += item.Quota
		matches = append(matches, item)
	}
	offset := data.reconcilePage()
	if offset < data.Total {
		data.Items = matches[offset:min(offset+int64(data.PageSize), data.Total)]
	}
}
