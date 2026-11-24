package main

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

type tokenNetworkExit struct {
	IP        string `json:"ip"`
	Requests  int64  `json:"requests"`
	FirstSeen int64  `json:"first_seen"`
	LastSeen  int64  `json:"last_seen"`
}

type tokenNetworkCounts struct {
	Requests int64
	IPs      map[string]*tokenNetworkExit
}

const tokenNetworkBasis = "retained_network_call_events"
const tokenNetworkNote = "出口统计与 IP 分析使用同一批已采集调用事件，排除任务结算、缺失和无效 IP，并合并 IPv4 映射地址；其余使用指标仍来自 NewAPI 调用日志。"

// Token analysis keeps its inclusive end_time contract; network queries use
// the equivalent exclusive end_time+1 boundary.
func parseTokenAnalysisRange(c *gin.Context, now int64) (start, end, seconds int64, err error) {
	window := c.DefaultQuery("window", "24h")
	seconds, exists := tokenAnalysisWindows[window]
	if !exists {
		return 0, 0, 0, errors.New("window 不支持")
	}
	values := c.Request.URL.Query()
	_, hasStart := values["start_time"]
	_, hasEnd := values["end_time"]
	end = now
	if hasEnd {
		end, err = strconv.ParseInt(c.Query("end_time"), 10, 64)
		if err != nil || end < 0 || end > now || end == math.MaxInt64 {
			return 0, 0, 0, errors.New("end_time 无效")
		}
	}
	if hasStart {
		if !hasEnd {
			return 0, 0, 0, errors.New("start_time 与 end_time 须同时提供")
		}
		start, err = strconv.ParseInt(c.Query("start_time"), 10, 64)
		if err != nil || start < 0 || end < start || end-start >= 366*86400 {
			return 0, 0, 0, errors.New("查询时间范围须在 366 天内且结束时间不得早于开始时间")
		}
		seconds = end - start + 1
	} else {
		start = end - seconds
		if start < 0 {
			return 0, 0, 0, errors.New("查询时间范围无效")
		}
	}
	return start, end, seconds, nil
}

// Read the normalized call source, rather than counting raw log IP strings.
// A failed import is unavailable data; it must not become a zero-IP result.
func (s *dashboardService) tokenNetworkCounts(ctx context.Context, start, end int64, ids []int64) (map[int64]*tokenNetworkCounts, error) {
	if s.network == nil {
		return nil, errors.New("调用网络事件采集尚未初始化")
	}
	if start < 0 || end <= start {
		return nil, errors.New("调用出口统计时间范围无效")
	}
	status, err := s.network.SyncCalls(ctx)
	if err != nil {
		return nil, errors.New("调用日志同步未完成，出口统计暂不可用")
	}
	if complete, _ := status["complete"].(bool); !complete {
		return nil, errors.New("调用日志仍在导入，出口统计暂不可用")
	}
	if raw, err := s.network.getMeta(ctx, "call_source_capabilities"); err != nil {
		return nil, err
	} else if raw != "" {
		var capabilities map[string]bool
		if err := json.Unmarshal([]byte(raw), &capabilities); err != nil {
			return nil, errors.New("调用日志字段状态暂不可用")
		}
		if available, known := capabilities["ip"]; known && !available {
			return nil, errors.New("调用日志未提供 IP 字段，出口数未知")
		}
	}
	counts := make(map[int64]*tokenNetworkCounts)
	observe := func(id int64, ip string, at int64) {
		if id <= 0 {
			return
		}
		canonical, class := classifyNetworkIP(ip)
		if class == "missing" || class == "invalid" {
			return
		}
		entry := counts[id]
		if entry == nil {
			entry = &tokenNetworkCounts{IPs: make(map[string]*tokenNetworkExit)}
			counts[id] = entry
		}
		entry.Requests++
		exit := entry.IPs[canonical]
		if exit == nil {
			exit = &tokenNetworkExit{IP: canonical, FirstSeen: at, LastSeen: at}
			entry.IPs[canonical] = exit
		}
		exit.Requests++
		if at < exit.FirstSeen {
			exit.FirstSeen = at
		}
		if at > exit.LastSeen {
			exit.LastSeen = at
		}
	}
	if s.config.Mode == "demo" {
		events, err := s.network.Events(ctx, networkSourceCall, start, end)
		if err != nil {
			return nil, err
		}
		wanted := make(map[int64]bool, len(ids))
		for _, id := range ids {
			wanted[id] = true
		}
		for _, event := range events {
			if len(ids) == 0 || wanted[event.TokenID] {
				observe(event.TokenID, event.IP, event.OccurredAt)
			}
		}
		return counts, nil
	}
	if s.management.db == nil {
		return nil, errors.New("调用网络事件存储暂不可用")
	}
	args := []any{networkSourceCall, start, end}
	query := "SELECT token_id,ip,occurred_at FROM siteadmin_network_events WHERE source=? AND occurred_at>=? AND occurred_at<? AND token_id>0"
	if len(ids) > 0 {
		query += " AND token_id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + ")"
		for _, id := range ids {
			args = append(args, id)
		}
	}
	rows, err := s.management.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, at int64
		var ip string
		if err := rows.Scan(&id, &ip, &at); err != nil {
			return nil, err
		}
		observe(id, ip, at)
	}
	return counts, rows.Err()
}

func sortedTokenNetworkExits(counts *tokenNetworkCounts) []tokenNetworkExit {
	exits := make([]tokenNetworkExit, 0)
	if counts != nil {
		for _, exit := range counts.IPs {
			exits = append(exits, *exit)
		}
	}
	sort.Slice(exits, func(i, j int) bool {
		if exits[i].Requests != exits[j].Requests {
			return exits[i].Requests > exits[j].Requests
		}
		return exits[i].IP < exits[j].IP
	})
	return exits
}

func (s *dashboardService) tokenNetworkMetadata(start, end int64, err error) map[string]any {
	warnings := []string{}
	if err != nil {
		warnings = append(warnings, err.Error())
	}
	if !s.config.NetworkHistoryComplete {
		warnings = append(warnings, "仅统计已经采集和保留的调用历史，源日志删除前的历史完整性尚未确认。")
	}
	return map[string]any{
		"available": err == nil, "sync_complete": err == nil,
		"history_complete": s.config.NetworkHistoryComplete,
		"source":           networkSourceCall, "basis": tokenNetworkBasis,
		"window_start": start, "window_end": end, "note": tokenNetworkNote, "warnings": warnings,
	}
}
