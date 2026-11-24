package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	riskIPMonitorLimit      = 200
	riskIPMonitorEventLimit = 200000
	riskIPMonitorExitLimit  = 50
)

type riskIPMonitorUser struct {
	ID           int64  `json:"id"`
	Username     string `json:"username"`
	DisplayName  string `json:"display_name"`
	Email        string `json:"email"`
	Status       int64  `json:"status"`
	Role         int64  `json:"role"`
	Known        bool   `json:"known"`
	RequestCount int64  `json:"request_count"`
	UsedQuota    int64  `json:"used_quota"`
}

type riskIPMonitorExit struct {
	IP         string         `json:"ip"`
	Events     int64          `json:"events"`
	Success    int64          `json:"success"`
	Failure    int64          `json:"failure"`
	LastSeenAt int64          `json:"last_seen_at"`
	Info       *networkIPInfo `json:"info"`
}

type riskIPMonitorActor struct {
	User       riskIPMonitorUser   `json:"user"`
	IPCount    int                 `json:"ip_count"`
	Events     int64               `json:"events"`
	IPs        []riskIPMonitorExit `json:"ips"`
	Truncated  bool                `json:"truncated"`
	LastSeenAt int64               `json:"last_seen_at"`
}

type riskIPMonitorShared struct {
	IP         string              `json:"ip"`
	Events     int64               `json:"events"`
	Users      []riskIPMonitorUser `json:"users"`
	UserCount  int                 `json:"user_count"`
	TokenIDs   []int64             `json:"token_ids"`
	TokenCount int                 `json:"token_count"`
	Info       *networkIPInfo      `json:"info"`
	Truncated  bool                `json:"truncated"`
}

type riskIPMonitorToken struct {
	TokenID   int64               `json:"token_id"`
	Users     []riskIPMonitorUser `json:"users"`
	UserCount int                 `json:"user_count"`
	Events    int64               `json:"events"`
	IPCount   int                 `json:"ip_count"`
	IPs       []riskIPMonitorExit `json:"ips"`
	Truncated bool                `json:"truncated"`
}

type riskIPMonitorDevice struct {
	BrowserID string              `json:"browser_id"`
	Users     []riskIPMonitorUser `json:"users"`
	UserCount int                 `json:"user_count"`
	Events    int64               `json:"events"`
	FirstSeen int64               `json:"first_seen_at"`
	LastSeen  int64               `json:"last_seen_at"`
	IPs       []string            `json:"ips"`
	IPCount   int                 `json:"ip_count"`
	Truncated bool                `json:"truncated"`
}

type riskIPMonitorBucket struct {
	events, success, failure, firstSeen, lastSeen int64
	ips                                           map[string]*riskIPMonitorBucket
	users, tokens                                 map[int64]bool
}

func newRiskIPMonitorBucket() *riskIPMonitorBucket {
	return &riskIPMonitorBucket{
		ips: map[string]*riskIPMonitorBucket{}, users: map[int64]bool{}, tokens: map[int64]bool{},
	}
}

func (bucket *riskIPMonitorBucket) observe(event networkEvent) {
	bucket.events++
	if event.Result == "success" {
		bucket.success++
	} else if event.Result == "failure" {
		bucket.failure++
	}
	if bucket.firstSeen == 0 || event.OccurredAt < bucket.firstSeen {
		bucket.firstSeen = event.OccurredAt
	}
	if event.OccurredAt > bucket.lastSeen {
		bucket.lastSeen = event.OccurredAt
	}
	if event.UserID > 0 {
		bucket.users[event.UserID] = true
	}
	if event.Source == networkSourceCall && event.TokenID > 0 {
		bucket.tokens[event.TokenID] = true
	}
}

