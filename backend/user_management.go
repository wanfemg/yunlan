package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"database/sql"

	"github.com/gin-gonic/gin"
)

type managedUserRecord struct {
	ID               int64  `json:"id"`
	Username         string `json:"username"`
	DisplayName      string `json:"display_name"`
	Email            string `json:"email"`
	Remark           string `json:"remark"`
	RemarkSupported  bool   `json:"remark_supported"`
	Role             int64  `json:"role"`
	Status           int64  `json:"status"`
	Group            string `json:"group"`
	Quota            int64  `json:"quota"`
	UsedQuota        int64  `json:"used_quota"`
	RequestCount     int64  `json:"upstream_request_count"`
	CreatedAt        int64  `json:"created_at,omitempty"`
	CreatedAtKnown   bool   `json:"-"`
	InviterID        int64  `json:"inviter_id,omitempty"`
	LastLoginAt      int64  `json:"upstream_last_login_at,omitempty"`
	AuthVersion      int64  `json:"-"`
	AuthVersionKnown bool   `json:"-"`
	deletedAtKnown   bool
}

type userManagementState struct {
	mu          sync.Mutex
	demo        map[int64]managedUserRecord
	initialized bool
}

func newUserManagementState() *userManagementState {
	return &userManagementState{demo: map[int64]managedUserRecord{}}
}

var userManagementInitMu sync.Mutex

func (s *dashboardService) userState() *userManagementState {
	userManagementInitMu.Lock()
	defer userManagementInitMu.Unlock()
	if s.userManagement == nil {
		s.userManagement = newUserManagementState()
	}
	return s.userManagement
}

type userActivitySummary struct {
	LastAttemptAt    int64  `json:"last_attempt_at,omitempty"`
	LastSuccessAt    int64  `json:"last_success_at,omitempty"`
	ObservedRequests int64  `json:"observed_requests"`
	Requests24h      int64  `json:"requests_24h"`
	Requests7d       int64  `json:"requests_7d"`
	Requests30d      int64  `json:"requests_30d"`
	Successes30d     int64  `json:"successes_30d"`
	Failures30d      int64  `json:"failures_30d"`
	ActiveDays7d     int64  `json:"active_days_7d"`
	ActiveDays30d    int64  `json:"active_days_30d"`
	Activity         string `json:"activity"`
	Intensity7d      string `json:"intensity_7d"`
}

const userActiveDaysBasis = "calendar_days_including_today"

type userLoginSummary struct {
	LastSuccessAt            int64  `json:"last_success_at,omitempty"`
	Successes30d             int64  `json:"successes_30d"`
	Failures30d              int64  `json:"failures_30d"`
	AttemptedAccountFailures int64  `json:"attempted_account_failures"`
	AttemptedAccountIdentity string `json:"attempted_account_identity"`
}

type userManagementItem struct {
	managedUserRecord
	UserActivity userActivitySummary `json:"call_activity"`
	Login        userLoginSummary    `json:"login_activity"`
}

type userManagementFilters struct {
	page, pageSize                          int
	search, group, status, activity         string
	intensity, sort, order                  string
	userID                                  int64
	historyComplete, activityWindowComplete bool
}

type managedUserError struct {
	Status  int
	Message string
}

func (e *managedUserError) Error() string { return e.Message }

func registerUserManagementRoutes(router *gin.Engine, service *dashboardService, adminToken string) {
	group := router.Group("/api/users", requireAdminToken(adminToken))
	group.GET("", service.usersListHTTP)
	group.GET("/activity-stats", service.usersActivityStatsHTTP)
	group.GET("/stats", service.usersActivityStatsHTTP)
	group.GET("/export", service.usersExportHTTP)
	group.GET("/lifecycle-audit", service.userLifecycleAuditHTTP)
	group.GET("/:id", service.userDetailHTTP)
	group.GET("/:id/remark", service.userRemarkHTTP)
	group.PUT("/:id/remark", service.userRemarkUpdateHTTP)
	group.GET("/:id/observations", service.userObservationsHTTP)
	group.POST("/:id/ban", service.userActionHTTP("ban"))
	group.POST("/:id/unban", service.userActionHTTP("unban"))
	group.DELETE("/:id", service.userDeleteHTTP)
	group.GET("/soft-deleted/count", service.softDeletedCountHTTP)
	group.POST("/soft-deleted/purge", service.softDeletedPurgeHTTP)
}

func parseUserManagementFilters(c *gin.Context) (userManagementFilters, error) {
	f := userManagementFilters{
		search: strings.TrimSpace(c.Query("search")), group: strings.TrimSpace(c.Query("group")),
		status: c.DefaultQuery("status", "all"), activity: c.DefaultQuery("activity", "all"),
		intensity: c.DefaultQuery("intensity", "all"), sort: c.DefaultQuery("sort", "id"),
		order: c.DefaultQuery("order", "desc"),
	}
	if !validLogFilter(f.search, 128) || !validLogFilter(f.group, 64) {
		return f, errors.New("invalid user filter")
	}
	var err error
	if f.page, err = positiveLogInt(c.Query("page"), 1, 2147483647); err != nil {
		return f, err
	}
	if f.pageSize, err = positiveLogInt(c.Query("page_size"), 20, 200); err != nil {
		return f, err
	}
	if f.page-1 > (2147483647-f.pageSize)/f.pageSize {
		return f, errors.New("page offset is too large")
	}
	switch f.status {
	case "all", "enabled", "disabled":
	default:
		return f, errors.New("status must be all, enabled or disabled")
	}
	switch f.activity {
	case "all", "daily", "weekly", "monthly", "silent", "dormant", "never", "unknown", "active_7d", "active_30d":
	default:
		return f, errors.New("unsupported call activity")
	}
	switch f.intensity {
	case "all", "none", "low", "regular", "high", "dense":
	default:
		return f, errors.New("unsupported call intensity")
	}
	switch f.sort {
	case "id", "last_attempt_at", "requests_7d", "requests_30d", "quota":
	default:
		return f, errors.New("unsupported user sort")
	}
	if f.order != "asc" && f.order != "desc" {
		return f, errors.New("order must be asc or desc")
	}
	if raw := c.Query("user_id"); raw != "" {
		f.userID, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || f.userID < 1 {
			return f, errors.New("user_id must be positive")
		}
	}
	return f, nil
}

