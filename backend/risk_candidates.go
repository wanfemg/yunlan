package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	riskCandidateLookback     = 7 * 24 * time.Hour
	riskCandidateHistoryLimit = 10000
	riskCandidateByteLimit    = 64 << 20
	riskCandidateUserBatch    = 500
)

type riskCandidateRecord struct {
	UserID            int64  `json:"user_id"`
	WindowEnd         int64  `json:"window_end"`
	CreatedAt         int64  `json:"created_at"`
	RuleVersion       int64  `json:"rule_version"`
	Score             int    `json:"score"`
	Level             string `json:"level"`
	Whitelisted       bool   `json:"whitelisted"`
	DetectionRevision int    `json:"detection_revision"`
	savedAt           int64
	key               string
	payload           json.RawMessage
}

func (r riskCandidateRecord) newerThan(other riskCandidateRecord) bool {
	switch {
	case r.WindowEnd != other.WindowEnd:
		return r.WindowEnd > other.WindowEnd
	case r.CreatedAt != other.CreatedAt:
		return r.CreatedAt > other.CreatedAt
	case r.RuleVersion != other.RuleVersion:
		return r.RuleVersion > other.RuleVersion
	case r.savedAt != other.savedAt:
		return r.savedAt > other.savedAt
	default:
		return r.key > other.key
	}
}

