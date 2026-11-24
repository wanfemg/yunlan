package main

import (
	"context"
	"net/http"
	"sort"
	"strconv"

	"github.com/gin-gonic/gin"
)

const riskAccountMatrixDefaultWindow = int64(7 * 86400)

type riskAccountEvidence struct {
	Type        string `json:"type"`
	Strength    string `json:"strength"`
	Description string `json:"description"`
}

type riskAccountMatrixMember struct {
	User             managedUserRecord `json:"user"`
	Browsers         []string          `json:"browser_ids,omitempty"`
	LoginIPs         []string          `json:"login_ips,omitempty"`
	CallIPs          []string          `json:"call_ips,omitempty"`
	Tokens           []string          `json:"token_ids,omitempty"`
	InviterID        int64             `json:"inviter_id,omitempty"`
	DetailsTruncated bool              `json:"details_truncated"`
}

type riskAccountMatrixGroup struct {
	ID         string                    `json:"id"`
	Strength   string                    `json:"strength"`
	Level      string                    `json:"level"`
	Reason     string                    `json:"reason"`
	Members    []riskAccountMatrixMember `json:"members"`
	Evidence   []riskAccountEvidence     `json:"evidence"`
	TotalUsers int                       `json:"total_users"`
	Truncated  bool                      `json:"truncated"`
}

type riskAccountMatrixData struct {
	AsOf            int64                    `json:"as_of"`
	WindowStart     int64                    `json:"window_start"`
	HistoryComplete bool                     `json:"history_complete"`
	Groups          []riskAccountMatrixGroup `json:"groups"`
	Warnings        []string                 `json:"warnings"`
	TotalGroups     int                      `json:"total_groups"`
	Truncated       bool                     `json:"truncated"`
}

func riskAccountAppendUnique(values []string, value string) []string {
	if value == "" {
		return values
	}
	for _, current := range values {
		if current == value {
			return values
		}
	}
	return append(values, value)
}

type riskAccountMatrixProfiles struct {
	browserUserIDs map[string][]int64
	tokenUserIDs   map[string][]int64
}

func riskAccountID(prefix string, key string) string {
	return prefix + "-" + key
}

func userIDSet(ids []int64) map[int64]bool {
	result := make(map[int64]bool, len(ids))
	for _, id := range ids {
		result[id] = true
	}
	return result
}

