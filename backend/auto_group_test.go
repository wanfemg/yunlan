package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAutoGroupDemoPreviewScanAndRevert(t *testing.T) {
	config := Config{Mode: "demo", Timezone: "Asia/Shanghai", Location: time.FixedZone("CST", 8*60*60)}
	service, err := newDashboardService(config)
	if err != nil {
		t.Fatal(err)
	}
	router := newRouter(config, service)

	preview := requestAutoGroup(router, http.MethodGet, "/api/auto-group/preview?page=1&page_size=20", "")
	if preview.Code != http.StatusOK {
		t.Fatalf("preview status = %d: %s", preview.Code, preview.Body.String())
	}
	var previewBody struct {
		Data struct {
			Total int             `json:"total"`
			Items []autoGroupUser `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(preview.Body.Bytes(), &previewBody); err != nil {
		t.Fatal(err)
	}
	if previewBody.Data.Total != 17 || len(previewBody.Data.Items) != 17 {
		t.Fatalf("preview returned %d users", previewBody.Data.Total)
	}

	dryRun := requestAutoGroup(router, http.MethodPost, "/api/auto-group/scan?dry_run=true", "")
	if dryRun.Code != http.StatusOK || !strings.Contains(dryRun.Body.String(), `"assigned":17`) {
		t.Fatalf("dry run response = %d %s", dryRun.Code, dryRun.Body.String())
	}
	users, err := service.autoGroupUsers(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range users {
		if user.ID == 101 && user.Group != "default" {
			t.Fatalf("dry run modified user group: %q", user.Group)
		}
	}

	scan := requestAutoGroup(router, http.MethodPost, "/api/auto-group/scan?dry_run=false", "")
	if scan.Code != http.StatusOK || !strings.Contains(scan.Body.String(), `"assigned":17`) {
		t.Fatalf("scan response = %d %s", scan.Code, scan.Body.String())
	}
	logs := requestAutoGroup(router, http.MethodGet, "/api/auto-group/logs?page=1&page_size=20", "")
	var logBody struct {
		Data struct {
			Items []autoGroupLog `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(logs.Body.Bytes(), &logBody); err != nil {
		t.Fatal(err)
	}
	if len(logBody.Data.Items) < 17 {
		t.Fatalf("scan created %d logs", len(logBody.Data.Items))
	}

	var scanLogs struct {
		Data struct {
			Items []autoGroupLog `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(logs.Body.Bytes(), &scanLogs); err != nil {
		t.Fatal(err)
	}
	assign := scanLogs.Data.Items[0]
	if assign.Action != "assign" {
		t.Fatalf("newest scan log is not an assignment: %+v", assign)
	}
	revert := requestAutoGroup(router, http.MethodPost, "/api/auto-group/revert", fmt.Sprintf(`{"log_id":%d}`, assign.ID))
	if revert.Code != http.StatusOK {
		t.Fatalf("revert response = %d %s", revert.Code, revert.Body.String())
	}
	users, err = service.autoGroupUsers(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, user := range users {
		if user.ID == assign.UserID && user.Group != assign.OldGroup {
			t.Fatalf("revert left user %d in %q", user.ID, user.Group)
		}
	}
	duplicate := requestAutoGroup(router, http.MethodPost, "/api/auto-group/revert", fmt.Sprintf(`{"log_id":%d}`, assign.ID))
	if duplicate.Code != http.StatusNotFound {
		t.Fatalf("duplicate revert status = %d", duplicate.Code)
	}
}

func TestAutoGroupConfigValidation(t *testing.T) {
	if err := validateAutoGroupConfig(autoGroupConfig{Mode: "simple", ScanInterval: 60}); err != nil {
		t.Fatal(err)
	}
	if err := validateAutoGroupConfig(autoGroupConfig{Mode: "unsupported", ScanInterval: 60}); err == nil {
		t.Fatal("expected invalid mode to fail")
	}
	if err := validateAutoGroupConfig(autoGroupConfig{Mode: "simple", ScanInterval: 1441}); err == nil {
		t.Fatal("expected out-of-range interval to fail")
	}
}

func TestAutoGroupStatePersistsConfigAndAudit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auto-group-state.json")
	state, err := newAutoGroupState(path, false)
	if err != nil {
		t.Fatal(err)
	}
	journal := state.snapshot()
	journal.Config.Enabled = true
	journal.Config.TargetGroup = "community"
	addAutoGroupLog(&journal, autoGroupLog{UserID: 55, Username: "operator-test", OldGroup: "default", NewGroup: "community", Action: "move", CreatedAt: 1770000000})
	if err := state.save(journal); err != nil {
		t.Fatal(err)
	}

	reloaded, err := newAutoGroupState(path, false)
	if err != nil {
		t.Fatal(err)
	}
	persisted := reloaded.snapshot()
	if !persisted.Config.Enabled || persisted.Config.TargetGroup != "community" {
		t.Fatalf("config was not persisted: %+v", persisted.Config)
	}
	if len(persisted.Logs) != 1 || persisted.Logs[0].UserID != 55 || persisted.NextID != 2 {
		t.Fatalf("audit was not persisted: %+v", persisted)
	}
}

func requestAutoGroup(router http.Handler, method, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}