func userClock(s *dashboardService) time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func classifyUserActivity(lastAttempt, upstreamRequests, asOf int64, historyComplete bool) string {
	if lastAttempt <= 0 {
		if historyComplete && upstreamRequests == 0 {
			return "never"
		}
		return "unknown"
	}
	age := asOf - lastAttempt
	switch {
	case age < 0:
		return "unknown"
	case age <= 86400:
		return "daily"
	case age <= 7*86400:
		return "weekly"
	case age <= 30*86400:
		return "monthly"
	case age <= 90*86400:
		return "silent"
	default:
		return "dormant"
	}
}

func classifyCallIntensity(requests int64) string {
	switch {
	case requests <= 0:
		return "none"
	case requests < 10:
		return "low"
	case requests < 100:
		return "regular"
	case requests < 1000:
		return "high"
	default:
		return "dense"
	}
}

// Identity and authentication events never contribute to API call activity.
func summarizeUserActivity(users []managedUserRecord, events []networkEvent, asOf int64, complete bool, locations ...*time.Location) []userManagementItem {
	location := time.UTC
	if len(locations) > 0 && locations[0] != nil {
		location = locations[0]
	}
	localNow := time.Unix(asOf, 0).In(location)
	// Calendar dates are independent of the length of a local day at DST changes.
	today := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, time.UTC)
	todayKey := today.Format("2006-01-02")
	firstDay7d := today.AddDate(0, 0, -6).Format("2006-01-02")
	firstDay30d := today.AddDate(0, 0, -29).Format("2006-01-02")
	items := make([]userManagementItem, len(users))
	positions := make(map[int64]int, len(users))
	claimedPositions := make(map[string][]int, len(users))
	activeDays7d := make(map[int64]map[string]struct{})
	activeDays30d := make(map[int64]map[string]struct{})
	seen := make(map[string]struct{}, len(events))
	for i, user := range users {
		items[i] = userManagementItem{
			managedUserRecord: user,
			Login:             userLoginSummary{AttemptedAccountIdentity: "unverified"},
		}
		positions[user.ID] = i
		if user.Username != "" {
			claimedPositions[user.Username] = append(claimedPositions[user.Username], i)
		}
	}
	for _, event := range events {
		if event.OccurredAt <= 0 || event.OccurredAt > asOf {
			continue
		}
		if event.ID != "" {
			key := event.Source + ":" + event.ID
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
		}
		if event.Source == "login" && event.Result == "failure" && event.UserID == 0 &&
			event.OccurredAt >= asOf-30*86400 {
			if matches := claimedPositions[event.ClaimedAccount]; len(matches) == 1 {
				items[matches[0]].Login.AttemptedAccountFailures++
			}
			continue
		}
		position, exists := positions[event.UserID]
		if !exists {
			continue
		}
		item := &items[position]
		if event.Source == "login" {
			if event.Result == "success" {
				if event.OccurredAt > item.Login.LastSuccessAt {
					item.Login.LastSuccessAt = event.OccurredAt
				}
				if event.OccurredAt >= asOf-30*86400 {
					item.Login.Successes30d++
				}
			} else if event.Result == "failure" && event.OccurredAt >= asOf-30*86400 {
				item.Login.Failures30d++
			}
			continue
		}
		if event.Source != "api_call" || (event.Result != "success" && event.Result != "failure") {
			continue
		}
		activity := &item.UserActivity
		activity.ObservedRequests++
		if event.OccurredAt > activity.LastAttemptAt {
			activity.LastAttemptAt = event.OccurredAt
		}
		if event.Result == "success" && event.OccurredAt > activity.LastSuccessAt {
			activity.LastSuccessAt = event.OccurredAt
		}
		if event.OccurredAt >= asOf-86400 {
			activity.Requests24h++
		}
		if event.OccurredAt >= asOf-7*86400 {
			activity.Requests7d++
		}
		if event.OccurredAt >= asOf-30*86400 {
			activity.Requests30d++
			if event.Result == "success" {
				activity.Successes30d++
			} else {
				activity.Failures30d++
			}
		}
		day := time.Unix(event.OccurredAt, 0).In(location).Format("2006-01-02")
		if day >= firstDay30d && day <= todayKey {
			if activeDays30d[event.UserID] == nil {
				activeDays30d[event.UserID] = map[string]struct{}{}
			}
			activeDays30d[event.UserID][day] = struct{}{}
			if day >= firstDay7d {
				if activeDays7d[event.UserID] == nil {
					activeDays7d[event.UserID] = map[string]struct{}{}
				}
				activeDays7d[event.UserID][day] = struct{}{}
			}
		}
	}
	for i := range items {
		item := &items[i]
		item.UserActivity.ActiveDays7d = int64(len(activeDays7d[item.ID]))
		item.UserActivity.ActiveDays30d = int64(len(activeDays30d[item.ID]))
		item.UserActivity.Activity = classifyUserActivity(item.UserActivity.LastAttemptAt, item.RequestCount, asOf, complete)
		item.UserActivity.Intensity7d = classifyCallIntensity(item.UserActivity.Requests7d)
	}
	return items
}

