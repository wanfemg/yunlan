package main

import (
	"cmp"
	"context"
	"hash/fnv"
	"slices"
	"strconv"
	"strings"
	"time"
)

// All populated demo pages aggregate these observations. No browser-side
// statistics or production database connection are needed for the demo.
func demoCallRecord(event networkEvent, location *time.Location) logAnalysisRecord {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(event.ID))
	id := int64(hash.Sum64() & ((1 << 52) - 1))
	number := func(key string, fallback int64) int64 {
		if value, err := strconv.ParseInt(event.Metadata[key], 10, 64); err == nil && value >= 0 {
			return value
		}
		return fallback
	}
	record := logAnalysisRecord{
		ID: id, CreatedAt: time.Unix(event.OccurredAt, 0).In(location),
		Status: "success", Model: event.Metadata["model"],
		ChannelID: number("channel_id", 1+event.UserID%5),
		RequestID: event.Metadata["request_id"],
		Quota:     number("quota", 0), PromptTokens: number("prompt_tokens", 300+id%1700),
		CompletionTokens: number("completion_tokens", 60+id%650),
		DurationSeconds:  number("duration_seconds", 1+id%8),
	}
	if record.Model == "" {
		record.Model = "gpt-4.1-mini"
	}
	if record.RequestID == "" {
		record.RequestID = strings.ReplaceAll(event.ID, ":", "-")
	}
	if event.Result != "success" {
		record.Status = "error"
		record.Quota, record.PromptTokens, record.CompletionTokens = 0, 0, 0
	}
	return record
}

func (s *dashboardService) readDemoLogAnalysis(ctx context.Context, data *logAnalysisData, filter logAnalysisFilters) error {
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
	userStats := map[int64]logUserRank{}
	modelStats := map[string]logModelRank{}
	var matched, duration, samples int64
	bucket := len(data.Trend) - 1
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		record := demoCallRecord(event, s.config.Location)
		if !filter.matches(record) {
			continue
		}
		for bucket > 0 && record.CreatedAt.Before(data.Trend[bucket].At) {
			bucket--
		}
		point := &data.Trend[bucket]
		point.Requests++
		user := userStats[event.UserID]
		user.UserID, user.Username = event.UserID, names[event.UserID]
		user.Count++
		model := modelStats[record.Model]
		model.Model = record.Model
		model.Count++
		if record.Status == "error" {
			point.Errors++
			model.Errors++
		} else {
			point.Quota += record.Quota
			model.Quota += record.Quota
			user.Quota += record.Quota
			duration += record.DurationSeconds
			samples++
		}
		userStats[event.UserID], modelStats[record.Model] = user, model
		if matched >= data.offset() && len(data.Records) < data.PageSize {
			data.Records = append(data.Records, record)
		}
		matched++
	}
	data.finish(duration, samples)
	for _, user := range userStats {
		data.Rankings.Requests = append(data.Rankings.Requests, user)
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
	data.Rankings.Requests = data.Rankings.Requests[:min(logRankingLimit, len(data.Rankings.Requests))]
	data.Rankings.Quota = data.Rankings.Quota[:min(logRankingLimit, len(data.Rankings.Quota))]
	return nil
}

func (s *dashboardService) demoWorkspaceUsage(ctx context.Context, window TimeRange, points []TrendPoint) ([]TrendPoint, UsageStats, []ModelUsage, error) {
	events, err := s.network.Events(ctx, networkSourceCall, unixBoundary(window.Start), unixBoundary(window.End))
	if err != nil {
		return nil, UsageStats{}, nil, err
	}
	trend := append([]TrendPoint(nil), points...)
	models := map[string]ModelUsage{}
	active := map[int64]struct{}{}
	var requests, failures, quota, prompt, completion, duration int64
	bucket := 0
	for _, event := range events {
		record := demoCallRecord(event, s.config.Location)
		for bucket+1 < len(trend) && !record.CreatedAt.Before(trend[bucket+1].At) {
			bucket++
		}
		requests++
		active[event.UserID] = struct{}{}
		trend[bucket].Requests++
		if record.Status == "error" {
			failures++
			trend[bucket].Errors++
			continue
		}
		quota += record.Quota
		prompt += record.PromptTokens
		completion += record.CompletionTokens
		duration += record.DurationSeconds
		trend[bucket].Quota += record.Quota
		model := models[record.Model]
		model.Model = record.Model
		model.Requests++
		model.Quota += record.Quota
		model.Tokens += record.PromptTokens + record.CompletionTokens
		models[record.Model] = model
	}
	usage := UsageStats{
		Requests: int64Pointer(requests), SuccessfulRequests: int64Pointer(requests - failures),
		FailedRequests: int64Pointer(failures), Quota: int64Pointer(quota),
		PromptTokens: int64Pointer(prompt), CompletionTokens: int64Pointer(completion),
		ActiveUsers: int64Pointer(int64(len(active))),
	}
	if requests > 0 {
		usage.SuccessRate = float64Pointer(100 * float64(requests-failures) / float64(requests))
	}
	if requests > failures {
		usage.AvgDurationSeconds = float64Pointer(float64(duration) / float64(requests-failures))
	}
	list := make([]ModelUsage, 0, len(models))
	for _, model := range models {
		list = append(list, model)
	}
	slices.SortFunc(list, func(a, b ModelUsage) int {
		if a.Requests != b.Requests {
			return cmp.Compare(b.Requests, a.Requests)
		}
		return strings.Compare(a.Model, b.Model)
	})
	return trend, usage, list, nil
}
