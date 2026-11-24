package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func riskTestService() *dashboardService {
	service := &dashboardService{
		config: Config{Mode: "demo", Location: time.UTC},
		now:    func() time.Time { return time.Unix(1800000000, 0) },
	}
	service.network = newNetworkStore(service)
	service.risk = newRiskControlStore(service)
	return service
}

func riskTestUser(service *dashboardService, role int64) (*riskUserState, *int) {
	state := &riskUserState{ID: 101, Status: 1, Role: role, AuthVersion: 1, AuthVersionKnown: true}
	calls := new(int)
	service.risk.readUser = func(context.Context, int64) (riskUserState, error) { return *state, nil }
	service.risk.setUser = func(_ context.Context, _ int64, enabled bool, _ string) error {
		*calls++
		if enabled {
			state.Status = 1
		} else {
			state.Status = 2
		}
		state.AuthVersion++
		return nil
	}
	return state, calls
}

func riskTestCode(err error) int {
	var typed *riskError
	if errors.As(err, &typed) {
		return typed.Status
	}
	return 0
}

func TestRiskProxyAndCrossSourceIPDoNotGenerateFindings(t *testing.T) {
	config := defaultRiskConfig()
	events := []networkEvent{
		{ID: "call", Source: "api_call", UserID: 101, TokenID: 7, OccurredAt: 150, IP: "8.8.8.8", Result: "success",
			Metadata: map[string]string{"proxy": "yes", "vpn": "yes", "country": "US"}},
		{ID: "login", Source: "login", UserID: 101, OccurredAt: 160, IP: "1.1.1.1", Result: "success", BrowserID: "browser",
			Metadata: map[string]string{"country": "CN"}},
	}
	result := evaluateRiskEvents(101, 100, 200, config, events)
	if result.Score != 0 || result.AutoEligible || len(result.Findings) != 0 {
		t.Fatalf("proxy or cross-source exits incorrectly scored as malicious: %+v", result)
	}
	if len(result.Features.CallIPs) != 1 || result.Features.CallIPs[0] != "8.8.8.8" ||
		len(result.Features.LoginIPs) != 1 || result.Features.LoginIPs[0] != "1.1.1.1" {
		t.Fatalf("network sources were mixed: %+v", result.Features)
	}
}

func TestRiskUpstreamErrorsAreNotAccountAbuse(t *testing.T) {
	config := defaultRiskConfig()
	events := make([]networkEvent, 1000)
	for i := range events {
		events[i] = networkEvent{ID: fmt.Sprint(i), Source: "api_call", UserID: 101, TokenID: 7,
			OccurredAt: 150, Result: "failure", IP: "8.8.8.8",
			Metadata: map[string]string{"error_type": "upstream_error", "status_code": "503"}}
	}
	result := evaluateRiskEvents(101, 100, 200, config, events)
	if result.Features.FailedCalls != 1000 || result.Score != 0 || result.AutoEligible {
		t.Fatalf("upstream outage became account risk: %+v", result)
	}
}

func TestRiskBurstNeedsBothTokenFanoutAndRequestThreshold(t *testing.T) {
	config := defaultRiskConfig()
	config.MinRequests, config.MaxRequestsPerMinute, config.ConcurrentTokenIPs = 2, 5, 3
	events := make([]networkEvent, 6)
	for i := range events {
		events[i] = networkEvent{ID: fmt.Sprint(i), Source: "api_call", UserID: 101, TokenID: 7,
			OccurredAt: 150, Result: "success", IP: fmt.Sprintf("8.8.8.%d", i%3+1)}
	}
	for i := 0; i < 2; i++ {
		events = append(events, networkEvent{ID: fmt.Sprint("prior-", i), Source: "api_call", UserID: 101,
			OccurredAt: 80, Result: "success", IP: "8.8.8.1"})
	}
	result := evaluateRiskEvents(101, 100, 200, config, events)
	if !result.AutoEligible || result.Score != 80 || len(result.Findings) != 1 {
		t.Fatalf("configured combined rule failed: %+v", result)
	}
	config.MaxRequestsPerMinute = 7
	result = evaluateRiskEvents(101, 100, 200, config, events)
	if result.AutoEligible || result.Score != 0 {
		t.Fatalf("fanout alone scored risk: %+v", result)
	}
	config.MaxRequestsPerMinute = 5
	result = evaluateRiskEvents(101, 100, 200, config, events[:6])
	if result.AutoEligible || result.ProposedAction != "review" || result.Features.BaselineAvailable {
		t.Fatalf("first observed high-volume fleet became eligible for automatic action: %+v", result)
	}
}

