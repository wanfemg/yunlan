package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func probeIngestRequest(t *testing.T, s *dashboardService, event map[string]any, offset int64, signingSecret string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	stamp := strconv.FormatInt(s.now().Unix()+offset, 10)
	mac := hmac.New(sha256.New, []byte(signingSecret))
	_, _ = mac.Write([]byte(stamp + "\n/internal/probe/events\n"))
	_, _ = mac.Write(body)
	request := httptest.NewRequest(http.MethodPost, "/internal/probe/events", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Probe-Timestamp", stamp)
	request.Header.Set("X-Probe-Signature", hex.EncodeToString(mac.Sum(nil)))
	response := httptest.NewRecorder()
	newRouter(s.config, s).ServeHTTP(response, request)
	return response
}

func TestProbeIngestAuthenticatesAndSeparatesSources(t *testing.T) {
	for _, tc := range []struct {
		name, source, secret string
		offset               int64
		status               int
	}{
		{"login", "login", strings.Repeat("a", 32), 0, http.StatusOK},
		{"registration", "registration", strings.Repeat("a", 32), 0, http.StatusOK},
		{"cannot forge calls", "api_call", strings.Repeat("a", 32), 0, http.StatusBadRequest},
		{"wrong secret", "login", strings.Repeat("b", 32), 0, http.StatusUnauthorized},
		{"expired", "login", strings.Repeat("a", 32), -301, http.StatusUnauthorized},
		{"future", "login", strings.Repeat("a", 32), 301, http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := redemptionTestService("demo")
			s.config.ProbeSharedSecret = strings.Repeat("a", 32)
			event := map[string]any{"id": "probe:test", "source": tc.source, "occurred_at": s.now().Unix(),
				"user_id": 42, "ip": "8.8.8.8", "result": "success"}
			response := probeIngestRequest(t, s, event, tc.offset, tc.secret)
			if response.Code != tc.status {
				t.Fatalf("response %d %s, want %d", response.Code, response.Body.String(), tc.status)
			}
			if tc.status == http.StatusOK {
				if again := probeIngestRequest(t, s, event, tc.offset, tc.secret); again.Code != http.StatusOK {
					t.Fatalf("idempotent retry failed: %d %s", again.Code, again.Body.String())
				}
				events, err := s.network.Snapshot(t.Context())
				if err != nil || len(events) != 1 || events[0].Source != tc.source {
					t.Fatalf("duplicate or wrong-source events: %+v %v", events, err)
				}
			}
		})
	}
}

func TestProbeIngestRejectsCredentialsAndUnverifiedCompletedLogin(t *testing.T) {
	for _, extra := range []map[string]any{
		{"password": "must-not-persist"},
		{"user_id": 0},
		{"token_id": 17},
		{"device": map[string]string{"access_token": "must-not-persist"}},
	} {
		s := redemptionTestService("demo")
		s.config.ProbeSharedSecret = strings.Repeat("a", 32)
		event := map[string]any{"id": "probe:test", "source": "login", "occurred_at": s.now().Unix(),
			"user_id": 42, "ip": "8.8.8.8", "result": "success"}
		for k, v := range extra {
			event[k] = v
		}
		response := probeIngestRequest(t, s, event, 0, s.config.ProbeSharedSecret)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("unsafe event accepted: %d %s", response.Code, response.Body.String())
		}
		events, err := s.network.Snapshot(t.Context())
		if err != nil || len(events) != 0 {
			t.Fatalf("rejected observation persisted: %+v %v", events, err)
		}
	}
}

func TestNewModuleConfigurationValidation(t *testing.T) {
	for _, tc := range []struct {
		env map[string]string
		bad bool
	}{
		{map[string]string{"NEWAPI_UPSTREAM": "http://127.0.0.1:3000"}, false},
		{map[string]string{"NEWAPI_UPSTREAM": "https://host/api"}, true},
		{map[string]string{"NEWAPI_UPSTREAM": "https://user:secret@host"}, true},
		{map[string]string{"NEWAPI_ADMIN_TOKEN": "admin"}, true},
		{map[string]string{"NEWAPI_ADMIN_TOKEN": "admin", "NEWAPI_UPSTREAM": "http://127.0.0.1:3000", "NEWAPI_ADMIN_USER_ID": "1"}, false},
		{map[string]string{"NEWAPI_ADMIN_USER_ID": "-1"}, true},
		{map[string]string{"PROBE_SHARED_SECRET": "short"}, true},
		{map[string]string{"PROBE_SHARED_SECRET": strings.Repeat("a", 32)}, false},
		{map[string]string{"NETWORK_HISTORY_COMPLETE": "unknown"}, true},
		{map[string]string{"NETWORK_HISTORY_COMPLETE": "true"}, false},
	} {
		_, err := loadConfig(func(name string) string { return tc.env[name] })
		if (err != nil) != tc.bad {
			t.Fatalf("validation = %v, want bad=%v", err, tc.bad)
		}
	}
}

func TestProbeReplayUsesOriginalRegistrationBinding(t *testing.T) {
	s := redemptionTestService("demo")
	s.config.ProbeSharedSecret = strings.Repeat("a", 32)
	event := map[string]any{"id": "probe:registration", "source": "registration", "occurred_at": s.now().Unix(),
		"user_id": 0, "claimed_account": "someone", "ip": "8.8.8.8", "result": "success"}
	first := probeIngestRequest(t, s, event, 0, s.config.ProbeSharedSecret)
	if first.Code != http.StatusOK {
		t.Fatalf("first registration: %d %s", first.Code, first.Body.String())
	}
	stored, found, err := s.network.FindEvent(t.Context(), "registration", "probe:registration")
	if err != nil || !found || stored.Metadata["probe_body_digest"] == "" {
		t.Fatalf("original registration missing: %+v %v", stored, err)
	}
	second := probeIngestRequest(t, s, event, 0, s.config.ProbeSharedSecret)
	if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), `"replayed":true`) {
		t.Fatalf("registration retry did not reuse original event: %d %s", second.Code, second.Body.String())
	}
	event["claimed_account"] = "different"
	conflict := probeIngestRequest(t, s, event, 0, s.config.ProbeSharedSecret)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("changed replay accepted: %d %s", conflict.Code, conflict.Body.String())
	}
}
