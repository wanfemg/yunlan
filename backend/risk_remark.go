package main

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

// Note is the immutable explanation supplied with a ban. RemarkSync tracks the
// separate write to NewAPI's administrator-only users.remark field.
type riskRemarkSync struct {
	State    string `json:"state"`
	Before   string `json:"before"`
	After    string `json:"after"`
	Error    string `json:"error,omitempty"`
	SyncedAt int64  `json:"synced_at,omitempty"`
}

func (s *dashboardService) prepareRiskBanRemark(ctx context.Context, userID int64, note string) (*riskRemarkSync, error) {
	user, err := s.readManagedUserRemark(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !user.RemarkSupported {
		return nil, riskFailure(http.StatusConflict, "当前 NewAPI 数据库没有用户备注字段，未执行封禁；清空追加备注后可单独封禁。")
	}
	remark := user.Remark
	if remark != "" && !strings.HasSuffix(remark, "\n") {
		remark += "\n"
	}
	remark += "[风控封禁] " + note
	if !utf8.ValidString(remark) || utf8.RuneCountInString(remark) > managedUserRemarkLimit {
		return nil, riskFailure(http.StatusBadRequest, "原备注与追加内容合计超过 NewAPI 的 255 字限制，未执行封禁；请缩短追加内容或先编辑用户备注。")
	}
	return &riskRemarkSync{State: "pending", Before: user.Remark, After: remark}, nil
}

func publicRemarkError(err error) string {
	var userError *managedUserError
	if errors.As(err, &userError) {
		return userError.Message
	}
	var operationError *riskError
	if errors.As(err, &operationError) {
		return operationError.Message
	}
	return "NewAPI 用户备注写入或回读未完成，请读取当前备注后重试。"
}

// Caller holds the account lease. The before/after plan is already durable when
// this follows a status change, so a retry can confirm an interrupted write
// without ever appending the same text twice.
func (s *dashboardService) syncRiskActionRemark(ctx context.Context, record *riskAction) {
	sync := record.RemarkSync
	if record.Action != "ban" || sync == nil || sync.State == "synced" {
		return
	}
	sync.State, sync.Error = "failed", ""
	user, err := s.readManagedUserRemark(ctx, record.UserID)
	switch {
	case err != nil:
		sync.Error = publicRemarkError(err)
		return
	case !user.RemarkSupported:
		sync.Error = "当前 NewAPI 数据库没有用户备注字段。"
		return
	case user.Remark == sync.After:
		// Covers a timeout after NewAPI accepted the previous write.
	case user.Remark != sync.Before:
		sync.Error = "NewAPI 用户备注已被其他操作修改，未覆盖；请读取最新备注后手动保存。"
		return
	default:
		if _, err := s.setManagedUserRemark(ctx, record.UserID, sync.Before, sync.After); err != nil {
			sync.Error = publicRemarkError(err)
			return
		}
	}
	sync.State, sync.Error, sync.SyncedAt = "synced", "", s.riskModule().now().Unix()
	record.NoteUpdatedAt = sync.SyncedAt
}

func riskActionResult(record riskAction, replayed bool) map[string]any {
	result := map[string]any{"action": record, "replayed": replayed}
	if record.RemarkSync != nil && record.RemarkSync.State != "synced" {
		message := "账号封禁状态已确认，但 NewAPI 用户备注尚未同步："
		if record.State == "released" {
			message = "账号已解封，该历史封禁的 NewAPI 用户备注尚未同步："
		}
		result["warning"] = message + record.RemarkSync.Error
	}
	return result
}

func riskRemarkAuditEvent(record riskAction) string {
	if record.RemarkSync != nil && record.RemarkSync.State == "synced" {
		return "action_remark_synced"
	}
	return "action_remark_failed"
}

func (s *dashboardService) replayRiskRemark(ctx context.Context, record riskAction) (map[string]any, error) {
	if record.Action != "ban" || record.RemarkSync == nil || record.RemarkSync.State == "synced" ||
		record.State == "released" {
		return riskActionResult(record, true), nil
	}
	current, err := s.riskModule().user(ctx, record.UserID)
	if err != nil {
		return nil, err
	}
	if current.Status != 2 || !sameRiskUserState(current, record.After) {
		result := riskActionResult(record, true)
		result["warning"] = "账号状态在该封禁后发生变化，未重试备注同步；请读取当前用户备注后编辑。"
		return result, nil
	}
	s.syncRiskActionRemark(ctx, &record)
	record.UpdatedAt = s.riskModule().now().Unix()
	if err := s.riskModule().saveAction(ctx, record, riskRemarkAuditEvent(record)); err != nil {
		return nil, riskFailure(http.StatusServiceUnavailable, "备注同步已执行，但同步审计未保存；请读取当前用户备注确认。")
	}
	return riskActionResult(record, true), nil
}

func (s *dashboardService) userRemarkUpdateHTTP(c *gin.Context) {
	userID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || userID < 1 {
		riskHTTPError(c, riskFailure(http.StatusBadRequest, "用户 ID 无效。"))
		return
	}
	var request struct {
		Remark         *string `json:"remark"`
		ExpectedRemark *string `json:"expected_remark"`
		ActionID       string  `json:"action_id"`
	}
	if err := decodeRiskJSON(c, &request); err != nil {
		riskHTTPError(c, err)
		return
	}
	if request.Remark == nil || request.ExpectedRemark == nil {
		riskHTTPError(c, riskFailure(http.StatusBadRequest, "保存备注须同时提交 remark 与读取时的 expected_remark；空字符串可清空备注。"))
		return
	}
	result, err := s.updateRiskUserRemark(c.Request.Context(), userID, *request.ExpectedRemark, *request.Remark, request.ActionID)
	if err != nil {
		riskHTTPError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
}

// Both the account editor and the legacy ban-note endpoint use the same lease,
// selective update, and durable audit lifecycle. No account status is changed.
func (s *dashboardService) updateRiskUserRemark(ctx context.Context, userID int64, expected, remark, actionID string) (map[string]any, error) {
	if !utf8.ValidString(remark) || utf8.RuneCountInString(remark) > managedUserRemarkLimit {
		return nil, riskFailure(http.StatusBadRequest, "NewAPI 用户备注最多 255 字，不能截断已有内容。")
	}
	if !utf8.ValidString(expected) || utf8.RuneCountInString(expected) > 4096 || len(actionID) > 128 {
		return nil, riskFailure(http.StatusBadRequest, "备注版本或处置记录 ID 无效，请重新读取。")
	}
	store := s.riskModule()
	owner, lease := riskID("remark"), "user:"+strconv.FormatInt(userID, 10)
	acquired, err := store.acquire(ctx, lease, owner, 60)
	if err != nil {
		return nil, err
	}
	if !acquired {
		return nil, riskFailure(http.StatusConflict, "该账号正在处理，请稍后再保存备注。")
	}
	defer store.release(context.Background(), lease, owner)

	var action riskAction
	if actionID != "" {
		found, err := store.get(ctx, "action:"+actionID, &action)
		if err != nil {
			return nil, err
		}
		if !found || action.Action != "ban" || action.UserID != userID {
			return nil, riskFailure(http.StatusNotFound, "未找到该账号对应的封禁记录。")
		}
	}
	user, err := s.readManagedUserRemark(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !user.RemarkSupported {
		return nil, riskFailure(http.StatusConflict, "当前 NewAPI 数据库没有用户备注字段。")
	}
	if user.Remark != expected {
		return nil, riskFailure(http.StatusConflict, "用户备注已被其他管理员修改，请重新读取后合并内容再保存。")
	}
	result := map[string]any{"user_id": userID, "remark": remark, "remark_supported": true, "limit": managedUserRemarkLimit}
	if user.Remark == remark && (actionID == "" || action.RemarkSync == nil || action.RemarkSync.State == "synced") {
		if actionID != "" {
			result["action"] = action
		}
		return result, nil
	}
	now := store.now().Unix()
	audit := map[string]any{
		"id": riskID("audit"), "event": "user_remark_pending", "user_id": userID,
		"action": "remark", "operator": "admin", "reason": "更新 NewAPI 管理员用户备注",
		"state": "prepared", "previous_note": expected, "note": remark,
		"remark_before": expected, "remark_after": remark, "created_at": now,
	}
	if actionID != "" {
		audit["action_id"] = actionID
	}
	auditKey := "audit:" + audit["id"].(string)
	if err := store.put(ctx, auditKey, audit); err != nil {
		return nil, err
	}
	if _, err := s.setManagedUserRemark(ctx, userID, expected, remark); err != nil {
		audit["event"], audit["state"], audit["error"] = "user_remark_failed", "failed", publicRemarkError(err)
		if persistErr := store.put(ctx, auditKey, audit); persistErr != nil {
			return nil, riskFailure(http.StatusServiceUnavailable, "用户备注写入或确认失败，最终审计未保存；请重新读取当前备注。")
		}
		return nil, err
	}
	audit["event"], audit["state"], audit["created_at"] = "user_remark_updated", "verified", store.now().Unix()
	records := map[string]any{auditKey: audit}
	if actionID != "" {
		// Do not rewrite the ban's original reason or supplemental note.
		if action.RemarkSync != nil {
			action.RemarkSync = &riskRemarkSync{State: "synced", Before: expected, After: remark, SyncedAt: store.now().Unix()}
		}
		action.NoteUpdatedAt = store.now().Unix()
		records["action:"+action.ID] = action
		result["action"] = action
	}
	if err := store.putMany(ctx, records); err != nil {
		return nil, riskFailure(http.StatusServiceUnavailable, "NewAPI 用户备注已保存，但最终审计未保存；请重新读取当前备注确认。")
	}
	return result, nil
}