// The aggregation reads one source at a time and never converts API egresses
// into login device evidence. Large windows explicitly report their read cap.
func (n *networkStore) visitRiskIPMonitorEvents(ctx context.Context, source string, start, end int64, visit func(networkEvent)) (bool, error) {
	if err := n.ensure(ctx); err != nil {
		return false, err
	}
	if n.service.config.Mode == "demo" {
		events, err := n.Events(ctx, source, start, end)
		if err != nil {
			return false, err
		}
		for index := len(events) - 1; index >= max(0, len(events)-riskIPMonitorEventLimit); index-- {
			if err := ctx.Err(); err != nil {
				return false, err
			}
			visit(events[index])
		}
		return len(events) > riskIPMonitorEventLimit, nil
	}
	rows, err := n.service.management.query(ctx, `SELECT payload FROM siteadmin_network_events
		WHERE source=? AND occurred_at>=? AND occurred_at<? ORDER BY occurred_at DESC,id DESC LIMIT ?`,
		source, start, end, riskIPMonitorEventLimit+1)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	scanned := 0
	for rows.Next() {
		if scanned >= riskIPMonitorEventLimit {
			return true, nil
		}
		var payload string
		var event networkEvent
		if err := rows.Scan(&payload); err != nil {
			return false, err
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return false, errors.New("invalid network observation")
		}
		if event.Source == source && event.OccurredAt >= start && event.OccurredAt < end {
			visit(event)
		}
		scanned++
	}
	return false, rows.Err()
}

