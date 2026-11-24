package main

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestDashboardRanges(t *testing.T) {
	location, err := time.LoadLocation("Asia/Kathmandu")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.November, 20, 3, 25, 15, 310000000, location)
	tests := []struct {
		period Period
		hours  time.Duration
		count  int
	}{
		{Period24Hours, 24, 25},
		{Period7Days, 7 * 24, 8},
		{Period30Days, 30 * 24, 31},
	}
	for _, tc := range tests {
		t.Run(string(tc.period), func(t *testing.T) {
			window, previous, trend, err := dashboardRanges(now, tc.period, location)
			if err != nil {
				t.Fatal(err)
			}
			duration := tc.hours * time.Hour
			if !window.End.Equal(now) || window.End.Sub(window.Start) != duration ||
				!previous.End.Equal(window.Start) || previous.End.Sub(previous.Start) != duration {
				t.Fatalf("unexpected periods: current=%+v prior=%+v", window, previous)
			}
			if len(trend) != tc.count || !trend[0].At.Equal(window.Start) {
				t.Fatalf("expected %d buckets starting at %s, got %v", tc.count, window.Start, trend)
			}
			for i := 1; i < len(trend); i++ {
				at := trend[i].At
				if !at.After(trend[i-1].At) || !at.Before(window.End) || at.Minute() != 0 ||
					(tc.period != Period24Hours && at.Hour() != 0) {
					t.Fatalf("invalid bucket boundary: %s", at)
				}
			}
			if got := window.End.Sub(trend[len(trend)-1].At); got <= 0 || got >= time.Hour*24 {
				t.Fatalf("last bucket should cover the partial final interval: got %s", got)
			}
		})
	}
}

func TestUnixBoundary(t *testing.T) {
	if got := unixBoundary(time.Unix(100, 0)); got != 100 {
		t.Fatalf("integer timestamp moved to %d", got)
	}
	if got := unixBoundary(time.Unix(100, 1)); got != 101 {
		t.Fatalf("subsecond timestamp should round up, got %d", got)
	}
}

func TestTrendBucketsThroughDST(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.November, 1, 4, 0, 0, 0, location)
	window, _, trend, err := dashboardRanges(now, Period24Hours, location)
	if err != nil {
		t.Fatal(err)
	}
	repeated := 0
	for i, point := range trend {
		if point.At.Day() == 1 && point.At.Hour() == 1 {
			repeated++
		}
		if i > 0 && point.At.Sub(trend[i-1].At) != time.Hour {
			t.Fatalf("gap between absolute hour buckets at %s", point.At)
		}
	}
	if window.End.Sub(window.Start) != 24*time.Hour || len(trend) != 24 || repeated != 2 {
		t.Fatalf("expected 24 buckets including both 01:00s, got %d buckets and %d repeated hours", len(trend), repeated)
	}
}

func TestDemoUsesConsistentTotals(t *testing.T) {
	config, err := loadConfig(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	service, err := newDashboardService(config)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return time.Date(2026, time.November, 20, 3, 25, 0, 0, config.Location) }
	ctx := context.Background()
	first, err := service.Dashboard(ctx, Period7Days)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Dashboard(ctx, Period7Days)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("the same demo window yielded different data")
	}
	if first.Mode != "demo" || len(first.Sources) != 2 || first.Sources[0].Status != "demo" || first.Sources[1].Status != "demo" {
		t.Fatalf("demo sources were not labeled: %v", first.Sources)
	}
	var requests, failed, quota, modelRequests, modelQuota, modelTokens int64
	for _, point := range first.Trend {
		requests += point.Requests
		failed += point.Errors
		quota += point.Quota
	}
	for _, model := range first.Models {
		modelRequests += model.Requests
		modelQuota += model.Quota
		modelTokens += model.Tokens
	}
	if requests != *first.Usage.Requests || failed != *first.Usage.FailedRequests || quota != *first.Usage.Quota ||
		modelRequests != *first.Usage.SuccessfulRequests || modelQuota != quota ||
		modelTokens != *first.Usage.PromptTokens+*first.Usage.CompletionTokens {
		t.Fatalf("demo summaries differ from trends or models: %+v", first.Usage)
	}
	// Extended workspace fixtures intentionally generate deterministic history.
	// The exact count can repeat on adjacent days; consistency above is the
	// meaningful invariant.
}
