package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func newWorkspaceDemoTestService(t *testing.T) *dashboardService {
	t.Helper()
	config, err := loadConfig(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	service := &dashboardService{config: config, now: func() time.Time {
		return time.Date(2026, time.November, 20, 12, 0, 0, 0, config.Location)
	}}
	service.network, service.userManagement, service.risk = newNetworkStore(service), newUserManagementState(), newRiskControlStore(service)
	return service
}

func TestDemoWorkspaceUsesRealStatisticsPathsWithLocalActionExamples(t *testing.T) {
	service := newWorkspaceDemoTestService(t)
	if err := service.seedDemoWorkspace(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	events, err := service.network.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) <= 873 {
		t.Fatalf("unexpected event count %d", len(events))
	}
	sources := map[string]int{}
	users, err := service.managedUsers(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	createdAt := map[int64]int64{}
	regularUsers := 0
	for _, user := range users {
		createdAt[user.ID] = user.CreatedAt
		if user.Role < 10 {
			regularUsers++
		}
	}
	if regularUsers != 60 {
		t.Fatalf("demo contains %d regular users, want 60", regularUsers)
	}
	now := service.now().Unix()
	expectedCalls, expectedLogins := int64(0), int64(0)
	callUsers, registeredUsers := map[int64]bool{}, map[int64]bool{}
	for _, event := range events {
		sources[event.Source]++
		if !strings.HasPrefix(event.ID, "demo:") || event.Metadata["dataset"] != "demo-fixture" {
			t.Fatalf("unmarked demo event: %+v", event)
		}
		if event.UserID > 0 && event.OccurredAt < createdAt[event.UserID] {
			t.Fatalf("demo observation predates account creation: %+v", event)
		}
		if event.Source == networkSourceRegistration && event.Result == "success" && event.UserID > 0 {
			registeredUsers[event.UserID] = true
			if event.Method != demoAutoGroupSource(event.UserID) {
				t.Fatalf("registration source differs from auto-group source: %+v", event)
			}
		}
		if event.OccurredAt >= now-7*86400 && event.OccurredAt < now {
			switch event.Source {
			case networkSourceCall:
				expectedCalls++
				if event.UserID > 0 {
					callUsers[event.UserID] = true
				}
			case networkSourceLogin:
				expectedLogins++
			}
		}
	}
	if sources[networkSourceCall] <= 843 || sources[networkSourceLogin] <= 27 ||
		sources[networkSourceRegistration] <= 3 || len(registeredUsers) != regularUsers {
		t.Fatalf("source separation: %+v", sources)
	}
	info, err := service.network.Lookup(ctx, "1.1.1.1")
	if err != nil || info.DataSource != "demo-fixture" || info.CountryCode == "" || info.ISP == "" {
		t.Fatalf("demo enrichment not marked: %+v %v", info, err)
	}
	calls, err := service.network.Analyze(ctx, networkSourceCall, now-7*86400, now, 0, 0, "")
	if err != nil || calls.Summary.Events != expectedCalls || calls.Summary.Users != len(callUsers) || len(calls.Geography) < 5 {
		t.Fatalf("real call statistics failed: %+v %v", calls.Summary, err)
	}
	logins, err := service.network.Analyze(ctx, networkSourceLogin, now-7*86400, now, 0, 0, "")
	if err != nil || logins.Summary.Events != expectedLogins {
		t.Fatalf("login events mixed: %+v %v", logins.Summary, err)
	}
	reviews, err := service.riskModule().list(ctx, "assessment:", 0)
	if err != nil || len(reviews) <= 3 {
		t.Fatalf("demo assessments unavailable: %d %v", len(reviews), err)
	}
	actions, err := service.riskModule().list(ctx, "action:", 0)
	if err != nil || len(actions) < 3 {
		t.Fatal("local demo action examples are unavailable", err)
	}
	for _, payload := range actions {
		var action riskAction
		if err := json.Unmarshal(payload, &action); err != nil || action.Operator != "demo-fixture" || createdAt[action.UserID] <= 0 {
			t.Fatalf("action does not belong to a local demo account: %+v %v", action, err)
		}
	}
	for _, payload := range reviews {
		var assessment riskAssessment
		if err := json.Unmarshal(payload, &assessment); err != nil || !assessment.DryRun || assessment.AI.Status != "disabled" {
			t.Fatalf("unsafe demo assessment: %+v %v", assessment, err)
		}
	}
}

func TestDemoWorkspaceSeedingIsIdempotentAndLiveDoesNothing(t *testing.T) {
	service := newWorkspaceDemoTestService(t)
	if err := service.seedDemoWorkspace(); err != nil {
		t.Fatal(err)
	}
	before, err := service.network.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return time.Date(2026, time.November, 21, 0, 0, 0, 0, time.UTC) }
	if err := service.seedDemoWorkspace(); err != nil {
		t.Fatal(err)
	}
	after, err := service.network.Snapshot(t.Context())
	if err != nil || len(before) != len(after) {
		t.Fatal("repeat seeding added duplicate events", err)
	}
	live := &dashboardService{config: Config{Mode: "live"}}
	if err := live.seedDemoWorkspace(); err != nil || live.network != nil || live.userManagement != nil || live.risk != nil {
		t.Fatal("live service was altered", err)
	}
}

func TestPublicWorkspaceMetaExposesOnlyDisplaySettingsAndConfigurationFlags(t *testing.T) {
	service := newWorkspaceDemoTestService(t)
	service.config.AdminToken = "private-site-admin"
	service.config.ProbeSharedSecret = "private-probe-secret"
	service.config.NewAPIAdminToken = "private-newapi-secret"
	service.config.NewAPIAdminUserID = 1
	service.config.NewAPIUpstream, _ = url.Parse("http://127.0.0.1:3000")
	service.config.FinanceUpstream, _ = url.Parse("http://127.0.0.1:18090")
	response := httptest.NewRecorder()
	newRouter(service.config, service).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/meta", nil))
	var metadata SystemMeta
	if err := json.Unmarshal(response.Body.Bytes(), &metadata); err != nil || response.Code != http.StatusOK {
		t.Fatalf("metadata response %d %s", response.Code, response.Body.String())
	}
	if metadata.QuotaPerUnit != service.config.QuotaPerUnit || metadata.Timezone != service.config.Timezone ||
		!metadata.FinanceConfigured || !metadata.ProbeConfigured || !metadata.NewAPIManagementConfigured || !metadata.AuthRequired {
		t.Fatalf("display metadata incomplete: %+v", metadata)
	}
	if strings.Contains(response.Body.String(), "private-") || strings.Contains(response.Body.String(), "127.0.0.1") {
		t.Fatal("public metadata leaked credentials or origins")
	}
}