func TestRiskWindowAndIdentityAreStrict(t *testing.T) {
	config := defaultRiskConfig()
	config.LoginFailuresThreshold = 2
	events := []networkEvent{
		{Source: "login", UserID: 101, OccurredAt: 99, BrowserID: "known", Result: "success"},
		{Source: "login", UserID: 101, OccurredAt: 100, BrowserID: "known", Result: "success"},
		{Source: "login", UserID: 101, OccurredAt: 101, Result: "pending"},
		{Source: "login", UserID: 101, OccurredAt: 200, Result: "failure"},
		{Source: "login", UserID: 0, OccurredAt: 150, ClaimedAccount: "101", Result: "failure"},
		{Source: "registration", UserID: 101, OccurredAt: 150, Result: "success"},
	}
	result := evaluateRiskEvents(101, 100, 200, config, events)
	if result.Features.LoginSuccesses != 1 || result.Features.LoginFailures != 0 ||
		result.Features.PendingLogins != 1 || result.Features.NewBrowserLogins != 0 ||
		result.Features.RegistrationSuccess != 1 || result.Score != 0 {
		t.Fatalf("window, pending state or claimed identity was misinterpreted: %+v", result)
	}
}

func TestRiskActionsIdempotentlyBanAndRestoreOwnedState(t *testing.T) {
	service := riskTestService()
	state, calls := riskTestUser(service, 1)
	ctx := context.Background()
	first, err := service.performRiskUserAction(ctx, 101, "ban", "reviewed evidence", "admin", "first")
	if err != nil || state.Status != 2 || *calls != 1 {
		t.Fatalf("ban=%v, %v, state=%+v calls=%d", first, err, state, *calls)
	}
	second, err := service.performRiskUserAction(ctx, 101, "ban", "reviewed evidence", "admin", "first")
	if err != nil || second["replayed"] != true || *calls != 1 {
		t.Fatalf("duplicate action was applied: %v, %v calls=%d", second, err, *calls)
	}
	if _, err := service.performRiskUserAction(ctx, 102, "ban", "reviewed evidence", "admin", "first"); riskTestCode(err) != 409 {
		t.Fatalf("key reuse for another user returned %v", err)
	}
	release, err := service.performRiskUserAction(ctx, 101, "unban", "case resolved", "admin", "second")
	if err != nil || state.Status != 1 || *calls != 2 {
		t.Fatalf("release=%v, %v, state=%+v", release, err, state)
	}
	var original riskAction
	if found, err := service.risk.get(ctx, "action:"+first["action"].(riskAction).ID, &original); err != nil || !found ||
		original.State != "released" || original.ReleasedBy == "" {
		t.Fatalf("original ban was not atomically released: %+v, %v", original, err)
	}
}

func TestRiskReleaseRejectsExternalStateChangesAndUnownedBans(t *testing.T) {
	service := riskTestService()
	state, calls := riskTestUser(service, 1)
	ctx := context.Background()
	if _, err := service.performRiskUserAction(ctx, 101, "ban", "evidence", "admin", "ban"); err != nil {
		t.Fatal(err)
	}
	state.AuthVersion++
	if _, err := service.performRiskUserAction(ctx, 101, "unban", "resolved", "admin", "unban"); riskTestCode(err) != 409 || *calls != 1 {
		t.Fatalf("changed authentication state restored: %v, calls=%d", err, *calls)
	}
	other := riskTestService()
	otherState, otherCalls := riskTestUser(other, 1)
	otherState.Status = 2
	result, err := other.performRiskUserAction(ctx, 101, "ban", "already disabled", "admin", "noop")
	if err != nil || result["action"].(riskAction).OwnsTransition || *otherCalls != 0 {
		t.Fatalf("existing disable was incorrectly claimed: %v %v", result, err)
	}
	if _, err := other.performRiskUserAction(ctx, 101, "unban", "resolved", "admin", "unowned"); riskTestCode(err) != 409 {
		t.Fatalf("unowned disable could be released: %v", err)
	}
}

