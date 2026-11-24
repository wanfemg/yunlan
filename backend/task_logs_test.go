package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestTaskLogsHTTP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	config, err := loadConfig(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	config.AdminToken = "sample-admin-token"
	service, err := newDashboardService(config)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time {
		return time.Date(2026, time.November, 20, 3, 25, 15, 0, config.Location)
	}
	router := newRouter(config, service)
	request := func(query string, token bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/task-logs"+query, nil)
		if token {
			req.Header.Set("Authorization", "Bearer "+config.AdminToken)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	getData := func(query string) taskLogData {
		t.Helper()
		response := request(query, true)
		if response.Code != http.StatusOK {
			t.Fatalf("%s returned %d: %s", query, response.Code, response.Body.String())
		}
		var data taskLogData
		if err := json.NewDecoder(response.Body).Decode(&data); err != nil {
			t.Fatal(err)
		}
		return data
	}
	if result := request("", false); result.Code != http.StatusUnauthorized {
		t.Fatalf("task logs require the configured admin token, got %d", result.Code)
	}
	first := getData("?period=24h&page_size=5")
	second := getData("?period=24h&page_size=5&page=2")
	if first.Mode != "demo" || first.Total <= 5 || first.TotalPages <= 1 || first.Page != 1 ||
		second.Page != 2 || len(first.Items) != 5 || len(second.Items) != 5 ||
		first.Items[len(first.Items)-1].SubmitTime <= second.Items[0].SubmitTime {
		t.Fatalf("unstable or invalid task pagination: %+v, %+v", first, second)
	}
	s := first.Summary
	if first.Total != s.Total || s.Total != s.Succeeded+s.Failed+s.InProgress+s.Pending+s.Unknown {
		t.Fatalf("summary does not account for all matching tasks: %+v", s)
	}
	found := first.Items[0]
	toMatch := url.Values{"task_id": {found.TaskID}, "platform": {found.Platform}, "status": {found.Status}, "page": {"6"}}
	filtered := getData("?" + toMatch.Encode())
	if filtered.Total != 1 || filtered.Page != 1 || len(filtered.Items) != 1 || filtered.Items[0] != found {
		t.Fatalf("exact search should return one previous item and clamp the page: %+v", filtered)
	}
	if filtered.Summary.Quota != found.Quota || filtered.QuotaPerUnit != config.QuotaPerUnit {
		t.Fatalf("quota summary does not match the filtered record and configured multiplier: %+v", filtered)
	}
	for _, user := range []string{strconv.FormatInt(found.UserID, 10), strings.ToUpper(found.Username)} {
		data := getData("?user=" + url.QueryEscape(user))
		if data.Total == 0 {
			t.Fatalf("user filter %q returned no records", user)
		}
		for _, item := range data.Items {
			if item.UserID != found.UserID {
				t.Fatalf("user filter %q returned records from another user: %+v", user, item)
			}
		}
	}
	toMatch.Set("task_id", found.TaskID+"_different")
	empty := getData("?" + toMatch.Encode())
	if empty.Total != 0 || empty.TotalPages != 1 || empty.Items == nil || len(empty.Items) != 0 {
		t.Fatalf("invalid empty result: %+v", empty)
	}
	for _, query := range []string{"?status=ERROR", "?period=1d", "?page=0", "?page_size=101",
		"?task_id=" + strings.Repeat("x", 192)} {
		if result := request(query, true); result.Code != http.StatusBadRequest {
			t.Fatalf("%s should return 400, got %d", query, result.Code)
		}
	}
}

func TestTaskUsageRequiresExplicitTaskID(t *testing.T) {
	const taskID = "task_12%"
	for _, sample := range []struct {
		other string
		want  bool
	}{
		{`{"task_id": "task_12%", "admin_info": {"api_key": "redacted"}}`, true},
		{`{"task_id": "task_12\u0025"}`, true},
		{`{"task_id": "task_12%_extra"}`, false},
		{`{"message": "task_12%"}`, false},
		{`{"admin_info": {"task_id": "task_12%"}}`, false},
		{`{"upstream_task_id": "task_12%"}`, false},
		{`{"task_id": ["task_12%"]}`, false},
		{`{"task_id": "task_12%"`, false},
	} {
		if got := matchesTaskUsage([]byte(sample.other), taskID); got != sample.want {
			t.Errorf("matching %s got %t, want %t", sample.other, got, sample.want)
		}
	}
	if got := taskLogContains(`task_12%!\`); got != `%task!_12!%!!\%` {
		t.Fatalf("unexpected SQL LIKE literal: %q", got)
	}
}

func TestTaskUsageHTTP(t *testing.T) {
	config, err := loadConfig(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	config.AdminToken = "sample-admin-token"
	service, err := newDashboardService(config)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return time.Unix(1_800_000_001, 0) }
	router := newRouter(config, service)
	request := func(id string, token bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/task-logs/"+id+"/logs", nil)
		if token {
			req.Header.Set("Authorization", "Bearer "+config.AdminToken)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	id := service.now().Unix() / demoTaskInterval
	for demoTaskLog(id, service.now().Unix()).Status != "SUCCESS" {
		id--
	}
	successID := strconv.FormatInt(id, 10)
	if got := request(successID, false).Code; got != http.StatusUnauthorized {
		t.Fatalf("unprotected task detail: %d", got)
	}
	if got := request("invalid", true).Code; got != http.StatusBadRequest {
		t.Fatalf("invalid task ID returned %d", got)
	}
	if got := request("1", true).Code; got != http.StatusNotFound {
		t.Fatalf("missing task returned %d", got)
	}
	response := request(successID, true)
	var data taskUsageResponse
	if response.Code != http.StatusOK || json.NewDecoder(response.Body).Decode(&data) != nil ||
		data.TaskID != "demo-task-"+successID || len(data.Logs) != 1 || data.Logs[0].Kind != "consume" {
		t.Fatalf("unexpected task detail: HTTP %d, %+v", response.Code, data)
	}
}

func TestTaskLogMixedTimestampBounds(t *testing.T) {
	start := time.Unix(1_700_000_000, 200_100_000)
	end := start.Add(time.Hour)
	_, args := taskLogWhere(TimeRange{Start: start, End: end}, taskLogFilter{})
	for i, want := range []int64{1_700_000_001, 1_700_003_601, 1_700_000_000_201, 1_700_003_600_201, 1_700_000_001, 1_700_003_601} {
		if args[i] != want {
			t.Fatalf("wrong time bound %d: %v != %d", i, args[i], want)
		}
	}
	for _, timestamp := range []int64{1_700_000_000, 1_700_000_000_200} {
		t.Run(strconv.FormatInt(timestamp, 10), func(t *testing.T) {
			if seconds := taskTimeSeconds(timestamp); seconds != 1_700_000_000 {
				t.Fatalf("different timestamp units should map to the same seconds: %d", seconds)
			}
		})
	}
}