func filterManagedUsers(items []userManagementItem, f userManagementFilters) []userManagementItem {
	result := make([]userManagementItem, 0, len(items))
	needle := strings.ToLower(f.search)
	for _, item := range items {
		if f.userID > 0 && item.ID != f.userID {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(item.Username+" "+item.DisplayName+" "+item.Email), needle) {
			continue
		}
		if f.group != "" && item.Group != f.group {
			continue
		}
		if f.status == "enabled" && item.Status != 1 || f.status == "disabled" && item.Status != 2 {
			continue
		}
		activity := item.UserActivity.Activity
		matchesActivity := f.activity == "all" || f.activity == activity ||
			(f.activity == "active_7d" && (activity == "daily" || activity == "weekly")) ||
			(f.activity == "active_30d" && (activity == "daily" || activity == "weekly" || activity == "monthly"))
		if !matchesActivity || f.intensity != "all" && item.UserActivity.Intensity7d != f.intensity {
			continue
		}
		result = append(result, item)
	}
	value := func(item userManagementItem) int64 {
		switch f.sort {
		case "last_attempt_at":
			return item.UserActivity.LastAttemptAt
		case "requests_7d":
			return item.UserActivity.Requests7d
		case "requests_30d":
			return item.UserActivity.Requests30d
		case "quota":
			return item.Quota
		default:
			return item.ID
		}
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := value(result[i]), value(result[j])
		if left == right {
			left, right = result[i].ID, result[j].ID
		}
		if f.order == "asc" {
			return left < right
		}
		return left > right
	})
	return result
}

func managedUserStatistics(items []userManagementItem) map[string]any {
	states := map[string]int64{"daily": 0, "weekly": 0, "monthly": 0, "silent": 0, "dormant": 0, "never": 0, "unknown": 0}
	intensity := map[string]int64{"none": 0, "low": 0, "regular": 0, "high": 0, "dense": 0}
	var enabled, disabled, requests24h, requests7d, requests30d, successes, failures, loginUsers30d, attemptedAccountFailures int64
	for _, item := range items {
		states[item.UserActivity.Activity]++
		intensity[item.UserActivity.Intensity7d]++
		if item.Status == 1 {
			enabled++
		} else if item.Status == 2 {
			disabled++
		}
		requests24h += item.UserActivity.Requests24h
		requests7d += item.UserActivity.Requests7d
		requests30d += item.UserActivity.Requests30d
		successes += item.UserActivity.Successes30d
		failures += item.UserActivity.Failures30d
		if item.Login.Successes30d > 0 {
			loginUsers30d++
		}
		attemptedAccountFailures += item.Login.AttemptedAccountFailures
	}
	return map[string]any{
		"total": len(items), "enabled": enabled, "disabled": disabled,
		"activity": states, "intensity_7d": intensity, "active_24h": states["daily"],
		"active_7d":    states["daily"] + states["weekly"],
		"active_30d":   states["daily"] + states["weekly"] + states["monthly"],
		"requests_24h": requests24h, "requests_7d": requests7d, "requests_30d": requests30d,
		"successes_30d": successes, "failures_30d": failures, "login_users_30d": loginUsers30d,
		"attempted_account_failures": attemptedAccountFailures, "attempted_account_identity": "unverified",
	}
}

func demoManagedUserFixtures(now int64) []managedUserRecord {
	users := []managedUserRecord{
		{ID: 1, Username: "admin", DisplayName: "演示管理员", Role: 100, Status: 1, Group: "default"},
		{ID: 101, Username: "demo_alice", DisplayName: "Alice", Role: 1, Status: 1, Group: "default", Quota: 2000000},
		{ID: 102, Username: "demo_bob", DisplayName: "Bob", Role: 1, Status: 1, Group: "standard", Quota: 1000000},
		{ID: 103, Username: "demo_carol", DisplayName: "Carol", Role: 1, Status: 1, Group: "standard", Quota: 500000},
		{ID: 104, Username: "demo_dan", DisplayName: "Dan", Role: 1, Status: 2, Group: "default"},
		{ID: 105, Username: "demo_eve", DisplayName: "Eve", Role: 1, Status: 1, Group: "default"},
		{ID: 106, Username: "demo_frank", DisplayName: "Frank", Role: 1, Status: 1, Group: "default"},
	}
	for index := range users {
		user := &users[index]
		user.CreatedAt, user.CreatedAtKnown = now-180*86400, true
		user.AuthVersion, user.AuthVersionKnown = 1, true
		user.RemarkSupported = true
		user.Email = user.Username + "@example.test"
	}
	users[1].Remark = "已有管理员备注：企业客户，保留跟进记录。"
	users[4].Remark = "管理员已记录：等待人工复核。"
	users[2].InviterID, users[3].InviterID, users[4].InviterID = 1, 1, 1
	groups := []string{"default", "standard", "community", "premium", "default", "standard"}
	for id := int64(107); id <= 160; id++ {
		index, profile := id-107, (id-107)%10
		user := managedUserRecord{
			ID: id, Username: fmt.Sprintf("demo_user_%03d", id), DisplayName: fmt.Sprintf("演示用户 %03d", id),
			Role: 1, Status: 1, Group: groups[index%int64(len(groups))],
			Quota: (id%9 + 1) * 250000, CreatedAt: now - (200+index*3)*86400,
			CreatedAtKnown: true, AuthVersion: 1, AuthVersionKnown: true, RemarkSupported: true,
		}
		if id%11 == 0 {
			user.Remark = "已有管理员备注：已联系用户，等待回复。"
		}
		user.Email = user.Username + "@example.test"
		if id%11 == 0 || id%17 == 0 {
			user.Status = 2
			user.deletedAtKnown = true
		}
		if id%13 == 0 {
			user.Quota = 0
		}
		switch profile {
		case 3:
			user.CreatedAt = now - 14*86400
		case 4:
			user.CreatedAt = now - 21*86400
		case 5:
			user.CreatedAt = now - 45*86400
		case 8:
			user.CreatedAt = now - (index+1)*3600
		case 9:
			// An upstream count without imported calls remains "unknown", not "never".
			user.RequestCount = 137 + id
		}
		if id == 154 || id == 155 {
			user.Status = 1
			user.deletedAtKnown = false
		}
		users = append(users, user)
	}
	return users
}

func (s *dashboardService) managedUsers(ctx context.Context, userID int64) ([]managedUserRecord, error) {
	return s.readManagedUsers(ctx, userID, "")
}

