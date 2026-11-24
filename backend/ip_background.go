package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

type networkSyncJob struct {
	ID                 string `json:"id"`
	State              string `json:"state"`
	Reason             string `json:"reason"`
	Mode               string `json:"mode"`
	StartedAt          int64  `json:"started_at"`
	UpdatedAt          int64  `json:"updated_at"`
	FinishedAt         int64  `json:"finished_at"`
	StartCursor        int64  `json:"start_cursor"`
	Cursor             int64  `json:"cursor"`
	Watermark          int64  `json:"watermark"`
	Imported           int64  `json:"imported"`
	SkippedSettlements int64  `json:"skipped_settlements"`
	Progress           *int   `json:"progress"`
	Error              string `json:"error,omitempty"`
	CheckpointError    string `json:"checkpoint_error,omitempty"`
	WorkerReady        bool   `json:"worker_ready"`
}

type cachedNetworkAnalysis struct {
	key      string
	revision uint64
	at       time.Time
	value    networkAnalysis
}

type networkBackground struct {
	mu          sync.Mutex
	once        sync.Once
	job         networkSyncJob
	wake        chan struct{}
	cancel      context.CancelFunc
	workerReady atomic.Bool
	cacheMu     sync.Mutex
	cache       []cachedNetworkAnalysis
}

func (n *networkStore) syncJobPath() string {
	return filepath.Join(n.service.config.DataDir, "network-sync-job.json")
}

func (n *networkStore) loadSyncJob() {
	n.background.once.Do(func() {
		n.background.mu.Lock()
		defer n.background.mu.Unlock()
		data, err := os.ReadFile(n.syncJobPath())
		if err == nil && len(data) <= 32<<10 {
			_ = json.Unmarshal(data, &n.background.job)
		}
		job := &n.background.job
		if job.Mode != n.service.config.Mode {
			*job = networkSyncJob{State: "idle", Mode: n.service.config.Mode}
		}
		if job.State == "" {
			job.State = "idle"
		}
		if job.State == "running" || job.State == "queued" {
			job.State, job.Error = "interrupted", "上次进程已退出；重新同步将从已提交游标继续。"
		}
	})
}

func (n *networkStore) syncJobStatus() networkSyncJob {
	n.loadSyncJob()
	n.background.mu.Lock()
	defer n.background.mu.Unlock()
	status := n.background.job
	status.WorkerReady = n.background.workerReady.Load()
	return status
}

func (n *networkStore) persistSyncJobLocked() {
	n.background.job.UpdatedAt = n.clock().Unix()
	if err := writePrivateJSON(n.syncJobPath(), n.background.job); err != nil {
		n.background.job.CheckpointError = "任务进度文件保存失败；数据库中已提交的事件与游标仍保持原子提交。"
	} else {
		n.background.job.CheckpointError = ""
	}
}

func (n *networkStore) queueCallSync(reason string) networkSyncJob {
	n.loadSyncJob()
	n.background.mu.Lock()
	if n.background.job.State == "queued" || n.background.job.State == "running" ||
		reason == "page" && n.background.job.State == "completed" && n.clock().Unix()-n.background.job.FinishedAt < 60 {
		status := n.background.job
		status.WorkerReady = n.background.workerReady.Load()
		n.background.mu.Unlock()
		return status
	}
	n.background.job = networkSyncJob{
		ID: fmt.Sprintf("sync-%d", time.Now().UnixNano()), State: "queued", Reason: reason,
		Mode: n.service.config.Mode, StartedAt: n.clock().Unix(),
	}
	n.persistSyncJobLocked()
	status := n.background.job
	status.WorkerReady = n.background.workerReady.Load()
	n.background.mu.Unlock()
	select {
	case n.background.wake <- struct{}{}:
	default:
	}
	return status
}

func syncProgressInt(value any) int64 {
	switch item := value.(type) {
	case int:
		return int64(item)
	case int64:
		return item
	default:
		return 0
	}
}

