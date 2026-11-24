package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

type userLifecycleRecord struct {
	ID        string   `json:"id"`
	Action    string   `json:"action"`
	UserID    int64    `json:"user_id,omitempty"`
	UserIDs   []int64  `json:"user_ids"`
	Usernames []string `json:"usernames"`
	Reason    string   `json:"reason"`
	Operator  string   `json:"operator"`
	State     string   `json:"state"`
	Affected  int64    `json:"affected"`
	CreatedAt int64    `json:"created_at"`
	UpdatedAt int64    `json:"updated_at"`
	Error     string   `json:"error,omitempty"`
	Warning   string   `json:"warning,omitempty"`
}

type userLifecycleContextKey struct{}

func userLifecycleAudit(record userLifecycleRecord) map[string]any {
	return map[string]any{
		"id": "lifecycle-" + record.ID + "-" + record.State, "event": "user_lifecycle_" + record.State,
		"action_id": record.ID, "user_id": record.UserID, "action": record.Action,
		"user_ids": record.UserIDs, "usernames": record.Usernames, "reason": record.Reason,
		"operator": record.Operator, "state": record.State, "affected": record.Affected,
		"created_at": record.UpdatedAt, "error": record.Error, "warning": record.Warning,
	}
}

func lifecyclePersistenceError(err error) error {
	var known *riskError
	if errors.As(err, &known) {
		return &managedUserError{known.Status, known.Message}
	}
	return &managedUserError{http.StatusServiceUnavailable, "用户操作记录暂时不可用，未开始删除。"}
}

func (s *dashboardService) saveUserLifecycle(ctx context.Context, record userLifecycleRecord) error {
	audit := userLifecycleAudit(record)
	return s.riskModule().putMany(ctx, map[string]any{
		"lifecycle:" + record.ID: record, "audit:" + audit["id"].(string): audit,
	})
}

// The completion record and the deletion share the management transaction.
// An interrupted request therefore never leaves a committed SQL deletion with
// a pending operation that could be executed a second time.
func (s *dashboardService) commitUserLifecycle(ctx context.Context, tx *sql.Tx, affected int64) error {
	record, _ := ctx.Value(userLifecycleContextKey{}).(*userLifecycleRecord)
	if record == nil {
		return tx.Commit()
	}
	next := *record
	next.State, next.Affected, next.UpdatedAt = "completed", affected, userClock(s).Unix()
	audit := userLifecycleAudit(next)
	statement := "INSERT INTO siteadmin_risk_state (state_key,payload,updated_at) VALUES (?,?,?) ON CONFLICT (state_key) DO UPDATE SET payload=EXCLUDED.payload,updated_at=EXCLUDED.updated_at"
	if s.management.driver == "mysql" {
		statement = "INSERT INTO siteadmin_risk_state (state_key,payload,updated_at) VALUES (?,?,?) ON DUPLICATE KEY UPDATE payload=VALUES(payload),updated_at=VALUES(updated_at)"
	}
	for key, value := range map[string]any{"lifecycle:" + next.ID: next, "audit:" + audit["id"].(string): audit} {
		payload, err := json.Marshal(value)
		if err != nil {
			return &managedUserError{http.StatusInternalServerError, "用户操作记录编码失败，数据库操作已回滚。"}
		}
		if _, err := tx.ExecContext(ctx, bindQuery(s.management.driver, statement), key, string(payload), next.UpdatedAt); err != nil {
			return &managedUserError{http.StatusServiceUnavailable, "用户操作记录写入失败，数据库操作已回滚。"}
		}
	}
	if err := tx.Commit(); err != nil {
		return &managedUserError{http.StatusServiceUnavailable, "数据库提交结果未确认，请先查看操作记录，不要再次删除。"}
	}
	*record = next
	return nil
}