func (s *dashboardService) readManagedUsers(ctx context.Context, userID int64, username string) ([]managedUserRecord, error) {
	if s.config.Mode == "demo" {
		state := s.userState()
		state.mu.Lock()
		defer state.mu.Unlock()
		if !state.initialized && len(state.demo) == 0 {
			for _, user := range demoManagedUserFixtures(userClock(s).Unix()) {
				state.demo[user.ID] = user
			}
		}
		state.initialized = true
		users := make([]managedUserRecord, 0, len(state.demo))
		for _, user := range state.demo {
			if !user.deletedAtKnown && (userID == 0 || user.ID == userID) && (username == "" || user.Username == username) {
				users = append(users, user)
			}
		}
		sort.Slice(users, func(i, j int) bool { return users[i].ID < users[j].ID })
		return users, nil
	}
	if s.management.db == nil {
		return nil, &managedUserError{http.StatusServiceUnavailable, "user management database is not configured"}
	}
	// Only column names are inspected; no password or credential value is read.
	schema, err := s.management.query(ctx, "SELECT * FROM users WHERE 1 = 0")
	if err != nil {
		return nil, &managedUserError{http.StatusServiceUnavailable, "user schema is unavailable"}
	}
	columns, err := schema.Columns()
	_ = schema.Close()
	if err != nil {
		return nil, &managedUserError{http.StatusServiceUnavailable, "user schema is unavailable"}
	}
	has := make(map[string]bool, len(columns))
	for _, column := range columns {
		has[strings.ToLower(column)] = true
	}
	for _, required := range []string{"id", "username", "role", "status"} {
		if !has[required] {
			return nil, &managedUserError{http.StatusServiceUnavailable, "user schema is unsupported"}
		}
	}
	selectColumn := func(column, fallback string) string {
		if !has[column] {
			return fallback
		}
		return "COALESCE(" + tokenIdentifier(s.management.driver, column) + ", " + fallback + ")"
	}
	query := "SELECT id, username, " + selectColumn("display_name", "''") + ", " + selectColumn("email", "''") +
		", role, status, " + selectColumn("group", "'default'") + ", " + selectColumn("quota", "0") +
		", " + selectColumn("used_quota", "0") + ", " + selectColumn("request_count", "0") +
		", " + selectColumn("created_at", "0") + ", " + selectColumn("last_login_at", "0") +
		", " + selectColumn("inviter_id", "0") +
		", " + selectColumn("auth_version", "0") + ", " + selectColumn("remark", "''") + " FROM users WHERE 1 = 1"
	if has["deleted_at"] {
		query += " AND deleted_at IS NULL"
	}
	var args []any
	if userID > 0 {
		query += " AND id = ?"
		args = append(args, userID)
	}
	if username != "" {
		query += " AND username = ?"
		args = append(args, username)
	}
	query += " ORDER BY id"
	rows, err := s.management.query(ctx, query, args...)
	if err != nil {
		return nil, &managedUserError{http.StatusServiceUnavailable, "user query failed"}
	}
	defer rows.Close()
	users := make([]managedUserRecord, 0)
	for rows.Next() {
		var user managedUserRecord
		if err := rows.Scan(&user.ID, &user.Username, &user.DisplayName, &user.Email, &user.Role, &user.Status,
			&user.Group, &user.Quota, &user.UsedQuota, &user.RequestCount, &user.CreatedAt, &user.LastLoginAt,
			&user.InviterID, &user.AuthVersion, &user.Remark); err != nil {
			return nil, &managedUserError{http.StatusServiceUnavailable, "user query failed"}
		}
		user.AuthVersionKnown = has["auth_version"]
		user.RemarkSupported = has["remark"]
		user.deletedAtKnown = has["deleted_at"]
		user.CreatedAtKnown = has["created_at"] && user.CreatedAt > 0
		if username == "" || user.Username == username {
			users = append(users, user)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, &managedUserError{http.StatusServiceUnavailable, "user query failed"}
	}
	return users, nil
}

// A registration response may omit its new user ID. A narrow server timestamp
// match avoids assigning delayed events to an account that reused the username.
func (s *dashboardService) correlateProbeRegistration(ctx context.Context, event networkEvent) (networkEvent, error) {
	if event.Source != "registration" || event.Result != "success" || event.UserID != 0 {
		return event, nil
	}
	metadata := make(map[string]string, len(event.Metadata)+2)
	for key, value := range event.Metadata {
		metadata[key] = value
	}
	event.Metadata = metadata
	event.Metadata["identity_status"] = "uncorrelated"
	delete(event.Metadata, "identity_basis")
	if event.ClaimedAccount == "" || !validLogFilter(event.ClaimedAccount, 128) || event.OccurredAt <= 0 {
		return event, nil
	}
	now := userClock(s).Unix()
	if event.OccurredAt < now-120 || event.OccurredAt > now+5 {
		return event, nil
	}
	users, err := s.readManagedUsers(ctx, 0, event.ClaimedAccount)
	if err != nil {
		return event, err
	}
	if len(users) != 1 {
		return event, nil
	}
	user := users[0]
	if !user.CreatedAtKnown || user.CreatedAt <= 0 || user.CreatedAt < event.OccurredAt-120 || user.CreatedAt > event.OccurredAt+5 {
		return event, nil
	}
	event.UserID = user.ID
	event.Metadata["identity_status"] = "database_time_correlated"
	event.Metadata["identity_basis"] = "username+created_at"
	return event, nil
}

func (s *dashboardService) managedUser(ctx context.Context, userID int64) (managedUserRecord, error) {
	if userID < 1 {
		return managedUserRecord{}, &managedUserError{http.StatusBadRequest, "user ID must be positive"}
	}
	users, err := s.managedUsers(ctx, userID)
	if err != nil {
		return managedUserRecord{}, err
	}
	if len(users) != 1 {
		return managedUserRecord{}, &managedUserError{http.StatusNotFound, "user was not found"}
	}
	return users[0], nil
}

func (s *dashboardService) managedUserItems(ctx context.Context, f userManagementFilters, asOf int64) ([]userManagementItem, error) {
	users, err := s.managedUsers(ctx, f.userID)
	if err != nil {
		return nil, err
	}
	if s.network == nil {
		return nil, &managedUserError{http.StatusServiceUnavailable, "network event collection is not initialized"}
	}
	if _, err := s.network.SyncCalls(ctx); err != nil {
		return nil, &managedUserError{http.StatusServiceUnavailable, "call activity import is incomplete or unavailable"}
	}
	events, err := s.network.Events(ctx, "api_call", 0, asOf+1)
	if err != nil {
		return nil, &managedUserError{http.StatusServiceUnavailable, "call activity is unavailable"}
	}
	logins, err := s.network.Events(ctx, "login", asOf-30*86400, asOf+1)
	if err != nil {
		return nil, &managedUserError{http.StatusServiceUnavailable, "login activity is unavailable"}
	}
	complete, err := s.network.historyCompleteFor(ctx, 0)
	if err != nil {
		return nil, &managedUserError{http.StatusServiceUnavailable, "调用历史覆盖信息读取失败。"}
	}
	items := summarizeUserActivity(users, append(events, logins...), asOf, complete, s.config.Location)
	return filterManagedUsers(items, f), nil
}

func (s *dashboardService) userHistoryCoverage(ctx context.Context, asOf int64) (bool, bool, error) {
	complete, err := s.network.historyCompleteFor(ctx, 0)
	if err != nil {
		return false, false, &managedUserError{http.StatusServiceUnavailable, "调用历史覆盖信息读取失败。"}
	}
	windowComplete, err := s.network.historyCompleteFor(ctx, asOf-30*86400)
	if err != nil {
		return false, false, &managedUserError{http.StatusServiceUnavailable, "近期调用覆盖信息读取失败。"}
	}
	return complete, windowComplete, nil
}

func writeUserManagementError(c *gin.Context, err error) {
	var known *managedUserError
	if errors.As(err, &known) {
		c.JSON(known.Status, gin.H{"success": false, "error": known.Message})
		return
	}
	c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "error": "user management is unavailable"})
}