func TestRiskAdministratorProtectionAndUpstreamFailureAudit(t *testing.T) {
	service := riskTestService()
	_, calls := riskTestUser(service, 10)
	if _, err := service.performRiskUserAction(context.Background(), 101, "ban", "reason", "admin", "protected"); riskTestCode(err) != 403 || *calls != 0 {
		t.Fatalf("administrator protection failed: %v", err)
	}
	riskTestUser(service, 1)
	service.risk.setUser = func(context.Context, int64, bool, string) error { return errors.New("private upstream secret") }
	if _, err := service.performRiskUserAction(context.Background(), 101, "ban", "reason", "admin", "failure"); riskTestCode(err) != 502 {
		t.Fatalf("upstream failure reported as success: %v", err)
	}
	actions, err := service.risk.list(context.Background(), "action:", 10)
	if err != nil || len(actions) != 1 || strings.Contains(string(actions[0]), "private upstream secret") {
		t.Fatalf("failure audit missing or leaked private error: %s, %v", actions, err)
	}
	var action riskAction
	_ = json.Unmarshal(actions[0], &action)
	if action.State != "failed" || action.OwnsTransition {
		t.Fatalf("failed action claimed successful ownership: %+v", action)
	}
}

func TestRiskPreparedActionCanResumeOnlyWithUnchangedSnapshot(t *testing.T) {
	service := riskTestService()
	state, calls := riskTestUser(service, 1)
	ctx := context.Background()
	result, err := service.performRiskUserAction(ctx, 101, "ban", "reason", "admin", "resume")
	if err != nil {
		t.Fatal(err)
	}
	record := result["action"].(riskAction)
	record.State, record.OwnsTransition, record.After = "prepared", false, riskUserState{}
	*state = record.Before
	*calls = 0
	if err := service.risk.put(ctx, "action:"+record.ID, record); err != nil {
		t.Fatal(err)
	}
	if _, err := service.performRiskUserAction(ctx, 101, "ban", "reason", "admin", "resume"); err != nil || *calls != 1 {
		t.Fatalf("safe prepared action did not resume: %v calls=%d", err, *calls)
	}
}

func TestRiskLeaseExcludesOtherWorkers(t *testing.T) {
	service := riskTestService()
	ctx := context.Background()
	first, err := service.risk.acquire(ctx, "scan", "first", 100)
	second, secondErr := service.risk.acquire(ctx, "scan", "second", 100)
	if err != nil || secondErr != nil || !first || second {
		t.Fatalf("lease exclusivity failed: %v %v %v %v", first, err, second, secondErr)
	}
	service.risk.release(ctx, "scan", "second")
	if got, _ := service.risk.acquire(ctx, "scan", "second", 100); got {
		t.Fatal("another owner released the lease")
	}
	service.risk.release(ctx, "scan", "first")
	if got, _ := service.risk.acquire(ctx, "scan", "second", 100); !got {
		t.Fatal("released lease could not be acquired")
	}
}

