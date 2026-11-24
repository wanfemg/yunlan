package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

type riskBannedUser struct {
	User       managedUserRecord `json:"user"`
	Ban        *riskAction       `json:"ban"`
	Owned      bool              `json:"owned"`
	LastAction *riskAction       `json:"last_action"`
}

// History remains readable when an account was deleted or its live profile
// cannot be loaded. Add labels to this response only; persisted evidence and
// the user ID recorded at action time are never overwritten.
func (s *dashboardService) riskRecordUserLabels(ctx context.Context, records []json.RawMessage) []json.RawMessage {
	ids := make(map[int64]bool)
	for _, payload := range records {
		var identity struct {
			UserID int64 `json:"user_id"`
		}
		if json.Unmarshal(payload, &identity) == nil && identity.UserID > 0 {
			ids[identity.UserID] = true
		}
	}
	if len(ids) == 0 {
		return records
	}
	users, err := s.managedUsers(ctx, 0)
	if err != nil {
		return records
	}
	labels := make(map[int64]managedUserRecord, len(ids))
	for _, user := range users {
		if ids[user.ID] {
			labels[user.ID] = user
		}
	}
	items := make([]json.RawMessage, 0, len(records))
	for _, payload := range records {
		var item map[string]json.RawMessage
		if json.Unmarshal(payload, &item) != nil {
			items = append(items, payload)
			continue
		}
		var id int64
		if json.Unmarshal(item["user_id"], &id) != nil {
			items = append(items, payload)
			continue
		}
		if user, found := labels[id]; found {
			item["username"], _ = json.Marshal(user.Username)
			item["display_name"], _ = json.Marshal(user.DisplayName)
			if enriched, err := json.Marshal(item); err == nil {
				items = append(items, enriched)
				continue
			}
		}
		items = append(items, payload)
	}
	return items
}

func newerRiskAction(left, right riskAction) bool {
	if left.CreatedAt != right.CreatedAt {
		return left.CreatedAt > right.CreatedAt
	}
	if left.UpdatedAt != right.UpdatedAt {
		return left.UpdatedAt > right.UpdatedAt
	}
	return left.ID > right.ID
}

// Stream existing action documents once and retain only records for accounts
// that are currently disabled. No action query or account query runs per row.
func (store *riskControlStore) bannedUserActions(
	ctx context.Context, users map[int64]managedUserRecord,
) (map[int64]riskAction, map[int64]riskAction, error) {
	active, latest := map[int64]riskAction{}, map[int64]riskAction{}
	if len(users) == 0 {
		return active, latest, nil
	}
	if err := store.ensure(ctx); err != nil {
		return nil, nil, err
	}
	visit := func(payload []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		var action riskAction
		if json.Unmarshal(payload, &action) != nil {
			return errors.New("risk action history is unavailable")
		}
		user, found := users[action.UserID]
		if !found {
			return nil
		}
		if previous, exists := latest[action.UserID]; !exists || newerRiskAction(action, previous) {
			latest[action.UserID] = action
		}
		current := riskUserState{
			ID: user.ID, Username: user.Username, Status: user.Status, Role: user.Role,
			AuthVersion: user.AuthVersion, AuthVersionKnown: user.AuthVersionKnown,
		}
		if action.Action != "ban" || action.State != "verified" || !action.OwnsTransition ||
			action.ReleasedBy != "" || action.After.Status != 2 ||
			!sameRiskUserState(current, action.After) {
			return nil
		}
		if previous, exists := active[action.UserID]; !exists || newerRiskAction(action, previous) {
			active[action.UserID] = action
		}
		return nil
	}
	if store.service.config.Mode == "demo" {
		store.mu.Lock()
		defer store.mu.Unlock()
		for key, payload := range store.docs {
			if strings.HasPrefix(key, "action:") {
				if err := visit(payload); err != nil {
					return nil, nil, err
				}
			}
		}
		return active, latest, nil
	}
	rows, err := store.service.management.query(ctx,
		"SELECT payload FROM siteadmin_risk_state WHERE state_key LIKE ?", "action:%")
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, nil, err
		}
		if err := visit(payload); err != nil {
			return nil, nil, err
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return active, latest, nil
}

func (s *dashboardService) riskBannedUsersHTTP(c *gin.Context) {
	limit, err := positiveLogInt(c.Query("limit"), 20, 100)
	if err != nil {
		riskHTTPError(c, riskFailure(http.StatusBadRequest, "每页数量须为 1–100。"))
		return
	}
	offset, err := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if err != nil || offset < 0 || offset > 1000000000 {
		riskHTTPError(c, riskFailure(http.StatusBadRequest, "分页偏移无效。"))
		return
	}
	search := strings.TrimSpace(c.Query("search"))
	if !validLogFilter(search, 128) {
		riskHTTPError(c, riskFailure(http.StatusBadRequest, "搜索内容过长或无效。"))
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), queryTimeout)
	defer cancel()
	users, err := s.managedUsers(ctx, 0)
	if err != nil {
		riskHTTPError(c, err)
		return
	}
	matching := make(map[int64]managedUserRecord)
	needle := strings.ToLower(search)
	for _, user := range users {
		if user.Status != 2 {
			continue
		}
		text := strings.ToLower(strings.Join([]string{
			strconv.FormatInt(user.ID, 10), user.Username, user.DisplayName, user.Email,
		}, " "))
		if needle == "" || strings.Contains(text, needle) {
			matching[user.ID] = user
		}
	}
	bans, actions, historyErr := s.riskModule().bannedUserActions(ctx, matching)
	items := make([]riskBannedUser, 0, len(matching))
	for _, user := range matching {
		item := riskBannedUser{User: user}
		if historyErr == nil {
			if ban, exists := bans[user.ID]; exists {
				record := ban
				item.Ban, item.Owned = &record, user.Role < 10
			}
			if last, exists := actions[user.ID]; exists {
				record := last
				item.LastAction = &record
			}
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Ban != nil && items[j].Ban == nil {
			return true
		}
		if items[i].Ban == nil && items[j].Ban != nil {
			return false
		}
		if items[i].Ban != nil && items[j].Ban != nil &&
			items[i].Ban.CreatedAt != items[j].Ban.CreatedAt {
			return items[i].Ban.CreatedAt > items[j].Ban.CreatedAt
		}
		return items[i].User.ID < items[j].User.ID
	})
	total := len(items)
	start, end := min(offset, total), min(offset+limit, total)
	var nextOffset any
	if end < total {
		nextOffset = end
	}
	warning := ""
	if historyErr != nil {
		warning = "当前封禁账号已读取，处置历史暂不可用；封禁时间、原因和归属无法确认，暂不开放解封。"
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
		"items": items[start:end], "total": total, "limit": limit, "offset": offset,
		"has_more": end < total, "next_offset": nextOffset, "as_of": s.riskModule().now().Unix(),
		"history_available": historyErr == nil, "history_warning": warning,
	}})
}