func (s *dashboardService) userHTTPData(c *gin.Context) ([]userManagementItem, userManagementFilters, int64, bool) {
	f, err := parseUserManagementFilters(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return nil, f, 0, false
	}
	asOf := userClock(s).Unix()
	ctx, cancel := context.WithTimeout(c.Request.Context(), queryTimeout)
	defer cancel()
	items, err := s.managedUserItems(ctx, f, asOf)
	if err != nil {
		writeUserManagementError(c, err)
		return nil, f, asOf, false
	}
	f.historyComplete, f.activityWindowComplete, err = s.userHistoryCoverage(ctx, asOf)
	if err != nil {
		writeUserManagementError(c, err)
		return nil, f, asOf, false
	}
	return items, f, asOf, true
}

func (s *dashboardService) usersListHTTP(c *gin.Context) {
	items, f, asOf, ok := s.userHTTPData(c)
	if !ok {
		return
	}
	total := len(items)
	start := (f.page - 1) * f.pageSize
	if start > total {
		start = total
	}
	end := start + f.pageSize
	if end > total {
		end = total
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
		"items": items[start:end], "total": total, "page": f.page, "page_size": f.pageSize,
		"stats": managedUserStatistics(items), "as_of": asOf, "mode": s.config.Mode,
		"history_complete": f.historyComplete, "activity_window_complete": f.activityWindowComplete,
		"active_days_timezone": userActivityTimezone(s.config.Location),
		"active_days_basis":    userActiveDaysBasis,
		"activity_source":      "api_call", "login_source": "login", "sample_mode": "none",
	}})
}

func userActivityTimezone(location *time.Location) string {
	if location == nil {
		return "UTC"
	}
	return location.String()
}

func (s *dashboardService) usersActivityStatsHTTP(c *gin.Context) {
	items, f, asOf, ok := s.userHTTPData(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
		"stats": managedUserStatistics(items), "as_of": asOf, "mode": s.config.Mode,
		"history_complete": f.historyComplete, "activity_window_complete": f.activityWindowComplete, "activity_source": "api_call",
		"active_days_timezone": userActivityTimezone(s.config.Location), "active_days_basis": userActiveDaysBasis,
	}})
}

func (s *dashboardService) userDetailHTTP(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id < 1 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "user ID must be positive"})
		return
	}
	f, err := parseUserManagementFilters(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": err.Error()})
		return
	}
	f.userID = id
	ctx, cancel := context.WithTimeout(c.Request.Context(), queryTimeout)
	defer cancel()
	asOf := userClock(s).Unix()
	items, err := s.managedUserItems(ctx, f, asOf)
	if err != nil {
		writeUserManagementError(c, err)
		return
	}
	if len(items) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "error": "user was not found"})
		return
	}
	complete, windowComplete, err := s.userHistoryCoverage(ctx, asOf)
	if err != nil {
		writeUserManagementError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"user": items[0], "as_of": asOf,
		"mode": s.config.Mode, "history_complete": complete, "activity_window_complete": windowComplete,
		"active_days_timezone": userActivityTimezone(s.config.Location), "active_days_basis": userActiveDaysBasis}})
}

func safeUserCSV(value string) string {
	if strings.HasPrefix(value, "=") || strings.HasPrefix(value, "+") || strings.HasPrefix(value, "-") ||
		strings.HasPrefix(value, "@") || strings.HasPrefix(value, "\t") || strings.HasPrefix(value, "\r") || strings.HasPrefix(value, "\n") {
		return "'" + value
	}
	return value
}