func (s *dashboardService) riskAccountMatrix(ctx context.Context, asOf int64) (riskAccountMatrixData, error) {
	if s.network == nil {
		return riskAccountMatrixData{}, riskFailure(http.StatusServiceUnavailable, "network event collection is not initialized")
	}
	users, err := s.managedUsers(ctx, 0)
	if err != nil {
		return riskAccountMatrixData{}, err
	}
	windowStart := asOf - riskAccountMatrixDefaultWindow
	events, err := s.riskEvents(ctx, windowStart, asOf)
	if err != nil {
		return riskAccountMatrixData{}, err
	}
	historyComplete, err := s.network.historyCompleteFor(ctx, windowStart)
	if err != nil {
		return riskAccountMatrixData{}, err
	}

	profiles := riskAccountMatrixProfiles{browserUserIDs: map[string][]int64{}, tokenUserIDs: map[string][]int64{}}
	browsers := map[int64][]string{}
	loginIPs := map[int64][]string{}
	callIPs := map[int64][]string{}
	tokens := map[int64][]string{}
	byUser := map[int64]managedUserRecord{}
	for _, user := range users {
		byUser[user.ID] = user
	}
	for _, event := range events {
		if event.UserID <= 0 {
			continue
		}
		if _, known := byUser[event.UserID]; !known {
			continue
		}
		switch event.Source {
		case networkSourceLogin:
			if event.Result != "success" || event.BrowserID == "" {
				continue
			}
			profiles.browserUserIDs[event.BrowserID] = append(profiles.browserUserIDs[event.BrowserID], event.UserID)
			browsers[event.UserID] = riskAccountAppendUnique(browsers[event.UserID], event.BrowserID)
			loginIPs[event.UserID] = riskAccountAppendUnique(loginIPs[event.UserID], event.IP)
		case networkSourceRegistration:
			if event.Result != "success" || event.BrowserID == "" {
				continue
			}
			profiles.browserUserIDs[event.BrowserID] = append(profiles.browserUserIDs[event.BrowserID], event.UserID)
			browsers[event.UserID] = riskAccountAppendUnique(browsers[event.UserID], event.BrowserID)
		case networkSourceCall:
			callIPs[event.UserID] = riskAccountAppendUnique(callIPs[event.UserID], event.IP)
			if event.TokenID > 0 {
				tokenKey := strconv.FormatInt(event.TokenID, 10)
				profiles.tokenUserIDs[tokenKey] = append(profiles.tokenUserIDs[tokenKey], event.UserID)
				tokens[event.UserID] = riskAccountAppendUnique(tokens[event.UserID], tokenKey)
			}
		}
	}
	for _, profile := range []map[string][]int64{profiles.browserUserIDs, profiles.tokenUserIDs} {
		for key, ids := range profile {
			seen, groups := map[int64]bool{}, make([]int64, 0)
			for _, id := range ids {
				if !seen[id] {
					seen[id] = true
					groups = append(groups, id)
				}
			}
			profile[key] = groups
		}
	}
	browserGroups := map[string]map[int64]bool{}
	for browser, ids := range profiles.browserUserIDs {
		if len(ids) >= 2 {
			browserGroups[browser] = userIDSet(ids)
		}
	}
	tokenGroups := map[string]map[int64]bool{}
	for token, ids := range profiles.tokenUserIDs {
		if len(ids) >= 2 {
			tokenGroups[token] = userIDSet(ids)
		}
	}

	grouped := map[int64]bool{}
	groups := []riskAccountMatrixGroup{}
	for browser, ids := range browserGroups {
		if len(ids) < 2 {
			continue
		}
		members := make([]riskAccountMatrixMember, 0, len(ids))
		for userID := range ids {
			user := byUser[userID]
			members = append(members, riskAccountMatrixMember{
				User: user, Browsers: []string{browser}, LoginIPs: loginIPs[userID],
				CallIPs: callIPs[userID], Tokens: tokens[userID], InviterID: user.InviterID,
			})
			grouped[userID] = true
		}
		groups = append(groups, riskAccountMatrixGroup{
			ID: riskAccountID("browser", browser), Strength: "high", Level: "review",
			Reason:  "同一已验证 BrowserID 在窗口内成功登录或注册多个账号。",
			Members: members,
			Evidence: []riskAccountEvidence{{Type: "shared_verified_browser_id", Strength: "high",
				Description: "登录/注册探针确认的浏览器标识关联，不是硬件指纹。"}},
		})
	}
	for token, ids := range tokenGroups {
		// Browser and token links can partially overlap. Retain the token group
		// so accounts linked only by that token are never silently discarded.
		if len(ids) < 2 {
			continue
		}
		members := make([]riskAccountMatrixMember, 0, len(ids))
		for userID := range ids {
			members = append(members, riskAccountMatrixMember{
				User: byUser[userID], Browsers: browsers[userID], LoginIPs: loginIPs[userID],
				CallIPs: callIPs[userID], Tokens: []string{token}, InviterID: byUser[userID].InviterID,
			})
			grouped[userID] = true
		}
		groups = append(groups, riskAccountMatrixGroup{
			ID: riskAccountID("token", token), Strength: "medium", Level: "review",
			Reason:  "多个账号在同一窗口使用同一令牌，令牌共享是账号关联证据，但可能来自转发服务。",
			Members: members,
			Evidence: []riskAccountEvidence{{Type: "shared_token", Strength: "medium",
				Description: "调用日志确认同一令牌被多个账号使用。"}},
		})
	}
	sort.Slice(groups, func(i, j int) bool {
		if len(groups[i].Members) != len(groups[j].Members) {
			return len(groups[i].Members) > len(groups[j].Members)
		}
		return groups[i].ID < groups[j].ID
	})
	warnings := []string{
		"账号矩阵只描述观测到的关联，不证明同一自然人。",
		"BrowserID 是已验证登录/注册的浏览器标识，同一设备可能更换标识，多个使用者也可能共享浏览器。",
		"共享令牌与共享调用 IP 是辅助证据，可能由网关或代理造成，不参与自动封禁。",
	}
	totalGroups := len(groups)
	if len(groups) > 200 {
		groups = groups[:200]
		warnings = append(warnings, "账号关联组超过展示上限，当前展示人数最多的 200 组。账号评估仍使用完整关联索引。")
	}
	detailsTruncated := false
	for index := range groups {
		group := &groups[index]
		sort.Slice(group.Members, func(i, j int) bool { return group.Members[i].User.ID < group.Members[j].User.ID })
		group.TotalUsers = len(group.Members)
		if len(group.Members) > 100 {
			group.Members, group.Truncated = group.Members[:100], true
		}
		for memberIndex := range group.Members {
			member := &group.Members[memberIndex]
			member.DetailsTruncated = len(member.Browsers) > 50 || len(member.LoginIPs) > 50 || len(member.CallIPs) > 50 || len(member.Tokens) > 50
			detailsTruncated = detailsTruncated || member.DetailsTruncated
			member.Browsers = member.Browsers[:min(50, len(member.Browsers))]
			member.LoginIPs = member.LoginIPs[:min(50, len(member.LoginIPs))]
			member.CallIPs = member.CallIPs[:min(50, len(member.CallIPs))]
			member.Tokens = member.Tokens[:min(50, len(member.Tokens))]
		}
	}
	if detailsTruncated {
		warnings = append(warnings, "部分账号的浏览器、登录 IP、调用 IP 或令牌详情超过上限，每类最多返回 50 条；关联人数及规则计算不受影响。")
	}
	if !historyComplete {
		warnings = append(warnings, "调用历史声明或保留范围未覆盖整个矩阵窗口，当前关联结果可能不完整，不能作为自动处置依据。")
	}
	return riskAccountMatrixData{
		AsOf: asOf, WindowStart: windowStart, HistoryComplete: historyComplete,
		Groups: groups, Warnings: warnings, TotalGroups: totalGroups, Truncated: totalGroups > len(groups),
	}, nil
}

func (s *dashboardService) riskAccountMatrixHTTP(c *gin.Context) {
	data, err := s.riskAccountMatrix(c.Request.Context(), s.riskModule().now().Unix())
	if err != nil {
		riskHTTPError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": data})
}
