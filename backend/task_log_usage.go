package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

const taskUsageMaxCandidates = 1000
const taskUsageMaxMatches = 100
const taskLogRefund = 6

type taskUsageEntry struct {
	ID        int64  `json:"id"`
	CreatedAt int64  `json:"created_at"`
	Kind      string `json:"kind"`
	Model     string `json:"model"`
	RequestID string `json:"request_id"`
	Quota     int64  `json:"quota"`
}

type taskUsageResponse struct {
	TaskID       string           `json:"task_id"`
	Timezone     string           `json:"timezone"`
	QuotaPerUnit float64          `json:"quota_per_unit"`
	Logs         []taskUsageEntry `json:"logs"`
	Truncated    bool             `json:"truncated"`
}

func taskLogContains(value string) string {
	result := "%"
	for _, char := range value {
		if char == '!' || char == '%' || char == '_' {
			result += "!"
		}
		result += string(char)
	}
	return result + "%"
}

func (s *dashboardService) taskRelatedLogsHTTP(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "任务记录 ID 无效"})
		return
	}
	response := taskUsageResponse{Timezone: s.config.Timezone, QuotaPerUnit: s.config.QuotaPerUnit, Logs: []taskUsageEntry{}}
	if s.config.Mode == "demo" {
		now := s.now().Unix()
		if id > now/demoTaskInterval || id < (now-int64(31*24*time.Hour/time.Second))/demoTaskInterval {
			c.JSON(http.StatusNotFound, gin.H{"error": "未找到任务记录"})
			return
		}
		task := demoTaskLog(id, now)
		response.TaskID = task.TaskID
		switch task.Status {
		case "SUCCESS", "IN_PROGRESS", "FAILURE":
			quota := task.Quota
			if quota == 0 {
				quota = 45000
			}
			entry := taskUsageEntry{ID: id * 2, CreatedAt: task.SubmitTime,
				Kind: "consume", Model: task.Platform + "-demo", RequestID: "demo-submit-" + strconv.FormatInt(id, 10), Quota: quota}
			if task.Status == "FAILURE" {
				response.Logs = append(response.Logs, taskUsageEntry{ID: entry.ID + 1, CreatedAt: task.FinishTime,
					Kind: "refund", Model: entry.Model, RequestID: "demo-refund-" + strconv.FormatInt(id, 10), Quota: quota})
			}
			response.Logs = append(response.Logs, entry)
		}
		c.JSON(http.StatusOK, response)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), queryTimeout)
	defer cancel()
	if err := s.readTaskRelatedUsage(ctx, id, &response); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "未找到任务记录"})
		} else {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "暂时无法读取关联使用日志，请检查数据源连接和读取权限"})
		}
		return
	}
	c.JSON(http.StatusOK, response)
}

func (s *dashboardService) readTaskRelatedUsage(ctx context.Context, taskRowID int64, result *taskUsageResponse) error {
	var userID, submittedAt int64
	err := s.main.queryRow(ctx, `SELECT COALESCE(task_id, ''), COALESCE(user_id, 0),
		CASE WHEN submit_time > 0 THEN submit_time ELSE COALESCE(created_at, 0) END
		FROM tasks WHERE id = ?`, taskRowID).Scan(&result.TaskID, &userID, &submittedAt)
	if err != nil {
		return err
	}
	if result.TaskID == "" || userID <= 0 {
		return nil
	}
	// Scope candidates by user and encoded task ID. Parse the JSON field before accepting any match.
	taskJSON, err := json.Marshal(result.TaskID)
	if err != nil {
		return err
	}
	rows, err := s.logs.query(ctx, `SELECT id, created_at, type, COALESCE(model_name, ''), COALESCE(request_id, ''), COALESCE(quota, 0), other
		FROM logs WHERE user_id = ? AND created_at >= ? AND type IN (?, ?, ?)
		AND other LIKE ? ESCAPE '!' AND other LIKE ? ESCAPE '!'
		ORDER BY created_at DESC, id DESC LIMIT ?`,
		userID, max(0, taskTimeSeconds(submittedAt)-120), logConsume, logError, taskLogRefund,
		taskLogContains(`"task_id"`), taskLogContains(string(taskJSON)), taskUsageMaxCandidates+1)
	if err != nil {
		return err
	}
	defer rows.Close()

	for scanned := 1; rows.Next(); scanned++ {
		if scanned > taskUsageMaxCandidates {
			result.Truncated = true
			break
		}
		var entry taskUsageEntry
		var kind int
		var other []byte
		if err := rows.Scan(&entry.ID, &entry.CreatedAt, &kind, &entry.Model, &entry.RequestID, &entry.Quota, &other); err != nil {
			return err
		}
		if !matchesTaskUsage(other, result.TaskID) {
			continue
		}
		if len(result.Logs) == taskUsageMaxMatches {
			result.Truncated = true
			break
		}
		switch kind {
		case logConsume:
			entry.Kind = "consume"
		case logError:
			entry.Kind = "error"
		case taskLogRefund:
			entry.Kind = "refund"
		}
		result.Logs = append(result.Logs, entry)
	}
	return rows.Err()
}

func matchesTaskUsage(other []byte, taskID string) bool {
	var fields map[string]json.RawMessage
	if len(other) == 0 || json.Unmarshal(other, &fields) != nil {
		return false
	}
	var id string
	return json.Unmarshal(fields["task_id"], &id) == nil && id != "" && id == taskID
}
