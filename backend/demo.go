package main

import (
	"context"
	"math"
	"time"
)

func (s *dashboardService) demo(ctx context.Context, data DashboardData, previous TimeRange, points []TrendPoint) (DashboardData, error) {
	users, err := s.managedUsers(ctx, 0)
	if err != nil {
		return data, err
	}
	totalTokens, activeTokens := s.demoTokenInventory()
	data.Inventory = Inventory{
		TotalUsers: int64Pointer(int64(len(users))), TotalTokens: int64Pointer(totalTokens), ActiveTokens: int64Pointer(activeTokens),
		TotalChannels: int64Pointer(5), ActiveChannels: int64Pointer(5),
	}
	data.Trend, data.Usage, data.Models, err = s.demoWorkspaceUsage(ctx, data.Range, points)
	if err != nil {
		return data, err
	}
	_, data.PreviousUsage, _, err = s.demoWorkspaceUsage(ctx, previous, trendBuckets(previous, data.Period, s.config.Location))
	if err != nil {
		return data, err
	}
	data.Inventory.TotalModels = int64Pointer(int64(len(data.Models)))
	data.Sources = []SourceStatus{
		{ID: "main", Name: "NewAPI 主库", Status: "demo", Message: "演示数据，未连接真实数据库"},
		{ID: "logs", Name: "NewAPI 日志库", Status: "demo", Message: "与用户、令牌和 IP 分析共用本地演示调用记录"},
	}
	data.Warnings = []string{"当前为本地演示数据，支持筛选、分页及管理操作；重启演示服务后恢复初始数据。"}
	return data, nil
}

// Stable time-based rates make demo refreshes reproducible without random data.
// Partial first/last buckets contribute only their actual covered duration.
func demoUsage(window TimeRange, points []TrendPoint) ([]TrendPoint, UsageStats) {
	trend := make([]TrendPoint, len(points))
	var requests, errors, quota int64
	for i, point := range points {
		end := window.End
		if i+1 < len(points) {
			end = points[i+1].At
		}
		hours := end.Sub(point.At).Hours()
		phase := float64(point.At.Unix()) / float64(24*time.Hour/time.Second)
		rate := 1750 + 420*math.Sin(phase*2*math.Pi-1.1) + 230*math.Sin(phase*6*math.Pi) +
			320*math.Sin(phase*2*math.Pi/7+0.7) + 230*math.Cos(phase*2*math.Pi/19)
		count := int64(math.Round(rate * hours))
		failed := int64(math.Round(float64(count) * (0.008 + 0.003*(1+math.Sin(phase*8*math.Pi)))))
		used := (count - failed) * 18750
		trend[i] = TrendPoint{At: point.At, Requests: count, Errors: failed, Quota: used}
		requests, errors, quota = requests+count, errors+failed, quota+used
	}
	users := int64(86 + window.End.Sub(window.Start).Hours()*3.7)
	if users > 1184 {
		users = 1184
	}
	usage := UsageStats{
		Requests: int64Pointer(requests), SuccessfulRequests: int64Pointer(requests - errors), FailedRequests: int64Pointer(errors),
		Quota: int64Pointer(quota), PromptTokens: int64Pointer((requests - errors) * 1240),
		CompletionTokens: int64Pointer((requests - errors) * 620), ActiveUsers: int64Pointer(users),
		AvgDurationSeconds: float64Pointer(1.28),
	}
	if requests > 0 {
		usage.SuccessRate = float64Pointer(100 * float64(requests-errors) / float64(requests))
	}
	return trend, usage
}

func demoModels(usage UsageStats) []ModelUsage {
	names := []string{"gpt-4.1", "claude-sonnet-4", "deepseek-v3", "gemini-2.5-pro", "其他模型"}
	weights := []int64{36, 27, 19, 12, 6}
	models := make([]ModelUsage, len(names))
	remaining := *usage.SuccessfulRequests
	for i, name := range names {
		count := *usage.SuccessfulRequests * weights[i] / 100
		if i == len(names)-1 {
			count = remaining
		}
		remaining -= count
		models[i] = ModelUsage{Model: name, Requests: count, Quota: count * 18750, Tokens: count * 1860}
	}
	return models
}