func (s *dashboardService) usersExportHTTP(c *gin.Context) {
	items, _, asOf, ok := s.userHTTPData(c)
	if !ok {
		return
	}
	var output bytes.Buffer
	writer := csv.NewWriter(&output)
	_ = writer.Write([]string{"id", "username", "display_name", "email", "role", "status", "group",
		"activity", "intensity_7d", "last_attempt_at", "last_success_at", "requests_24h", "requests_7d",
		"requests_30d", "successes_30d", "failures_30d", "login_successes_30d",
		"attempted_account_failures", "attempted_account_identity", "as_of",
		"active_days_7d", "active_days_30d", "active_days_basis", "active_days_timezone"})
	for _, item := range items {
		activity := item.UserActivity
		_ = writer.Write([]string{strconv.FormatInt(item.ID, 10), safeUserCSV(item.Username),
			safeUserCSV(item.DisplayName), safeUserCSV(item.Email), strconv.FormatInt(item.Role, 10),
			strconv.FormatInt(item.Status, 10), safeUserCSV(item.Group), activity.Activity, activity.Intensity7d,
			strconv.FormatInt(activity.LastAttemptAt, 10), strconv.FormatInt(activity.LastSuccessAt, 10),
			strconv.FormatInt(activity.Requests24h, 10), strconv.FormatInt(activity.Requests7d, 10),
			strconv.FormatInt(activity.Requests30d, 10), strconv.FormatInt(activity.Successes30d, 10),
			strconv.FormatInt(activity.Failures30d, 10), strconv.FormatInt(item.Login.Successes30d, 10),
			strconv.FormatInt(item.Login.AttemptedAccountFailures, 10), item.Login.AttemptedAccountIdentity,
			strconv.FormatInt(asOf, 10), strconv.FormatInt(activity.ActiveDays7d, 10),
			strconv.FormatInt(activity.ActiveDays30d, 10), userActiveDaysBasis, userActivityTimezone(s.config.Location)})
	}
	writer.Flush()
	if writer.Error() != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "error": "user export failed"})
		return
	}
	c.Header("Content-Disposition", `attachment; filename="users.csv"`)
	c.Header("X-Statistics-As-Of", strconv.FormatInt(asOf, 10))
	c.Data(http.StatusOK, "text/csv; charset=utf-8", output.Bytes())
}

func (s *dashboardService) userActionHTTP(action string) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil || userID < 1 {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "user ID must be positive"})
			return
		}
		var body struct {
			Reason string `json:"reason"`
			Note   string `json:"note"`
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8192)
		decoder := json.NewDecoder(c.Request.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "a JSON reason is required"})
			return
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "request must contain a single JSON object"})
			return
		}
		body.Reason = strings.TrimSpace(body.Reason)
		body.Note = strings.TrimSpace(body.Note)
		key := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
		if body.Reason == "" || !validLogFilter(body.Reason, 500) || key == "" || !validLogFilter(key, 128) {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "reason and Idempotency-Key are required"})
			return
		}
		var noteText []string
		if action == "ban" {
			noteText = []string{body.Note}
		} else if body.Note != "" {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "备注仅用于封禁记录。"})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
		defer cancel()
		result, err := s.performRiskUserAction(ctx, userID, action, body.Reason, "admin", key, noteText...)
		if err != nil {
			riskHTTPError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
	}
}