func (s *dashboardService) performUserLifecycle(ctx context.Context, action string, userID int64, reason, key string) (userLifecycleRecord, error) {
	reason, key = strings.TrimSpace(reason), strings.TrimSpace(key)
	if !utf8.ValidString(reason) || utf8.RuneCountInString(reason) < 1 || utf8.RuneCountInString(reason) > 500 ||
		key == "" || len(key) > 200 {
		return userLifecycleRecord{}, &managedUserError{http.StatusBadRequest, "删除操作必须填写 1–500 字原因并提供 Idempotency-Key。"}
	}
	digest := sha256.Sum256([]byte("user-lifecycle:" + key))
	id := hex.EncodeToString(digest[:])
	store, owner := s.riskModule(), riskID("lifecycle")
	acquired, err := store.acquire(ctx, "lifecycle-key:"+id, owner, 120)
	if err != nil {
		return userLifecycleRecord{}, lifecyclePersistenceError(err)
	}
	if !acquired {
		return userLifecycleRecord{}, &managedUserError{http.StatusConflict, "本次删除正在处理中，请勿重复操作。"}
	}
	defer store.release(context.Background(), "lifecycle-key:"+id, owner)
	var record userLifecycleRecord
	exists, err := store.get(ctx, "lifecycle:"+id, &record)
	if err != nil {
		return record, lifecyclePersistenceError(err)
	}
	if exists {
		if record.Action != action || record.UserID != userID || record.Reason != reason {
			return record, &managedUserError{http.StatusConflict, "该幂等标识已用于其他操作。"}
		}
		if record.State == "completed" {
			return record, nil
		}
		return record, &managedUserError{http.StatusConflict, "该操作已登记，当前状态为「" + record.State + "」。请在操作记录核实，禁止重复执行。"}
	}
	lease := "user:" + strconv.FormatInt(userID, 10)
	if action == "purge_deleted" {
		lease = "user-lifecycle-purge"
	}
	acquired, err = store.acquire(ctx, lease, owner, 120)
	if err != nil {
		return record, lifecyclePersistenceError(err)
	}
	if !acquired {
		return record, &managedUserError{http.StatusConflict, "该用户或清理任务正在执行其他管理操作。"}
	}
	defer store.release(context.Background(), lease, owner)
	now := userClock(s).Unix()
	record = userLifecycleRecord{ID: id, Action: action, UserID: userID, Reason: reason, Operator: "admin",
		State: "pending", CreatedAt: now, UpdatedAt: now, UserIDs: []int64{}, Usernames: []string{}}
	if s.config.Mode != "demo" {
		record.Warning = "数据库操作与审计已保存。此操作不清理历史调用、财务及风控证据；NewAPI Redis 缓存清理无法通过直接数据库操作确认。"
	}
	if err := s.saveUserLifecycle(ctx, record); err != nil {
		return record, lifecyclePersistenceError(err)
	}
	operationCtx := context.WithValue(ctx, userLifecycleContextKey{}, &record)
	var affected int64
	if action == "purge_deleted" {
		affected, err = s.purgeSoftDeletedUsers(operationCtx)
	} else {
		affected, err = s.deleteManagedUser(operationCtx, userID, action == "hard_delete")
	}
	if err != nil {
		// A lost commit acknowledgement is reconciled from the durable record.
		// The original key is never allowed to re-run an uncertain operation.
		verifyCtx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		var persisted userLifecycleRecord
		if found, readErr := store.get(verifyCtx, "lifecycle:"+id, &persisted); readErr == nil && found && persisted.State == "completed" {
			return persisted, nil
		}
		record.State, record.Error, record.UpdatedAt = "needs_review", err.Error(), userClock(s).Unix()
		_ = s.saveUserLifecycle(verifyCtx, record)
		return record, err
	}
	if s.config.Mode == "demo" {
		record.State, record.Affected, record.UpdatedAt = "completed", affected, userClock(s).Unix()
		if err := s.saveUserLifecycle(context.WithoutCancel(ctx), record); err != nil {
			return record, &managedUserError{http.StatusServiceUnavailable, "演示数据已变更，操作记录保存失败，请刷新核实。"}
		}
	}
	return record, nil
}

func (s *dashboardService) userLifecycleAuditHTTP(c *gin.Context) {
	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, sizeErr := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if err != nil || sizeErr != nil || page < 1 || page > 1000000 || pageSize < 1 || pageSize > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "分页参数无效。"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), queryTimeout)
	defer cancel()
	payloads, total, err := s.riskModule().page(ctx, "lifecycle:", (page-1)*pageSize, pageSize)
	if err != nil {
		writeUserManagementError(c, lifecyclePersistenceError(err))
		return
	}
	items := make([]userLifecycleRecord, 0, len(payloads))
	for _, payload := range payloads {
		var record userLifecycleRecord
		if err := json.Unmarshal(payload, &record); err != nil {
			writeUserManagementError(c, &managedUserError{http.StatusServiceUnavailable, "用户操作记录损坏。"})
			return
		}
		items = append(items, record)
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"items": items, "total": total, "page": page, "page_size": pageSize}})
}

func userLifecycleCapture(ctx context.Context, user managedUserRecord) {
	if record, _ := ctx.Value(userLifecycleContextKey{}).(*userLifecycleRecord); record != nil {
		record.UserIDs = append(record.UserIDs, user.ID)
		record.Usernames = append(record.Usernames, user.Username)
	}
}

