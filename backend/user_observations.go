package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

const userObservationLimit = 20

// This projection deliberately excludes session identifiers, claimed account
// names, peer addresses and arbitrary metadata from stored network events.
type userObservation struct {
	ID         string            `json:"id"`
	Source     string            `json:"source"`
	OccurredAt int64             `json:"occurred_at"`
	IP         string            `json:"ip"`
	Result     string            `json:"result"`
	Method     string            `json:"method,omitempty"`
	BrowserID  string            `json:"browser_id,omitempty"`
	UserAgent  string            `json:"user_agent,omitempty"`
	TokenID    int64             `json:"token_id,omitempty"`
	Device     map[string]string `json:"device"`
	IPInfo     *networkIPInfo    `json:"ip_info"`
	Identity   string            `json:"identity"`
}

type userObservationDevice struct {
	BrowserID         string            `json:"browser_id"`
	FirstSeenAt       int64             `json:"first_seen_at"`
	LastSeenAt        int64             `json:"last_seen_at"`
	LoginCount        int64             `json:"login_count"`
	RegistrationCount int64             `json:"registration_count"`
	Successes         int64             `json:"successes"`
	Failures          int64             `json:"failures"`
	IPs               []string          `json:"ips"`
	Device            map[string]string `json:"device"`
	UserAgent         string            `json:"user_agent,omitempty"`
}

type userObservationTotals struct {
	Login              int64 `json:"login"`
	Call               int64 `json:"call"`
	Registration       int64 `json:"registration"`
	UnverifiedAttempts int64 `json:"unverified_attempts"`
	Devices            int64 `json:"devices"`
}

type userObservationCoverage struct {
	APICall      bool `json:"api_call"`
	Login        bool `json:"login"`
	Registration bool `json:"registration"`
}

type userObservationsData struct {
	UserID             int64                   `json:"user_id"`
	AsOf               int64                   `json:"as_of"`
	WindowStart        int64                   `json:"window_start"`
	WindowEnd          int64                   `json:"window_end"`
	Timezone           string                  `json:"timezone"`
	HistoryComplete    bool                    `json:"history_complete"`
	Coverage           userObservationCoverage `json:"coverage"`
	LatestLogin        *userObservation        `json:"latest_login"`
	LatestCall         *userObservation        `json:"latest_call"`
	LatestRegistration *userObservation        `json:"latest_registration"`
	Logins             []userObservation       `json:"logins"`
	Calls              []userObservation       `json:"calls"`
	Registrations      []userObservation       `json:"registrations"`
	UnverifiedAttempts []userObservation       `json:"unverified_attempts"`
	Devices            []userObservationDevice `json:"devices"`
	Totals             userObservationTotals   `json:"totals"`
	Limit              int                     `json:"limit"`
}

type userObservationDeviceState struct {
	value       userObservationDevice
	ips         map[string]bool
	fieldSeenAt map[string]int64
	userAgentAt int64
}

// These are the exact fields accepted by login-probe/main.go and produced by
// login-probe/probe.js. Browser IDs identify observed browsers, not people.
func userObservationDeviceFields(values map[string]string) map[string]string {
	result := make(map[string]string)
	for _, key := range []string{"browser", "platform", "language", "timezone", "screen", "color_depth", "touch"} {
		if value := values[key]; value != "" {
			result[key] = value
		}
	}
	return result
}

func projectUserObservation(event networkEvent, identity string) userObservation {
	item := userObservation{
		ID: event.ID, Source: event.Source, OccurredAt: event.OccurredAt,
		IP: event.IP, Result: event.Result, Identity: identity, Device: map[string]string{},
	}
	if event.Source == networkSourceCall {
		// API egresses must never become login device addresses or browser
		// fingerprints, even if malformed historical call payloads contain them.
		item.TokenID = event.TokenID
		return item
	}
	item.Method, item.BrowserID, item.UserAgent = event.Method, event.BrowserID, event.UserAgent
	item.Device = userObservationDeviceFields(event.Device)
	return item
}

func userObservationNewer(left, right userObservation) bool {
	if left.OccurredAt != right.OccurredAt {
		return left.OccurredAt > right.OccurredAt
	}
	return left.ID > right.ID
}