func TestRiskAIActuallyRequestsAndValidatesEvidence(t *testing.T) {
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer hidden-key" {
			t.Errorf("unexpected AI request %s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"{\"risk_level\":\"high\",\"confidence\":0.8,\"evidence_ids\":[\"rule\"],\"reason\":\"review\",\"recommendation\":\"review\"}"}}]}`)
	}))
	defer upstream.Close()
	config := riskAIConfig{BaseURL: upstream.URL, APIKey: "hidden-key", Model: "test", TimeoutSeconds: 2}
	result, err := requestRiskAI(context.Background(), config, riskAssessment{Findings: []riskFinding{{ID: "rule"}}})
	if err != nil || result.Status != "assessed" || requests.Load() != 1 {
		t.Fatalf("AI test did not execute: %+v %v requests=%d", result, err, requests.Load())
	}
	if _, err := requestRiskAI(context.Background(), config, riskAssessment{}); err == nil {
		t.Fatal("invented evidence was accepted")
	}
}

func TestRiskAIRefusesCredentialForwardingRedirects(t *testing.T) {
	var leaked atomic.Bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer destination.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	_, err := requestRiskAI(context.Background(), riskAIConfig{BaseURL: redirect.URL, APIKey: "secret", Model: "test", TimeoutSeconds: 2}, riskAssessment{})
	if err == nil || leaked.Load() {
		t.Fatalf("redirect forwarded credentials: err=%v leaked=%v", err, leaked.Load())
	}
}

func TestRiskDryRunPersistsRealAssessmentWithoutChangingUsers(t *testing.T) {
	service := riskTestService()
	service.config.NetworkHistoryComplete = true
	_, calls := riskTestUser(service, 1)
	now := service.now().Unix()
	config := defaultRiskConfig()
	config.MinRequests, config.MaxRequestsPerMinute, config.ConcurrentTokenIPs = 2, 3, 3
	config.AutoExecute, config.DryRun = true, true
	for i := 0; i < 6; i++ {
		event := networkEvent{ID: fmt.Sprint("dry-", i), Source: "api_call", UserID: 101, TokenID: 1,
			OccurredAt: now - 30, IP: fmt.Sprintf("8.8.8.%d", i%3+1), Result: "success"}
		if err := service.network.Add(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := service.network.Add(context.Background(), networkEvent{ID: fmt.Sprint("baseline-", i), Source: "api_call",
			UserID: 101, TokenID: 1, OccurredAt: now - 4000, IP: "8.8.8.1", Result: "success"}); err != nil {
			t.Fatal(err)
		}
	}
	job, err := service.createRiskScan(context.Background(), config, []int64{101})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.runRiskJob(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	var stored riskScanJob
	_, err = service.risk.get(context.Background(), "job:"+job.ID, &stored)
	if err != nil || stored.State != "complete" || stored.Assessed != 1 || stored.Scanned != 1 || stored.Banned != 0 || *calls != 0 {
		t.Fatalf("dry-run was a placeholder or performed writes: %+v err=%v calls=%d", stored, err, *calls)
	}
	assessments, err := service.risk.list(context.Background(), "assessment:", 10)
	if err != nil || len(assessments) != 1 {
		t.Fatalf("dry-run assessment not durable: %v %v", assessments, err)
	}
	var assessment riskAssessment
	_ = json.Unmarshal(assessments[0], &assessment)
	if assessment.Features.Calls != 6 || !assessment.AutoEligible || !assessment.DryRun {
		t.Fatalf("dry-run did not compute actual evidence: %+v", assessment)
	}
}

func TestRiskClaimedAccountFailuresRemainUnverifiedReviewEvidence(t *testing.T) {
	config := defaultRiskConfig()
	config.LoginFailuresThreshold = 2
	events := []networkEvent{
		{ID: "failure-1", Source: "login", OccurredAt: 120, Result: "failure", ClaimedAccount: "alice"},
		{ID: "failure-2", Source: "login", OccurredAt: 130, Result: "failure", ClaimedAccount: "alice"},
	}
	events = associateRiskAttempts(events, riskUserState{ID: 101, Username: "alice"})
	result := evaluateRiskEvents(101, 100, 200, config, events)
	if result.Features.AttemptedAccountFailures != 2 || result.Features.LoginFailures != 0 ||
		result.AutoEligible || result.ProposedAction != "review" || events[0].UserID != 0 {
		t.Fatalf("claimed account was promoted to verified identity or auto-ban evidence: %+v events=%+v", result, events)
	}
}

func TestRiskScanSnapshotSeparatesUsersAndPreservesClaimedIdentity(t *testing.T) {
	events := []networkEvent{
		{ID: "alice-call", Source: "api_call", UserID: 101, Result: "success", OccurredAt: 120},
		{ID: "bob-call", Source: "api_call", UserID: 102, Result: "success", OccurredAt: 120},
		{ID: "alice-attempt", Source: "login", UserID: 0, Result: "failure", ClaimedAccount: "alice", OccurredAt: 130},
		{ID: "bob-attempt", Source: "login", UserID: 0, Result: "failure", ClaimedAccount: "bob", OccurredAt: 130},
	}
	snapshot := newRiskEventSnapshot(events, map[string]any{"history_complete": true})
	alice := riskUserState{ID: 101, Username: "alice"}
	selected := associateRiskAttempts(snapshot.userEvents(alice), alice)
	if len(selected) != 2 || selected[0].ID != "alice-call" || selected[1].ID != "alice-attempt" ||
		selected[1].UserID != 0 || selected[1].Metadata["attempted_account_id"] != "101" {
		t.Fatalf("snapshot mixed users or promoted claimed identity: %+v", selected)
	}
	if events[2].Metadata != nil || snapshot.byClaim["alice"][0].Metadata != nil {
		t.Fatal("per-user assessment mutated the shared source snapshot")
	}
}

func TestRiskHistoryPaginationReachesOlderRecords(t *testing.T) {
	service := riskTestService()
	ctx := context.Background()
	for index := 0; index < 5; index++ {
		id := fmt.Sprintf("page-%d", index)
		if err := service.risk.put(ctx, "action:"+id, riskAction{ID: id, UserID: 101, State: "verified"}); err != nil {
			t.Fatal(err)
		}
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	registerRiskControlRoutes(router, service, "")
	seen := map[string]bool{}
	for _, offset := range []int{0, 2, 4} {
		request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/risk/ban-records?limit=2&offset=%d", offset), nil)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		var decoded struct {
			Data struct {
				Items      []riskAction `json:"items"`
				Total      int          `json:"total"`
				Offset     int          `json:"offset"`
				HasMore    bool         `json:"has_more"`
				NextOffset *int         `json:"next_offset"`
			} `json:"data"`
		}
		if json.Unmarshal(response.Body.Bytes(), &decoded) != nil || response.Code != http.StatusOK {
			t.Fatalf("history page failed: %d %s", response.Code, response.Body.String())
		}
		if decoded.Data.Total != 5 || decoded.Data.Offset != offset || decoded.Data.HasMore != (offset < 4) {
			t.Fatalf("incorrect page metadata: %s", response.Body.String())
		}
		if offset < 4 && (decoded.Data.NextOffset == nil || *decoded.Data.NextOffset != offset+2) {
			t.Fatalf("page does not point to its successor: %s", response.Body.String())
		}
		if offset == 4 && decoded.Data.NextOffset != nil {
			t.Fatalf("last page has a successor: %s", response.Body.String())
		}
		for _, item := range decoded.Data.Items {
			if seen[item.ID] {
				t.Fatalf("history repeated %s across pages", item.ID)
			}
			seen[item.ID] = true
		}
	}
	if len(seen) != 5 {
		t.Fatalf("older records were inaccessible: %v", seen)
	}
	all, err := service.risk.list(ctx, "action:", 0)
	if err != nil || len(all) != 5 {
		t.Fatalf("public pagination changed internal full-history reads: %v %v", all, err)
	}
}

func TestRiskReconciliationNeverWritesUpstream(t *testing.T) {
	service := riskTestService()
	state, calls := riskTestUser(service, 1)
	ctx := context.Background()
	result, err := service.performRiskUserAction(ctx, 101, "ban", "reason", "admin", "reconcile")
	if err != nil {
		t.Fatal(err)
	}
	record := result["action"].(riskAction)
	record.State, record.OwnsTransition = "uncertain", false
	if err := service.risk.put(ctx, "action:"+record.ID, record); err != nil {
		t.Fatal(err)
	}
	state.AuthVersion++
	gin.SetMode(gin.TestMode)
	router := gin.New()
	registerRiskControlRoutes(router, service, "")
	request := httptest.NewRequest(http.MethodPost, "/api/risk/actions/"+record.ID+"/reconcile",
		strings.NewReader(`{"reason":"inspect interruption"}`))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || *calls != 1 || !strings.Contains(response.Body.String(), `"resolution":"state_conflict"`) {
		t.Fatalf("reconciliation wrote upstream or hid conflict: %d %s calls=%d", response.Code, response.Body.String(), *calls)
	}
	var stored riskAction
	_, _ = service.risk.get(ctx, "action:"+record.ID, &stored)
	if stored.OwnsTransition || stored.State != "conflict" {
		t.Fatalf("reconciliation claimed uncertain ownership: %+v", stored)
	}
	audit, err := service.risk.list(ctx, "audit:", 10)
	if err != nil || len(audit) < 1 {
		t.Fatalf("reconciliation was not audited: %v %v", audit, err)
	}
	foundReconciliation := false
	for _, record := range audit {
		if strings.Contains(string(record), `"event":"action_reconciled"`) {
			foundReconciliation = true
		}
	}
	if !foundReconciliation {
		t.Fatal("reconciliation audit event is missing")
	}
}

func TestRiskAnalysisGETDoesNotRequestPaidAI(t *testing.T) {
	service := riskTestService()
	riskTestUser(service, 1)
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()
	config := defaultRiskConfig()
	config.AI = riskAIConfig{Enabled: true, BaseURL: upstream.URL, APIKey: "secret", Model: "model", TimeoutSeconds: 2}
	if err := service.risk.put(context.Background(), "config", config); err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	registerRiskControlRoutes(router, service, "")
	request := httptest.NewRequest(http.MethodGet, "/api/risk/users/101/analysis", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || requests.Load() != 0 {
		t.Fatalf("GET analysis invoked paid AI: %d %s requests=%d", response.Code, response.Body.String(), requests.Load())
	}
}

func TestRiskHTTPAuthenticationAndSecretRedaction(t *testing.T) {
	service := riskTestService()
	config := defaultRiskConfig()
	config.AI.APIKey = "never-show-this-secret"
	if err := service.risk.put(context.Background(), "config", config); err != nil {
		t.Fatal(err)
	}
	if _, err := service.createRiskScan(context.Background(), config, []int64{101}); err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	registerRiskControlRoutes(router, service, "admin-secret")
	for _, path := range []string{"/api/risk/config", "/api/risk/jobs", "/api/risk/ban-records"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		unauthenticated := httptest.NewRecorder()
		router.ServeHTTP(unauthenticated, request)
		if unauthenticated.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated %s returned %d", path, unauthenticated.Code)
		}
		request = httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer admin-secret")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK || strings.Contains(response.Body.String(), config.AI.APIKey) {
			t.Fatalf("%s leaked credentials or failed: %d %s", path, response.Code, response.Body.String())
		}
	}
}

type riskFailDriver struct{}
type riskFailConn struct{}

func (riskFailDriver) Open(string) (driver.Conn, error) { return riskFailConn{}, nil }
func (riskFailConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("database failed with internal DSN")
}
func (riskFailConn) Close() error              { return nil }
func (riskFailConn) Begin() (driver.Tx, error) { return nil, errors.New("database unavailable") }
func (riskFailConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return nil, errors.New("database failed with internal DSN")
}

var riskRegisterFailDriver sync.Once

func TestRiskPersistenceFailurePreventsUpstreamAction(t *testing.T) {
	riskRegisterFailDriver.Do(func() { sql.Register("risk-failing", riskFailDriver{}) })
	db, err := sql.Open("risk-failing", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	service := riskTestService()
	service.config.Mode = "live"
	service.management = queryDB{db: db, driver: "mysql"}
	_, calls := riskTestUser(service, 1)
	_, err = service.performRiskUserAction(context.Background(), 101, "ban", "reason", "admin", "persist-fail")
	if err == nil || *calls != 0 || strings.Contains(err.Error(), "DSN") {
		t.Fatalf("unrecorded action executed or leaked DB detail: err=%v calls=%d", err, *calls)
	}
}

func TestRiskFinalAuditFailureNeverReportsSuccessAfterUpstreamChanged(t *testing.T) {
	service := riskTestService()
	state, calls := riskTestUser(service, 1)
	service.risk.setUser = func(context.Context, int64, bool, string) error {
		*calls++
		state.Status, state.AuthVersion = 2, state.AuthVersion+1
		// Simulate losing the persistence connection after the external write.
		service.config.Mode = "live"
		service.management = queryDB{}
		return nil
	}
	result, err := service.performRiskUserAction(context.Background(), 101, "ban", "reason", "admin", "audit-disconnect")
	if result != nil || riskTestCode(err) != http.StatusServiceUnavailable || state.Status != 2 || *calls != 1 {
		t.Fatalf("final audit failure became successful action: %v %v state=%+v calls=%d", result, err, state, *calls)
	}
	service.config.Mode = "demo"
	actions, err := service.risk.list(context.Background(), "action:", 10)
	if err != nil || len(actions) != 1 {
		t.Fatalf("prepared action disappeared: %v %v", actions, err)
	}
	var stored riskAction
	_ = json.Unmarshal(actions[0], &stored)
	if stored.State != "executing" || stored.OwnsTransition {
		t.Fatalf("unverified action was finalized: %+v", stored)
	}
}

func TestRiskAutomaticScanUsesCurrentPolicyAndIsResumable(t *testing.T) {
	service := riskTestService()
	service.config.NetworkHistoryComplete = true
	state, calls := riskTestUser(service, 1)
	config := defaultRiskConfig()
	config.MinRequests, config.MaxRequestsPerMinute, config.ConcurrentTokenIPs = 2, 3, 3
	config.AutoExecute, config.DryRun = true, false
	ctx := context.Background()
	if err := service.risk.put(ctx, "config", config); err != nil {
		t.Fatal(err)
	}
	now := service.now().Unix()
	for i := 0; i < 8; i++ {
		at, ip := now-30, fmt.Sprintf("8.8.8.%d", i%3+1)
		if i >= 6 {
			at, ip = now-4000, "8.8.8.1"
		}
		if err := service.network.Add(ctx, networkEvent{ID: fmt.Sprint("auto-", i), Source: "api_call",
			UserID: 101, TokenID: 1, OccurredAt: at, IP: ip, Result: "success",
			Metadata: map[string]string{"quota": "100"}}); err != nil {
			t.Fatal(err)
		}
	}
	job, err := service.createRiskScan(ctx, config, []int64{101})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.runRiskJob(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	if state.Status != 2 || *calls != 1 {
		t.Fatalf("configured scan did not apply verified action: state=%+v calls=%d", state, *calls)
	}
	var stored riskScanJob
	_, _ = service.risk.get(ctx, "job:"+job.ID, &stored)
	if stored.Banned != 1 || stored.State != "complete" {
		t.Fatalf("scan result is incorrect: %+v", stored)
	}
	// Restart from the persisted cursor before this user to exercise replay.
	stored.Cursor, stored.Scanned, stored.Assessed, stored.Banned, stored.State = 0, 0, 0, 0, "running"
	if err := service.risk.put(ctx, "job:"+job.ID, stored); err != nil {
		t.Fatal(err)
	}
	if err := service.runRiskJob(ctx, job.ID); err != nil || *calls != 1 {
		t.Fatalf("resumed scan repeated the upstream action: %v calls=%d", err, *calls)
	}
	assessments, err := service.risk.list(ctx, "assessment:", 10)
	if err != nil || len(assessments) != 1 {
		t.Fatalf("resumed scan repeated assessment records: %v %v", assessments, err)
	}
	var assessment riskAssessment
	_ = json.Unmarshal(assessments[0], &assessment)
	if assessment.Features.CurrentQuota != 600 || assessment.Features.PreviousQuota != 200 || assessment.Features.BurstRatio != 3 {
		t.Fatalf("request/consumption baseline is incorrect: %+v", assessment.Features)
	}
	actions, err := service.risk.list(ctx, "action:", 10)
	if err != nil || len(actions) != 1 {
		t.Fatalf("resumed scan repeated action records: %v %v", actions, err)
	}
	var action riskAction
	_ = json.Unmarshal(actions[0], &action)
	if action.AssessmentID == "" || action.RuleVersion != config.Version || len(action.EvidenceIDs) != 1 {
		t.Fatalf("automatic action has no versioned evidence: %+v", action)
	}
	changed := config
	changed.AutoExecute, changed.Version = false, config.Version+1
	if err := service.risk.put(ctx, "config", changed); err != nil {
		t.Fatal(err)
	}
	state.Status, state.AuthVersion = 1, state.AuthVersion+1
	staleJob, err := service.createRiskScan(ctx, config, []int64{101})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.runRiskJob(ctx, staleJob.ID); err != nil || state.Status != 1 || *calls != 1 {
		t.Fatalf("stale queued policy bypassed current automatic-action switch: %v state=%+v calls=%d", err, state, *calls)
	}
}