func (s *dashboardService) lifecycleColumns(ctx context.Context) (map[string]bool, error) {
	rows, err := s.management.query(ctx, "SELECT * FROM users WHERE 1 = 0")
	if err != nil {
		return nil, &managedUserError{http.StatusServiceUnavailable, "无法检查 NewAPI 用户表结构。"}
	}
	columns, err := rows.Columns()
	_ = rows.Close()
	if err != nil {
		return nil, &managedUserError{http.StatusServiceUnavailable, "无法检查 NewAPI 用户表结构。"}
	}
	result := map[string]bool{}
	for _, column := range columns {
		result[strings.ToLower(column)] = true
	}
	return result, nil
}

func (s *dashboardService) lifecycleLockUser(ctx context.Context, tx *sql.Tx, userID int64) (managedUserRecord, error) {
	// SQLite has no SELECT FOR UPDATE; taking its write reservation first
	// prevents a role promotion from racing the protected-account check.
	if s.management.driver != "postgres" && s.management.driver != "mysql" {
		if _, err := tx.ExecContext(ctx, bindQuery(s.management.driver, "UPDATE users SET id = id WHERE id = ?"), userID); err != nil {
			return managedUserRecord{}, &managedUserError{http.StatusConflict, "用户正在被其他操作修改，请刷新后重试。"}
		}
	}
	query := "SELECT id, username, role, status FROM users WHERE id = ?"
	if s.management.driver == "postgres" || s.management.driver == "mysql" {
		query += " FOR UPDATE"
	}
	var user managedUserRecord
	if err := tx.QueryRowContext(ctx, bindQuery(s.management.driver, query), userID).Scan(&user.ID, &user.Username, &user.Role, &user.Status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return user, &managedUserError{http.StatusNotFound, "用户不存在。"}
		}
		return user, &managedUserError{http.StatusServiceUnavailable, "用户锁定失败，未执行删除。"}
	}
	if user.Role >= 10 {
		return user, &managedUserError{http.StatusForbidden, "管理员账号受保护，不能删除。"}
	}
	return user, nil
}

// These are NewAPI authentication records, not usage/finance/risk history.
// Discover known tables before opening the deletion transaction so older
// NewAPI schemas remain supported without swallowing SQL errors.
func (s *dashboardService) lifecycleAuthenticationTables(ctx context.Context, hardDelete bool) ([]string, error) {
	candidates := []string{"user_access_tokens", "user_sessions"}
	if hardDelete {
		candidates = append(candidates, "auth_flows", "passkey_credentials", "two_fa_backup_codes",
			"two_fas", "user_oauth_bindings", "external_identity_claims")
	}
	if s.management.driver != "mysql" && s.management.driver != "postgres" {
		return nil, nil
	}
	scope := "table_schema = current_schema()"
	if s.management.driver == "mysql" {
		scope = "table_schema = DATABASE()"
	}
	args := make([]any, len(candidates))
	for index, candidate := range candidates {
		args[index] = candidate
	}
	query := "SELECT table_name FROM information_schema.columns WHERE " + scope +
		" AND column_name = 'user_id' AND table_name IN (" + strings.TrimSuffix(strings.Repeat("?,", len(candidates)), ",") + ")"
	rows, err := s.management.query(ctx, query, args...)
	if err != nil {
		return nil, &managedUserError{http.StatusServiceUnavailable, "无法检查 NewAPI 登录凭据结构，未执行删除。"}
	}
	defer rows.Close()
	present := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, &managedUserError{http.StatusServiceUnavailable, "NewAPI 登录凭据结构读取失败。"}
		}
		present[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, &managedUserError{http.StatusServiceUnavailable, "NewAPI 登录凭据结构读取失败。"}
	}
	tables := make([]string, 0, len(present))
	for _, name := range candidates {
		if present[name] {
			tables = append(tables, name)
		}
	}
	return tables, nil
}

func (s *dashboardService) deleteLifecycleAuthentication(ctx context.Context, tx *sql.Tx, tables []string, userID int64) error {
	for _, table := range tables {
		query := "DELETE FROM " + tokenIdentifier(s.management.driver, table) + " WHERE user_id = ?"
		if _, err := tx.ExecContext(ctx, bindQuery(s.management.driver, query), userID); err != nil {
			return &managedUserError{http.StatusInternalServerError, "用户登录凭据清理失败，数据库操作已回滚。"}
		}
	}
	return nil
}