func retainRecentUserObservation(items []userObservation, item userObservation) []userObservation {
	position := sort.Search(len(items), func(index int) bool {
		return userObservationNewer(item, items[index])
	})
	if position >= userObservationLimit {
		return items
	}
	items = append(items, userObservation{})
	copy(items[position+1:], items[position:])
	items[position] = item
	if len(items) > userObservationLimit {
		items = items[:userObservationLimit]
	}
	return items
}

func retainLatestUserObservation(current **userObservation, item userObservation) {
	if *current == nil || userObservationNewer(item, **current) {
		value := item
		*current = &value
	}
}

func collectUserObservationDevice(devices map[string]*userObservationDeviceState, item userObservation) {
	if item.BrowserID == "" || item.Source == networkSourceCall || item.Identity != "verified" {
		return
	}
	state := devices[item.BrowserID]
	if state == nil {
		state = &userObservationDeviceState{
			value: userObservationDevice{
				BrowserID: item.BrowserID, FirstSeenAt: item.OccurredAt, LastSeenAt: item.OccurredAt,
				Device: map[string]string{}, IPs: []string{},
			},
			ips: map[string]bool{}, fieldSeenAt: map[string]int64{},
		}
		devices[item.BrowserID] = state
	}
	if item.OccurredAt < state.value.FirstSeenAt {
		state.value.FirstSeenAt = item.OccurredAt
	}
	if item.OccurredAt > state.value.LastSeenAt {
		state.value.LastSeenAt = item.OccurredAt
	}
	if item.Source == networkSourceLogin {
		state.value.LoginCount++
	} else if item.Source == networkSourceRegistration {
		state.value.RegistrationCount++
	}
	switch item.Result {
	case "success":
		state.value.Successes++
	case "failure":
		state.value.Failures++
	}
	if item.IP != "" {
		state.ips[item.IP] = true
	}
	for key, value := range item.Device {
		if item.OccurredAt >= state.fieldSeenAt[key] {
			state.value.Device[key], state.fieldSeenAt[key] = value, item.OccurredAt
		}
	}
	if item.UserAgent != "" && item.OccurredAt >= state.userAgentAt {
		state.value.UserAgent, state.userAgentAt = item.UserAgent, item.OccurredAt
	}
}

// Read only retained events for this account and unassociated authentication
// failures. No SyncCalls or schema initializer runs from this detail endpoint.
func (n *networkStore) readUserObservations(ctx context.Context, userID, start, end int64, visit func(networkEvent)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if n.service.config.Mode == "demo" {
		n.mu.Lock()
		defer n.mu.Unlock()
		for _, event := range n.events {
			if err := ctx.Err(); err != nil {
				return err
			}
			if event.OccurredAt >= start && event.OccurredAt < end &&
				(event.UserID == userID || event.UserID == 0 &&
					(event.Source == networkSourceLogin || event.Source == networkSourceRegistration) &&
					event.Result == "failure") {
				visit(event)
			}
		}
		return nil
	}
	if n.service.management.db == nil {
		return errors.New("network event storage is unavailable")
	}
	rows, err := n.service.management.query(ctx,
		`SELECT payload FROM siteadmin_network_events
		 WHERE occurred_at>=? AND occurred_at<? AND
		 (user_id=? OR (user_id=0 AND source IN ('login','registration') AND result='failure'))`,
		start, end, userID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var payload string
		var event networkEvent
		if err := rows.Scan(&payload); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return err
		}
		visit(event)
	}
	return rows.Err()
}