func (store *riskControlStore) recentRiskCandidateRecords(ctx context.Context, since, asOf int64, config riskConfig) (map[int64]riskCandidateRecord, bool, error) {
	const prefix = "assessment:"
	if err := store.ensure(ctx); err != nil {
		return nil, false, err
	}
	whitelist := make(map[int64]struct{}, len(config.Whitelist))
	for _, id := range config.Whitelist {
		whitelist[id] = struct{}{}
	}
	latest := make(map[int64]riskCandidateRecord)
	examined, bytesRead := 0, 0
	truncated := false
	visit := func(key string, payload []byte, savedAt int64) (bool, error) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if examined >= riskCandidateHistoryLimit || len(payload) > riskCandidateByteLimit-bytesRead {
			truncated = true
			return false, nil
		}
		examined++
		bytesRead += len(payload)
		var current riskCandidateRecord
		if err := json.Unmarshal(payload, &current); err != nil {
			return false, errors.New("invalid saved risk assessment")
		}
		if current.UserID <= 0 || current.WindowEnd < since || current.WindowEnd > asOf {
			return true, nil
		}
		if _, allowed := whitelist[current.UserID]; allowed {
			return true, nil
		}
		current.savedAt, current.key = savedAt, key
		if previous, found := latest[current.UserID]; !found || current.newerThan(previous) {
			// A later low-risk assessment supersedes older findings too.
			if !current.Whitelisted && current.RuleVersion == config.Version &&
				current.DetectionRevision == riskDetectionRevision &&
				(current.Level == "medium" || current.Level == "high") {
				current.payload = payload
			}
			latest[current.UserID] = current
		}
		return true, nil
	}
	if store.service.config.Mode == "demo" {
		store.mu.Lock()
		defer store.mu.Unlock()
		keys := make([]string, 0)
		for key := range store.docs {
			if updatedAt := store.updated[key]; strings.HasPrefix(key, prefix) && updatedAt >= since && updatedAt <= asOf {
				keys = append(keys, key)
			}
		}
		sort.Slice(keys, func(i, j int) bool {
			if left, right := store.updated[keys[i]], store.updated[keys[j]]; left != right {
				return left > right
			}
			return keys[i] > keys[j]
		})
		for _, key := range keys {
			keepGoing, err := visit(key, store.docs[key], store.updated[key])
			if err != nil {
				return nil, false, err
			}
			if !keepGoing {
				break
			}
		}
		return latest, truncated, nil
	}

	rows, err := store.service.management.query(ctx, `
		SELECT state_key,payload,updated_at FROM siteadmin_risk_state
		WHERE state_key LIKE ? AND updated_at>=? AND updated_at<=?
		ORDER BY updated_at DESC,state_key DESC LIMIT ?`,
		prefix+"%", since, asOf, riskCandidateHistoryLimit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var payload []byte
		var savedAt int64
		if err := rows.Scan(&key, &payload, &savedAt); err != nil {
			return nil, false, err
		}
		keepGoing, err := visit(key, payload, savedAt)
		if err != nil {
			return nil, false, err
		}
		if !keepGoing {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return latest, truncated, nil
}

func (s *dashboardService) activeRiskCandidateIDs(ctx context.Context, ids []int64) (map[int64]bool, error) {
	active := make(map[int64]bool, len(ids))
	if len(ids) == 0 {
		return active, nil
	}
	if s.config.Mode == "demo" {
		users, err := s.managedUsers(ctx, 0)
		if err != nil {
			return nil, err
		}
		for _, user := range users {
			if user.Role < 10 && user.Status == 1 {
				active[user.ID] = true
			}
		}
		return active, nil
	}
	schema, err := s.management.query(ctx, "SELECT deleted_at FROM users WHERE 1 = 0")
	if err != nil {
		// Older deployments may not have an account soft-delete column.
		schema, err = s.management.query(ctx, "SELECT id,role,status FROM users WHERE 1 = 0")
		if err != nil {
			return nil, err
		}
	}
	columns, err := schema.Columns()
	_ = schema.Close()
	if err != nil {
		return nil, err
	}
	predicate := "status = 1 AND role < 10"
	if len(columns) == 1 && strings.EqualFold(columns[0], "deleted_at") {
		predicate += " AND deleted_at IS NULL"
	}
	for start := 0; start < len(ids); start += riskCandidateUserBatch {
		batch := ids[start:min(start+riskCandidateUserBatch, len(ids))]
		query := "SELECT id FROM users WHERE " + predicate + " AND id IN (" +
			strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",") + ")"
		args := make([]any, len(batch))
		for index, id := range batch {
			args[index] = id
		}
		rows, err := s.management.query(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return nil, err
			}
			active[id] = true
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return active, nil
}

func (s *dashboardService) riskCandidatesHTTP(c *gin.Context) {
	limit, err := positiveLogInt(c.Query("limit"), 20, 100)
	if err != nil {
		riskHTTPError(c, riskFailure(http.StatusBadRequest, "limit must be between 1 and 100"))
		return
	}
	offset, err := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if err != nil || offset < 0 || offset > 1000000000 {
		riskHTTPError(c, riskFailure(http.StatusBadRequest, "offset must be between 0 and 1000000000"))
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), queryTimeout)
	defer cancel()
	store := s.riskModule()
	config, err := store.config(ctx)
	if err != nil {
		riskHTTPError(c, err)
		return
	}
	now := store.now()
	since, asOf := now.Add(-riskCandidateLookback).Unix(), now.Unix()
	records, truncated, err := store.recentRiskCandidateRecords(ctx, since, asOf, config)
	if err != nil {
		riskHTTPError(c, err)
		return
	}
	ids := make([]int64, 0)
	for id, record := range records {
		if record.payload != nil {
			ids = append(ids, id)
		}
	}
	active, err := s.activeRiskCandidateIDs(ctx, ids)
	if err != nil {
		riskHTTPError(c, err)
		return
	}
	candidates := make([]riskCandidateRecord, 0, len(active))
	for _, id := range ids {
		if active[id] && !riskAssessmentReviewRejected(records[id].payload) {
			candidates = append(candidates, records[id])
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		switch {
		case left.Score != right.Score:
			return left.Score > right.Score
		case left.WindowEnd != right.WindowEnd:
			return left.WindowEnd > right.WindowEnd
		case left.CreatedAt != right.CreatedAt:
			return left.CreatedAt > right.CreatedAt
		default:
			return left.UserID < right.UserID
		}
	})
	total := len(candidates)
	start, end := min(offset, total), min(offset+limit, total)
	items := make([]json.RawMessage, 0, end-start)
	for _, candidate := range candidates[start:end] {
		items = append(items, candidate.payload)
	}
	items = s.riskRecordUserLabels(ctx, items)
	var nextOffset any
	if end < total {
		nextOffset = end
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
		"items": items, "limit": limit, "offset": offset, "total": total,
		"has_more": end < total, "next_offset": nextOffset,
		"window_start": since, "as_of": asOf, "rule_version": config.Version, "truncated": truncated,
		"detection_revision": riskDetectionRevision,
	}})
}
