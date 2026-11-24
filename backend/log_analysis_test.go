package main

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func setupLogAnalysisTest(t *testing.T) *gin.Engine {
	t.Helper()
	s := &dashboardService{
		config: Config{Mode: "demo", Timezone: "UTC", Location: time.UTC},
		now: func() time.Time {
			return time.Date(2026, time.November, 20, 12, 15, 0, 0, time.UTC)
		},
	}
	s.network = newNetworkStore(s)
	s.userManagement = newUserManagementState()
	if err := s.seedDemoWorkspace(); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.GET("/api/log-analysis", s.logAnalysisHTTP)
	return router
}

func getTestLogAnalysis(t *testing.T, router *gin.Engine, query string) logAnalysisData {
	t.Helper()
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/api/log-analysis?"+query, nil))
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var response logAnalysisData
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func TestLogAnalysisFiltersAndPagination(t *testing.T) {
	router := setupLogAnalysisTest(t)
	first := getTestLogAnalysis(t, router, "status=error&page_size=3")
	second := getTestLogAnalysis(t, router, "status=error&page_size=3&page=2")
	if first.Total < 6 || first.Total != second.Total || len(first.Records) != 3 || len(second.Records) != 3 {
		t.Fatalf("unexpected page sizes or totals %d/%d, %d/%d", len(first.Records), len(second.Records), first.Total, second.Total)
	}
	if first.Records[0].CreatedAt.Before(second.Records[0].CreatedAt) ||
		first.Records[len(first.Records)-1].CreatedAt.Before(second.Records[len(second.Records)-1].CreatedAt) {
		t.Fatal("record pages are not in stable newest-first order")
	}
	if first.Summary.Errors != first.Total || first.Summary.Success != 0 ||
		first.Summary.SuccessRate == nil || *first.Summary.SuccessRate != 0 || first.Summary.AverageDuration != nil {
		t.Fatalf("failed-only summary mismatch: %+v", first.Summary)
	}
	var pointsTotal int64
	for _, point := range first.Trend {
		pointsTotal += point.Requests
		if point.Requests != point.Errors || point.Quota != 0 {
			t.Fatalf("unexpected failure bucket: %+v", point)
		}
	}
	if pointsTotal != first.Total {
		t.Fatalf("trend count = %d, want %d", pointsTotal, first.Total)
	}

	record := first.Records[0]
	query := url.Values{
		"model": {record.Model}, "channel_id": {strconv.FormatInt(record.ChannelID, 10)},
		"request_id": {record.RequestID}, "status": {"error"},
	}
	exact := getTestLogAnalysis(t, router, query.Encode())
	if exact.Total != 1 || len(exact.Records) != 1 || exact.Records[0].ID != record.ID {
		t.Fatalf("combined filters did not find the requested record: %+v", exact)
	}
	query.Set("status", "success")
	empty := getTestLogAnalysis(t, router, query.Encode())
	if empty.Total != 0 || empty.Records == nil || len(empty.Records) != 0 || empty.Summary.SuccessRate != nil {
		t.Fatalf("contradictory filters did not return an empty result: %+v", empty)
	}
}

func TestLogAnalysisRejectsInvalidFilters(t *testing.T) {
	router := setupLogAnalysisTest(t)
	for _, query := range []string{"period=90d", "status=failed", "channel_id=-1", "request_id=%00", "page=0", "page_size=101"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", "/api/log-analysis?"+query, nil))
		if w.Code != 400 {
			t.Errorf("%s: got %d, want 400", query, w.Code)
		}
	}
}

func TestLogAnalysisRankingsUseWholeFilterScope(t *testing.T) {
	router := setupLogAnalysisTest(t)
	first := getTestLogAnalysis(t, router, "period=7d&page_size=1")
	next := getTestLogAnalysis(t, router, "period=7d&page_size=1&page=2")
	if !first.GeneratedAt.Equal(first.Range.End) || len(first.Rankings.Requests) != logRankingLimit ||
		len(first.Rankings.Quota) != logRankingLimit || len(first.Rankings.Models) != 4 {
		t.Fatalf("unexpected analytics metadata or ranking limits: %+v", first.Rankings)
	}
	for i, entry := range first.Rankings.Requests {
		if entry != next.Rankings.Requests[i] || entry.UserID <= 0 || entry.Username == "" || entry.Count <= 1 ||
			(i > 0 && first.Rankings.Requests[i-1].Count < entry.Count) {
			t.Fatalf("request rank %d = %+v", i+1, entry)
		}
	}
	for i, entry := range first.Rankings.Quota {
		if entry.Quota <= 0 || (i > 0 && first.Rankings.Quota[i-1].Quota < entry.Quota) {
			t.Fatalf("quota rank %d = %+v", i+1, entry)
		}
	}
	filtered := getTestLogAnalysis(t, router, "period=7d&status=error&model=gpt-4.1-mini")
	if filtered.Rankings.Quota == nil || len(filtered.Rankings.Quota) != 0 || len(filtered.Rankings.Models) != 1 {
		t.Fatalf("filtered rankings = %+v", filtered.Rankings)
	}
	model := filtered.Rankings.Models[0]
	if model.Model != "gpt-4.1-mini" || model.Count == 0 || model.Count != model.Errors || model.Quota != 0 {
		t.Fatalf("filtered model rank does not match failure-only summary: %+v", model)
	}
}