func (s *dashboardService) riskIPMonitorHTTP(c *gin.Context) {
	source := c.DefaultQuery("source", networkSourceCall)
	period := c.DefaultQuery("period", "24h")
	windows := map[string]int64{"1h": 3600, "6h": 21600, "24h": 86400, "3d": 259200, "7d": 604800}
	seconds, valid := windows[period]
	if !validNetworkSource(source) || !valid {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": "IP 来源或时间范围无效"})
		return
	}
	if s.network == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "error": "网络事件采集尚未初始化"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), queryTimeout)
	defer cancel()
	warnings := []string{}
	syncComplete := source != networkSourceCall
	if source == networkSourceCall {
		syncContext, stopSync := context.WithTimeout(ctx, 5*time.Second)
		result, err := s.network.SyncCalls(syncContext)
		stopSync()
		if err != nil {
			warnings = append(warnings, "调用日志本轮同步未完成，以下仅展示已经采集的调用事件。")
		} else {
			syncComplete, _ = result["complete"].(bool)
			if !syncComplete {
				warnings = append(warnings, "调用日志仍在导入，当前统计不是完整调用历史。")
			}
		}
	}
	end := userClock(s).Unix() + 1
	start := end - seconds
	users, err := s.managedUsers(ctx, 0)
	if err != nil {
		riskHTTPError(c, err)
		return
	}
	identities := make(map[int64]riskIPMonitorUser, len(users))
	for _, user := range users {
		identities[user.ID] = riskIPMonitorUser{ID: user.ID, Username: user.Username,
			DisplayName: user.DisplayName, Email: user.Email, Status: user.Status, Role: user.Role, Known: true,
			RequestCount: user.RequestCount, UsedQuota: user.UsedQuota}
	}
	identity := func(id int64) riskIPMonitorUser {
		if user, found := identities[id]; found {
			return user
		}
		return riskIPMonitorUser{ID: id}
	}
	projectUsers := func(ids map[int64]bool) []riskIPMonitorUser {
		result := make([]riskIPMonitorUser, 0, len(ids))
		for id := range ids {
			result = append(result, identity(id))
		}
		sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
		return result[:min(len(result), riskIPMonitorExitLimit)]
	}
	byIP, byUser, byToken, byDevice := map[string]*riskIPMonitorBucket{}, map[int64]*riskIPMonitorBucket{},
		map[int64]*riskIPMonitorBucket{}, map[string]*riskIPMonitorBucket{}
	var events, recorded, unassociated, missing, invalidIPs, nonPublic int64
	truncatedEvents, err := s.network.visitRiskIPMonitorEvents(ctx, source, start, end, func(event networkEvent) {
		events++
		if event.UserID <= 0 {
			unassociated++
		}
		ip, class := classifyNetworkIP(event.IP)
		validIP := class != "missing" && class != "invalid"
		observeActor := func(actor *riskIPMonitorBucket) {
			actor.observe(event)
			if !validIP {
				return
			}
			exit := actor.ips[ip]
			if exit == nil {
				exit = newRiskIPMonitorBucket()
				actor.ips[ip] = exit
			}
			exit.observe(event)
		}
		// Account totals include missing-IP observations. The exit sets only
		// contain valid addresses; browsers can still be observed without IP.
		if event.UserID > 0 {
			if byUser[event.UserID] == nil {
				byUser[event.UserID] = newRiskIPMonitorBucket()
			}
			observeActor(byUser[event.UserID])
			if source == networkSourceCall && event.TokenID > 0 {
				if byToken[event.TokenID] == nil {
					byToken[event.TokenID] = newRiskIPMonitorBucket()
				}
				observeActor(byToken[event.TokenID])
			}
			if source != networkSourceCall && event.Result == "success" && event.BrowserID != "" {
				if byDevice[event.BrowserID] == nil {
					byDevice[event.BrowserID] = newRiskIPMonitorBucket()
				}
				observeActor(byDevice[event.BrowserID])
			}
		}
		if class == "missing" {
			missing++
			return
		}
		if class == "invalid" {
			invalidIPs++
			return
		}
		recorded++
		if class != "public" {
			nonPublic++
		}
		bucket := byIP[ip]
		if bucket == nil {
			bucket = newRiskIPMonitorBucket()
			byIP[ip] = bucket
		}
		bucket.observe(event)
	})
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "error": "IP 监控事件暂不可用，请检查网络采集数据源"})
		return
	}
	if truncatedEvents {
		warnings = append(warnings, "所选窗口超过 200000 条事件，当前按最近采集记录统计；缩短时间范围可查看完整窗口。")
	}
	infoContext, stopInfo := context.WithTimeout(ctx, 2*time.Second)
	defer stopInfo()
	infos := map[string]*networkIPInfo{}
	lookup := func(ip string) *networkIPInfo {
		if info, found := infos[ip]; found {
			return info
		}
		_, class := classifyNetworkIP(ip)
		infos[ip] = &networkIPInfo{IP: ip, AddressClass: class, NetworkType: "unknown",
			Proxy: defaultProxyAttributes(), DataSource: "unavailable", FieldSources: map[string]string{}}
		if infoContext.Err() == nil {
			if info, err := s.network.Lookup(infoContext, ip); err == nil {
				infos[ip] = &info
			}
		}
		return infos[ip]
	}
	projectExits := func(actor *riskIPMonitorBucket) []riskIPMonitorExit {
		result := make([]riskIPMonitorExit, 0, len(actor.ips))
		for ip, bucket := range actor.ips {
			result = append(result, riskIPMonitorExit{IP: ip, Events: bucket.events, Success: bucket.success,
				Failure: bucket.failure, LastSeenAt: bucket.lastSeen})
		}
		sort.Slice(result, func(i, j int) bool {
			if result[i].Events != result[j].Events {
				return result[i].Events > result[j].Events
			}
			return result[i].IP < result[j].IP
		})
		result = result[:min(len(result), riskIPMonitorExitLimit)]
		return result
	}
	shared := []riskIPMonitorShared{}
	for ip, bucket := range byIP {
		if len(bucket.users) < 2 && (source != networkSourceCall || len(bucket.tokens) < 2) {
			continue
		}
		tokens := make([]int64, 0, len(bucket.tokens))
		for id := range bucket.tokens {
			tokens = append(tokens, id)
		}
		sort.Slice(tokens, func(i, j int) bool { return tokens[i] < tokens[j] })
		shared = append(shared, riskIPMonitorShared{IP: ip, Events: bucket.events, Users: projectUsers(bucket.users),
			UserCount: len(bucket.users), TokenIDs: tokens[:min(len(tokens), riskIPMonitorExitLimit)], TokenCount: len(tokens),
			Truncated: len(bucket.users) > riskIPMonitorExitLimit || len(tokens) > riskIPMonitorExitLimit})
	}
	sort.Slice(shared, func(i, j int) bool {
		if shared[i].UserCount != shared[j].UserCount {
			return shared[i].UserCount > shared[j].UserCount
		}
		if shared[i].Events != shared[j].Events {
			return shared[i].Events > shared[j].Events
		}
		return shared[i].IP < shared[j].IP
	})
	actors := []riskIPMonitorActor{}
	for id, bucket := range byUser {
		if len(bucket.ips) >= 3 {
			actors = append(actors, riskIPMonitorActor{User: identity(id), IPCount: len(bucket.ips), Events: bucket.events,
				IPs: projectExits(bucket), LastSeenAt: bucket.lastSeen, Truncated: len(bucket.ips) > riskIPMonitorExitLimit})
		}
	}
	sort.Slice(actors, func(i, j int) bool {
		if actors[i].IPCount != actors[j].IPCount {
			return actors[i].IPCount > actors[j].IPCount
		}
		if actors[i].Events != actors[j].Events {
			return actors[i].Events > actors[j].Events
		}
		return actors[i].User.ID < actors[j].User.ID
	})
	tokens := []riskIPMonitorToken{}
	for id, bucket := range byToken {
		if len(bucket.ips) >= 3 {
			tokens = append(tokens, riskIPMonitorToken{TokenID: id, Users: projectUsers(bucket.users), UserCount: len(bucket.users),
				Events: bucket.events, IPCount: len(bucket.ips), IPs: projectExits(bucket),
				Truncated: len(bucket.ips) > riskIPMonitorExitLimit || len(bucket.users) > riskIPMonitorExitLimit})
		}
	}
	sort.Slice(tokens, func(i, j int) bool {
		if tokens[i].IPCount != tokens[j].IPCount {
			return tokens[i].IPCount > tokens[j].IPCount
		}
		return tokens[i].TokenID < tokens[j].TokenID
	})
	devices := []riskIPMonitorDevice{}
	for browser, bucket := range byDevice {
		if len(bucket.users) < 2 {
			continue
		}
		ips := make([]string, 0, len(bucket.ips))
		for ip := range bucket.ips {
			ips = append(ips, ip)
		}
		sort.Strings(ips)
		devices = append(devices, riskIPMonitorDevice{BrowserID: browser, Users: projectUsers(bucket.users),
			UserCount: len(bucket.users), Events: bucket.events, FirstSeen: bucket.firstSeen, LastSeen: bucket.lastSeen,
			IPs: ips[:min(len(ips), riskIPMonitorExitLimit)], IPCount: len(ips),
			Truncated: len(ips) > riskIPMonitorExitLimit || len(bucket.users) > riskIPMonitorExitLimit})
	}
	sort.Slice(devices, func(i, j int) bool {
		if devices[i].UserCount != devices[j].UserCount {
			return devices[i].UserCount > devices[j].UserCount
		}
		return devices[i].BrowserID < devices[j].BrowserID
	})
	totals := gin.H{"shared_ips": len(shared), "multi_ip_users": len(actors), "multi_ip_tokens": len(tokens), "shared_devices": len(devices)}
	listTruncated := len(shared) > riskIPMonitorLimit || len(actors) > riskIPMonitorLimit || len(tokens) > riskIPMonitorLimit || len(devices) > riskIPMonitorLimit
	shared = shared[:min(len(shared), riskIPMonitorLimit)]
	actors = actors[:min(len(actors), riskIPMonitorLimit)]
	tokens = tokens[:min(len(tokens), riskIPMonitorLimit)]
	devices = devices[:min(len(devices), riskIPMonitorLimit)]
	for index := range shared {
		shared[index].Info = lookup(shared[index].IP)
	}
	for index := range actors {
		for exit := range actors[index].IPs {
			actors[index].IPs[exit].Info = lookup(actors[index].IPs[exit].IP)
		}
	}
	for index := range tokens {
		for exit := range tokens[index].IPs {
			tokens[index].IPs[exit].Info = lookup(tokens[index].IPs[exit].IP)
		}
	}
	var coverage *float64
	if events > 0 {
		coverage = float64Pointer(float64(recorded) / float64(events))
	}
	if listTruncated {
		warnings = append(warnings, "每类关联列表最多展示前 200 项，单项最多展开 50 个关联；总数独立显示。")
	}
	warnings = append(warnings, "共享 IP、多出口、VPN 或代理属性仅用于人工复核，不独立触发封禁。调用出口不代表用户登录位置。")
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
		"source": source, "period": period, "window_start": start, "window_end": end, "as_of": end - 1,
		"timezone": userActivityTimezone(s.config.Location), "quota_per_unit": s.config.QuotaPerUnit,
		"sync_complete": syncComplete, "summary": gin.H{"events": events, "recorded_ip_events": recorded,
			"ip_coverage": coverage, "unique_ips": len(byIP), "shared_ips": totals["shared_ips"], "multi_ip_tokens": totals["multi_ip_tokens"],
			"unassociated_events": unassociated, "missing_ip_events": missing, "invalid_ip_events": invalidIPs, "non_public_ip_events": nonPublic},
		"shared_ips": shared, "multi_ip_users": actors, "multi_ip_tokens": tokens, "shared_devices": devices,
		"totals": totals, "truncated": listTruncated, "event_sampled": truncatedEvents, "limit": riskIPMonitorLimit,
		"exit_limit": riskIPMonitorExitLimit, "event_limit": riskIPMonitorEventLimit, "warnings": warnings,
	}})
}