func (s *dashboardService) userObservationsHTTP(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id < 1 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "user ID must be positive"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), queryTimeout)
	defer cancel()
	user, err := s.managedUser(ctx, id)
	if err != nil {
		writeUserManagementError(c, err)
		return
	}
	if s.network == nil {
		writeUserManagementError(c, &managedUserError{http.StatusServiceUnavailable, "network event collection is not initialized"})
		return
	}
	asOf := userClock(s).Unix()
	data := userObservationsData{
		UserID: id, AsOf: asOf, WindowStart: asOf - 30*86400, WindowEnd: asOf + 1,
		Timezone: userActivityTimezone(s.config.Location), Limit: userObservationLimit,
		// Keep the existing completeness definition scoped to API calls and
		// report the two probe sources separately.
		HistoryComplete: s.config.NetworkHistoryComplete,
		Coverage:        userObservationCoverage{APICall: s.config.NetworkHistoryComplete},
		Logins:          []userObservation{}, Calls: []userObservation{}, Registrations: []userObservation{},
		UnverifiedAttempts: []userObservation{}, Devices: []userObservationDevice{},
	}
	if data.WindowStart < 0 {
		data.WindowStart = 0
	}
	devices := make(map[string]*userObservationDeviceState)
	err = s.network.readUserObservations(ctx, id, data.WindowStart, data.WindowEnd, func(event networkEvent) {
		if event.OccurredAt < data.WindowStart || event.OccurredAt >= data.WindowEnd {
			return
		}
		if user.Username != "" && event.UserID == 0 && event.Result == "failure" && event.ClaimedAccount == user.Username &&
			(event.Source == networkSourceLogin || event.Source == networkSourceRegistration) {
			data.Totals.UnverifiedAttempts++
			data.UnverifiedAttempts = retainRecentUserObservation(data.UnverifiedAttempts,
				projectUserObservation(event, "unverified_account_attempt"))
			return
		}
		if event.UserID != id {
			return
		}
		item := projectUserObservation(event, "verified")
		switch event.Source {
		case networkSourceLogin:
			data.Totals.Login++
			data.Logins = retainRecentUserObservation(data.Logins, item)
			if item.Result == "success" {
				retainLatestUserObservation(&data.LatestLogin, item)
			}
		case networkSourceRegistration:
			data.Totals.Registration++
			data.Registrations = retainRecentUserObservation(data.Registrations, item)
			if item.Result == "success" {
				retainLatestUserObservation(&data.LatestRegistration, item)
			}
		case networkSourceCall:
			data.Totals.Call++
			data.Calls = retainRecentUserObservation(data.Calls, item)
			retainLatestUserObservation(&data.LatestCall, item)
		default:
			return
		}
		collectUserObservationDevice(devices, item)
	})
	if err != nil {
		writeUserManagementError(c, &managedUserError{http.StatusServiceUnavailable, "user observations are unavailable"})
		return
	}
	data.Totals.Devices = int64(len(devices))
	for _, state := range devices {
		for ip := range state.ips {
			state.value.IPs = append(state.value.IPs, ip)
		}
		sort.Strings(state.value.IPs)
		data.Devices = append(data.Devices, state.value)
	}
	sort.Slice(data.Devices, func(left, right int) bool {
		if data.Devices[left].LastSeenAt != data.Devices[right].LastSeenAt {
			return data.Devices[left].LastSeenAt > data.Devices[right].LastSeenAt
		}
		return data.Devices[left].BrowserID < data.Devices[right].BrowserID
	})
	if len(data.Devices) > userObservationLimit {
		data.Devices = data.Devices[:userObservationLimit]
	}

	// Cache each lookup only for this request; failed enrichment is represented
	// by null without hiding the account's observed history. Do not initialize
	// storage from a read-only detail request.
	s.network.mu.Lock()
	canLookup := s.network.ready
	s.network.mu.Unlock()
	lookupContext, stopLookup := context.WithTimeout(ctx, 2*time.Second)
	defer stopLookup()
	ipCache := make(map[string]*networkIPInfo)
	enrich := func(item *userObservation) {
		if item == nil || item.IP == "" || !canLookup {
			return
		}
		if info, found := ipCache[item.IP]; found {
			item.IPInfo = info
			return
		}
		ipCache[item.IP] = nil
		if lookupContext.Err() != nil {
			return
		}
		info, lookupErr := s.network.Lookup(lookupContext, item.IP)
		if lookupErr == nil {
			ipCache[item.IP], item.IPInfo = &info, &info
		}
	}
	for _, items := range [][]userObservation{data.Logins, data.Calls, data.Registrations, data.UnverifiedAttempts} {
		for index := range items {
			enrich(&items[index])
		}
	}
	enrich(data.LatestLogin)
	enrich(data.LatestCall)
	enrich(data.LatestRegistration)
	c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
}