func (s *dashboardService) userDeleteHTTP(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || userID < 1 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "user ID must be positive"})
		return
	}
	var body struct {
		ConfirmText string `json:"confirm_text"`
		Reason      string `json:"reason"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "confirmation text is required"})
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "request must contain a single JSON object"})
		return
	}
	hardDelete := c.DefaultQuery("hard_delete", "false") == "true"
	expected := "注销用户"
	if hardDelete {
		expected = "彻底删除"
	}
	if strings.TrimSpace(body.ConfirmText) != expected {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "confirmation text does not match the requested operation"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	action := "soft_delete"
	if hardDelete {
		action = "hard_delete"
	}
	result, err := s.performUserLifecycle(ctx, action, userID, body.Reason, c.GetHeader("Idempotency-Key"))
	if err != nil {
		writeUserManagementError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
}

func (s *dashboardService) deleteManagedUser(ctx context.Context, userID int64, hardDelete bool) (int64, error) {
	if s.config.Mode == "demo" {
		tokens := s.demoTokenState()
		state := s.userState()
		state.mu.Lock()
		defer state.mu.Unlock()
		latest, exists := state.demo[userID]
		if !exists {
			return 0, &managedUserError{http.StatusNotFound, "用户不存在。"}
		}
		if latest.Role >= 10 {
			return 0, &managedUserError{http.StatusForbidden, "管理员账号受保护，不能删除。"}
		}
		if latest.deletedAtKnown && !hardDelete {
			return 0, &managedUserError{http.StatusConflict, "该用户已经注销。"}
		}
		userLifecycleCapture(ctx, latest)
		tokens.mu.Lock()
		defer tokens.mu.Unlock()
		for id, token := range tokens.items {
			if token.UserID != userID {
				continue
			}
			if hardDelete {
				delete(tokens.items, id)
			} else {
				token.UserStatus = 2
				if token.Status == 1 {
					token.Status = 2
				}
				tokens.items[id] = token
			}
		}
		if hardDelete {
			delete(state.demo, userID)
		} else {
			latest.Status, latest.deletedAtKnown = 2, true
			latest.AuthVersion++
			state.demo[userID] = latest
		}
		return 1, nil
	}
	if s.management.db == nil {
		return 0, &managedUserError{http.StatusServiceUnavailable, "management database is not configured"}
	}
	columns, err := s.lifecycleColumns(ctx)
	if err != nil {
		return 0, err
	}
	if !hardDelete && !columns["deleted_at"] {
		return 0, &managedUserError{http.StatusConflict, "当前 NewAPI 用户表没有 deleted_at 字段，无法注销。"}
	}
	authenticationTables, err := s.lifecycleAuthenticationTables(ctx, hardDelete)
	if err != nil {
		return 0, err
	}
	// Use NewAPI's management flow to revoke sessions and invalidate the user
	// cache before removing an enabled account. Failure stops deletion.
	var status, role int64
	if err := s.management.queryRow(ctx, "SELECT status, role FROM users WHERE id = ?", userID).Scan(&status, &role); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, &managedUserError{http.StatusNotFound, "用户不存在。"}
		}
		return 0, &managedUserError{http.StatusServiceUnavailable, "用户状态读取失败。"}
	}
	if role >= 10 {
		return 0, &managedUserError{http.StatusForbidden, "管理员账号受保护，不能删除。"}
	}
	if status == 1 {
		if err := s.setManagedUserStatus(ctx, userID, false, "用户注销或彻底删除前停用"); err != nil {
			return 0, err
		}
	}
	tx, err := s.management.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, &managedUserError{http.StatusServiceUnavailable, "user delete transaction could not start"}
	}
	defer func() { _ = tx.Rollback() }()
	current, err := s.lifecycleLockUser(ctx, tx, userID)
	if err != nil {
		return 0, err
	}
	if current.Status != 2 {
		return 0, &managedUserError{http.StatusConflict, "账号状态发生变化，未执行删除。"}
	}
	userLifecycleCapture(ctx, current)
	if err := s.deleteLifecycleAuthentication(ctx, tx, authenticationTables, userID); err != nil {
		return 0, err
	}
	if hardDelete {
		if _, err := tx.ExecContext(ctx, bindQuery(s.management.driver, "DELETE FROM tokens WHERE user_id = ?"), userID); err != nil {
			return 0, &managedUserError{http.StatusInternalServerError, "user tokens could not be deleted"}
		}
		result, err := tx.ExecContext(ctx, bindQuery(s.management.driver, "DELETE FROM users WHERE id = ? AND role < 10"), userID)
		if err != nil {
			return 0, &managedUserError{http.StatusInternalServerError, "user could not be deleted"}
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return 0, &managedUserError{http.StatusInternalServerError, "用户删除结果无法确认，数据库操作已回滚。"}
		}
		if affected == 0 {
			return 0, &managedUserError{http.StatusNotFound, "user was not found"}
		}
		if err := s.commitUserLifecycle(ctx, tx, affected); err != nil {
			return 0, err
		}
		return affected, nil
	}
	if _, err := tx.ExecContext(ctx, bindQuery(s.management.driver, "UPDATE tokens SET status = 2 WHERE user_id = ? AND status = 1"), userID); err != nil {
		return 0, &managedUserError{http.StatusInternalServerError, "用户令牌停用失败，数据库操作已回滚。"}
	}
	update := "UPDATE users SET deleted_at = ?, status = 2"
	if columns["auth_version"] {
		update += ", auth_version = auth_version + 1"
	}
	update += " WHERE id = ? AND deleted_at IS NULL AND role < 10"
	result, err := tx.ExecContext(ctx, bindQuery(s.management.driver, update), userClock(s), userID)
	if err != nil {
		return 0, &managedUserError{http.StatusInternalServerError, "user could not be soft deleted"}
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, &managedUserError{http.StatusInternalServerError, "注销结果无法确认，数据库操作已回滚。"}
	}
	if affected == 0 {
		return 0, &managedUserError{http.StatusNotFound, "user was not found or was already deleted"}
	}
	if err := s.commitUserLifecycle(ctx, tx, affected); err != nil {
		return 0, err
	}
	return affected, nil
}

type softDeletedPreview struct {
	Count  int64    `json:"count"`
	Users  []string `json:"users"`
	Sample bool     `json:"sample"`
}

func (s *dashboardService) softDeletedData(ctx context.Context) (softDeletedPreview, error) {
	if s.config.Mode == "demo" {
		if _, err := s.managedUsers(ctx, 0); err != nil {
			return softDeletedPreview{}, err
		}
		state := s.userState()
		state.mu.Lock()
		defer state.mu.Unlock()
		users := make([]string, 0)
		for _, user := range state.demo {
			if user.deletedAtKnown && user.Role < 10 {
				users = append(users, user.Username)
			}
		}
		sort.Strings(users)
		total := int64(len(users))
		if len(users) > 20 {
			users = users[:20]
		}
		return softDeletedPreview{Count: total, Users: users, Sample: total > int64(len(users))}, nil
	}
	if s.management.db == nil {
		return softDeletedPreview{}, &managedUserError{http.StatusServiceUnavailable, "management database is not configured"}
	}
	var count int64
	if err := s.management.queryRow(ctx, "SELECT COUNT(*) FROM users WHERE deleted_at IS NOT NULL AND role < 10").Scan(&count); err != nil {
		return softDeletedPreview{}, &managedUserError{http.StatusServiceUnavailable, "soft deleted users could not be counted"}
	}
	users := make([]string, 0, min(count, 20))
	rows, err := s.management.query(ctx, "SELECT username FROM users WHERE deleted_at IS NOT NULL AND role < 10 ORDER BY deleted_at DESC, id DESC LIMIT 20")
	if err != nil {
		return softDeletedPreview{}, &managedUserError{http.StatusServiceUnavailable, "soft deleted users could not be listed"}
	}
	defer rows.Close()
	for rows.Next() {
		var username sql.NullString
		if err := rows.Scan(&username); err != nil {
			return softDeletedPreview{}, &managedUserError{http.StatusServiceUnavailable, "soft deleted user could not be read"}
		}
		users = append(users, username.String)
	}
	if err := rows.Err(); err != nil {
		return softDeletedPreview{}, &managedUserError{http.StatusServiceUnavailable, "soft deleted users could not be read"}
	}
	return softDeletedPreview{Count: count, Users: users, Sample: count > int64(len(users))}, nil
}

func (s *dashboardService) softDeletedCountHTTP(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), queryTimeout)
	defer cancel()
	data, err := s.softDeletedData(ctx)
	if err != nil {
		writeUserManagementError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"count": data.Count}})
}

func (s *dashboardService) softDeletedPurgeHTTP(c *gin.Context) {
	var body struct {
		DryRun      bool   `json:"dry_run"`
		ConfirmText string `json:"confirm_text"`
		Reason      string `json:"reason"`
	}
	body.DryRun = true
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "request body is required"})
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "request must contain a single JSON object"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	if body.DryRun {
		data, err := s.softDeletedData(ctx)
		if err != nil {
			writeUserManagementError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
		return
	}
	if strings.TrimSpace(body.ConfirmText) != "彻底删除" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "confirmation text does not match the requested operation"})
		return
	}
	result, err := s.performUserLifecycle(ctx, "purge_deleted", 0, body.Reason, c.GetHeader("Idempotency-Key"))
	if err != nil {
		writeUserManagementError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
}

func (s *dashboardService) purgeSoftDeletedUsers(ctx context.Context) (int64, error) {
	if s.config.Mode == "demo" {
		tokens := s.demoTokenState()
		state := s.userState()
		state.mu.Lock()
		defer state.mu.Unlock()
		tokens.mu.Lock()
		defer tokens.mu.Unlock()
		affected := int64(0)
		for id, user := range state.demo {
			if user.deletedAtKnown && user.Role < 10 {
				userLifecycleCapture(ctx, user)
				for tokenID, token := range tokens.items {
					if token.UserID == id {
						delete(tokens.items, tokenID)
					}
				}
				delete(state.demo, id)
				affected++
			}
		}
		return affected, nil
	}
	if s.management.db == nil {
		return 0, &managedUserError{http.StatusServiceUnavailable, "management database is not configured"}
	}
	authenticationTables, err := s.lifecycleAuthenticationTables(ctx, true)
	if err != nil {
		return 0, err
	}
	tx, err := s.management.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, &managedUserError{http.StatusServiceUnavailable, "soft delete purge transaction could not start"}
	}
	defer func() { _ = tx.Rollback() }()
	if s.management.driver != "postgres" && s.management.driver != "mysql" {
		if _, err := tx.ExecContext(ctx, "UPDATE users SET id = id WHERE deleted_at IS NOT NULL AND role < 10"); err != nil {
			return 0, &managedUserError{http.StatusConflict, "注销账号正被其他操作修改。"}
		}
	}
	query := "SELECT id, username, role, status FROM users WHERE deleted_at IS NOT NULL AND role < 10 ORDER BY id"
	if s.management.driver == "postgres" || s.management.driver == "mysql" {
		query += " FOR UPDATE"
	}
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return 0, &managedUserError{http.StatusServiceUnavailable, "无法锁定已注销用户，未执行清理。"}
	}
	var users []managedUserRecord
	for rows.Next() {
		var user managedUserRecord
		if err := rows.Scan(&user.ID, &user.Username, &user.Role, &user.Status); err != nil {
			_ = rows.Close()
			return 0, &managedUserError{http.StatusServiceUnavailable, "已注销用户读取失败。"}
		}
		users = append(users, user)
	}
	readErr := rows.Err()
	_ = rows.Close()
	if readErr != nil {
		return 0, &managedUserError{http.StatusServiceUnavailable, "已注销用户读取失败。"}
	}
	var affected int64
	for _, user := range users {
		userLifecycleCapture(ctx, user)
		if err := s.deleteLifecycleAuthentication(ctx, tx, authenticationTables, user.ID); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, bindQuery(s.management.driver, "DELETE FROM tokens WHERE user_id = ?"), user.ID); err != nil {
			return 0, &managedUserError{http.StatusInternalServerError, "注销用户令牌清理失败，数据库操作已回滚。"}
		}
		result, err := tx.ExecContext(ctx, bindQuery(s.management.driver, "DELETE FROM users WHERE id = ? AND deleted_at IS NOT NULL AND role < 10"), user.ID)
		if err != nil {
			return 0, &managedUserError{http.StatusInternalServerError, "注销用户清理失败，数据库操作已回滚。"}
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return 0, &managedUserError{http.StatusConflict, "注销用户状态发生变化，数据库操作已回滚。"}
		}
		affected += count
	}
	if err := s.commitUserLifecycle(ctx, tx, affected); err != nil {
		return 0, err
	}
	return affected, nil
}

func (s *dashboardService) setManagedUserStatus(ctx context.Context, userID int64, enabled bool, reason string) error {
	current, err := s.managedUser(ctx, userID)
	if err != nil {
		return err
	}
	if current.Role >= 10 {
		return &managedUserError{http.StatusForbidden, "administrator accounts cannot be changed by risk management"}
	}
	wanted, action := int64(2), "disable"
	if enabled {
		wanted, action = 1, "enable"
	}
	if current.Status == wanted {
		return nil
	}
	if s.config.Mode == "demo" {
		state := s.userState()
		state.mu.Lock()
		defer state.mu.Unlock()
		latest, exists := state.demo[userID]
		if !exists || latest.Status != current.Status || latest.AuthVersion != current.AuthVersion {
			return &managedUserError{http.StatusConflict, "user status changed concurrently"}
		}
		latest.Status = wanted
		latest.AuthVersion++
		state.demo[userID] = latest
		return nil
	}
	if s.config.NewAPIUpstream == nil || s.config.NewAPIAdminToken == "" || s.config.NewAPIAdminUserID < 1 {
		return &managedUserError{http.StatusServiceUnavailable, "NewAPI management connection is not configured"}
	}
	upstream := *s.config.NewAPIUpstream
	upstream.Path = path.Join(strings.TrimRight(upstream.Path, "/"), "api/user/manage")
	upstream.RawPath, upstream.RawQuery, upstream.Fragment = "", "", ""
	payload, _ := json.Marshal(map[string]any{"id": userID, "action": action})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, upstream.String(), bytes.NewReader(payload))
	if err != nil {
		return &managedUserError{http.StatusServiceUnavailable, "NewAPI management request could not be created"}
	}
	request.Header.Set("Authorization", "Bearer "+s.config.NewAPIAdminToken)
	request.Header.Set("New-Api-User", strconv.FormatInt(s.config.NewAPIAdminUserID, 10))
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	response, err := client.Do(request)
	if err != nil {
		return &managedUserError{http.StatusBadGateway, "NewAPI management request failed"}
	}
	defer response.Body.Close()
	var result struct {
		Success bool `json:"success"`
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &managedUserError{http.StatusBadGateway, "NewAPI rejected the management request"}
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil || !result.Success {
		return &managedUserError{http.StatusBadGateway, "NewAPI did not confirm the management action"}
	}
	after, err := s.managedUser(ctx, userID)
	if err != nil || after.Status != wanted {
		return &managedUserError{http.StatusBadGateway, "NewAPI user state could not be verified"}
	}
	if current.AuthVersionKnown && after.AuthVersionKnown && after.AuthVersion <= current.AuthVersion {
		return &managedUserError{http.StatusBadGateway, "NewAPI authentication invalidation could not be verified"}
	}
	return nil
}