func (n *networkStore) runCallSync(ctx context.Context) {
	n.background.mu.Lock()
	if n.background.job.State != "queued" {
		n.background.mu.Unlock()
		return
	}
	jobID := n.background.job.ID
	n.background.job.State = "running"
	taskCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	n.background.cancel = cancel
	n.persistSyncJobLocked()
	n.background.mu.Unlock()
	progress := func(values map[string]any) {
		n.background.mu.Lock()
		defer n.background.mu.Unlock()
		job := &n.background.job
		if job.ID != jobID {
			return
		}
		job.Cursor, job.StartCursor = syncProgressInt(values["cursor"]), syncProgressInt(values["start_cursor"])
		job.Watermark = syncProgressInt(values["watermark"])
		job.Imported, job.SkippedSettlements = syncProgressInt(values["imported"]), syncProgressInt(values["skipped_settlements"])
		if job.Watermark > job.StartCursor {
			value := int(min(int64(100), max(int64(0), (job.Cursor-job.StartCursor)*100/(job.Watermark-job.StartCursor))))
			job.Progress = &value
		}
		n.persistSyncJobLocked()
	}
	result, err := n.syncCallsProgress(taskCtx, progress)
	cancel()
	n.background.mu.Lock()
	n.background.cancel = nil
	job := &n.background.job
	if job.ID == jobID {
		job.FinishedAt = n.clock().Unix()
		job.State, job.Error = "completed", ""
		if err != nil {
			job.State, job.Error = "failed", "同步未完成，请检查日志库、管理库及表结构。再次同步会从已提交游标继续。"
			if errors.Is(err, context.Canceled) {
				job.State, job.Error = "cancelled", "任务已停止；已提交数据保留，下次从游标继续。"
			} else if errors.Is(err, context.DeadlineExceeded) {
				job.State, job.Error = "interrupted", "单次后台时间片已结束；已提交游标保留，后台会继续导入。"
			}
		} else {
			job.Imported, job.SkippedSettlements = syncProgressInt(result["imported"]), syncProgressInt(result["skipped_settlements"])
			value := 100
			job.Progress = &value
		}
		n.persistSyncJobLocked()
	}
	n.background.mu.Unlock()
	n.revision.Add(1)
}

func (s *dashboardService) runNetworkScheduler(ctx context.Context) {
	n := s.network
	n.loadSyncJob()
	n.background.workerReady.Store(true)
	defer n.background.workerReady.Store(false)
	if s.config.Mode == "live" {
		n.queueCallSync("startup")
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-n.background.wake:
			n.runCallSync(ctx)
		case <-ticker.C:
			if s.config.Mode == "live" {
				n.queueCallSync("scheduled")
			}
		}
	}
}

func (n *networkStore) cachedAnalysis(ctx context.Context, source string, start, end, userID, tokenID int64, ip string) (networkAnalysis, error) {
	key := fmt.Sprintf("%s/%d/%d/%d/%d/%s", source, start, end, userID, tokenID, ip)
	revision := n.revision.Load()
	n.background.cacheMu.Lock()
	for _, entry := range n.background.cache {
		if entry.key == key && entry.revision == revision && n.clock().Sub(entry.at) < 20*time.Second {
			value := entry.value
			value.Metadata = maps.Clone(value.Metadata)
			value.Metadata["aggregation_cache"] = "hit"
			value.Metadata["sync_job"] = n.syncJobStatus()
			n.background.cacheMu.Unlock()
			return value, nil
		}
	}
	n.background.cacheMu.Unlock()
	value, err := n.Analyze(ctx, source, start, end, userID, tokenID, ip)
	if err != nil {
		return value, err
	}
	value.Metadata["sync_job"] = n.syncJobStatus()
	job := n.syncJobStatus()
	if source == networkSourceCall && (job.State == "queued" || job.State == "running" || job.State == "failed" || job.State == "interrupted") {
		value.Completeness = "import_in_progress"
		value.Metadata["import_complete"] = false
		value.Warnings = append(value.Warnings, "调用历史尚在后台同步或等待续传；当前统计仅覆盖已提交事件。")
	} else if source == networkSourceCall {
		value.Metadata["import_complete"] = job.State == "completed" || n.service.config.Mode == "demo"
	}
	value.Metadata["aggregation_cache"] = "miss"
	if n.revision.Load() == revision {
		n.background.cacheMu.Lock()
		if len(n.background.cache) >= 4 {
			n.background.cache = n.background.cache[1:]
		}
		n.background.cache = append(n.background.cache, cachedNetworkAnalysis{key: key, revision: revision, at: n.clock(), value: value})
		n.background.cacheMu.Unlock()
	}
	value.Metadata = maps.Clone(value.Metadata)
	return value, nil
}

func (s *dashboardService) ipSyncStatusHTTP(c *gin.Context) {
	status := s.network.syncJobStatus()
	// The persisted DB cursor is authoritative after a restart.
	ctx, cancel := context.WithTimeout(c.Request.Context(), queryTimeout)
	defer cancel()
	cursor, err := s.network.getMeta(ctx, "call_cursor")
	if err == nil {
		if value, parseErr := strconv.ParseInt(cursor, 10, 64); parseErr == nil && value > status.Cursor {
			status.Cursor = value
		}
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": status})
}

func (s *dashboardService) ipSyncCancelHTTP(c *gin.Context) {
	n := s.network
	n.loadSyncJob()
	n.background.mu.Lock()
	if n.background.cancel != nil {
		n.background.cancel()
	} else if n.background.job.State == "queued" {
		n.background.job.State = "cancelled"
		n.background.job.FinishedAt = n.clock().Unix()
		n.persistSyncJobLocked()
	}
	n.background.mu.Unlock()
	c.JSON(http.StatusOK, gin.H{"success": true, "data": n.syncJobStatus()})
}
