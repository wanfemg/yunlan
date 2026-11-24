package main

import (
	"context"
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

// NewAPI's administrator-only users.remark is varchar(255), validated by
// Unicode character count rather than UTF-8 bytes.
const managedUserRemarkLimit = 255

func (s *dashboardService) readManagedUserRemark(ctx context.Context, id int64) (managedUserRecord, error) {
	if id < 1 {
		return managedUserRecord{}, &managedUserError{http.StatusBadRequest, "用户 ID 无效。"}
	}
	return s.managedUser(ctx, id)
}

// Compare the value the administrator actually read. No username, quota,
// status, authentication version, or other account field is part of this
// update. Do not use NewAPI's full-user PUT endpoint for this operation.
func (s *dashboardService) setManagedUserRemark(ctx context.Context, id int64, expected, next string) (managedUserRecord, error) {
	if !utf8.ValidString(expected) || !utf8.ValidString(next) {
		return managedUserRecord{}, &managedUserError{http.StatusBadRequest, "用户备注包含无效字符。"}
	}
	if utf8.RuneCountInString(next) > managedUserRemarkLimit {
		return managedUserRecord{}, &managedUserError{http.StatusBadRequest, "NewAPI 用户备注最多 255 字，原有内容与追加内容合并后也须符合限制。"}
	}
	current, err := s.readManagedUserRemark(ctx, id)
	if err != nil {
		return managedUserRecord{}, err
	}
	if !current.RemarkSupported {
		return managedUserRecord{}, &managedUserError{http.StatusConflict, "当前 NewAPI 的 users 表没有 remark 字段，无法保存管理员用户备注。"}
	}
	if current.Remark != expected {
		return current, &managedUserError{http.StatusConflict, "NewAPI 用户备注已被其他管理员修改，请重新读取后再保存。"}
	}
	if s.config.Mode == "demo" {
		state := s.userState()
		state.mu.Lock()
		defer state.mu.Unlock()
		latest, exists := state.demo[id]
		if !exists {
			return managedUserRecord{}, &managedUserError{http.StatusNotFound, "用户不存在。"}
		}
		if latest.Remark != expected || latest.Role != current.Role || latest.Status != current.Status {
			return latest, &managedUserError{http.StatusConflict, "用户资料或备注已变更，请重新读取后再保存。"}
		}
		latest.Remark = next
		state.demo[id] = latest
		return latest, nil
	}
	if expected == next {
		return current, nil
	}
	db := s.management
	column := tokenIdentifier(db.driver, "remark")
	comparison := "COALESCE(" + column + ", '') COLLATE BINARY = ?"
	switch db.driver {
	case "mysql":
		// The default MySQL collation can ignore case and trailing spaces.
		comparison = "CAST(COALESCE(" + column + ", '') AS BINARY) = CAST(? AS BINARY)"
	case "postgres":
		comparison = "COALESCE(" + column + ", '') COLLATE \"C\" = ?"
	}
	query := "UPDATE users SET " + column + " = ? WHERE id = ? AND role = ? AND status = ? AND " + comparison
	if current.deletedAtKnown {
		query += " AND deleted_at IS NULL"
	}
	result, err := db.db.ExecContext(ctx, bindQuery(db.driver, query), next, id, current.Role, current.Status, expected)
	if err != nil {
		return managedUserRecord{}, &managedUserError{http.StatusServiceUnavailable, "NewAPI 用户备注写入失败，请检查管理数据库连接及写入权限。"}
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return managedUserRecord{}, &managedUserError{http.StatusServiceUnavailable, "用户备注写入结果未能确认，请重新读取备注。"}
	}
	if affected != 1 {
		return managedUserRecord{}, &managedUserError{http.StatusConflict, "用户资料或备注已变更，请重新读取后再保存。"}
	}
	after, err := s.readManagedUserRemark(ctx, id)
	if err != nil {
		return managedUserRecord{}, &managedUserError{http.StatusServiceUnavailable, "用户备注已提交，但读取确认失败，请重新读取备注。"}
	}
	if !after.RemarkSupported || after.Remark != next {
		return after, &managedUserError{http.StatusConflict, "用户备注提交后又发生变更，请重新读取后确认。"}
	}
	return after, nil
}

func (s *dashboardService) userRemarkHTTP(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id < 1 {
		writeUserManagementError(c, &managedUserError{http.StatusBadRequest, "用户 ID 无效。"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), queryTimeout)
	defer cancel()
	user, err := s.readManagedUserRemark(ctx, id)
	if err != nil {
		writeUserManagementError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
		"user_id": user.ID, "remark": user.Remark, "remark_supported": user.RemarkSupported, "limit": managedUserRemarkLimit,
	}})
}
