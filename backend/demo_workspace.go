package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"time"
)

// Demo records use the same storage and statistics paths as live observations.
// Their geography is an explicit illustration, not a real lookup of these exits.
func (s *dashboardService) seedDemoWorkspace() error {
	if s.config.Mode != "demo" {
		return nil
	}
	if s.network == nil {
		return errors.New("demo network storage is not initialized")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if complete, err := s.network.getMeta(ctx, "demo_seed_complete"); err != nil {
		return err
	} else if complete == "true" {
		return nil
	}
	seedAt, err := s.network.getMeta(ctx, "demo_seed_at")
	if err != nil {
		return err
	}
	now := userClock(s).Unix()
	if seedAt != "" {
		now, err = strconv.ParseInt(seedAt, 10, 64)
		if err != nil || now <= 0 {
			return errors.New("demo seed timestamp is invalid")
		}
	} else if err := s.network.setMeta(ctx, "demo_seed_at", strconv.FormatInt(now, 10)); err != nil {
		return err
	}
	if _, err := s.managedUsers(ctx, 0); err != nil {
		return err
	}
	// Historical demo observations include accounts that have since been
	// cancelled. Their creation timeline comes from the fixture store, while
	// management/assessment paths still exclude cancelled users.
	userState := s.userState()
	userState.mu.Lock()
	byID := make(map[int64]managedUserRecord, len(userState.demo))
	for id, user := range userState.demo {
		byID[id] = user
	}
	userState.mu.Unlock()
	for _, id := range []int64{101, 102, 103, 104, 105, 106} {
		if _, found := byID[id]; !found {
			return errors.New("demo account fixture is unavailable")
		}
	}
	attributes := []networkIPInfo{
		{IP: "1.1.1.1", CountryCode: "CN", Country: "中国", Region: "浙江", City: "杭州", ASN: 13335, ASOrg: "演示应用网络", ISP: "演示运营商", NetworkType: "hosting"},
		{IP: "1.0.0.1", CountryCode: "CN", Country: "中国", Region: "北京", City: "北京", ASN: 13335, ASOrg: "演示应用网络", ISP: "演示运营商", NetworkType: "corporate"},
		{IP: "8.8.8.8", CountryCode: "US", Country: "美国", Region: "California", City: "Mountain View", ASN: 15169, ASOrg: "演示云网络", ISP: "演示云出口", NetworkType: "hosting"},
		{IP: "8.8.4.4", CountryCode: "SG", Country: "新加坡", Region: "Singapore", City: "Singapore", ASN: 15169, ASOrg: "演示云网络", ISP: "演示云出口", NetworkType: "hosting"},
		{IP: "9.9.9.9", CountryCode: "JP", Country: "日本", Region: "Tokyo", City: "Tokyo", ASN: 19281, ASOrg: "演示网络组织", ISP: "演示移动网络", NetworkType: "mobile"},
		{IP: "149.112.112.112", CountryCode: "DE", Country: "德国", Region: "Hesse", City: "Frankfurt", ASN: 19281, ASOrg: "演示网络组织", ISP: "演示机房出口", NetworkType: "hosting"},
		{IP: "93.184.216.34", CountryCode: "GB", Country: "英国", Region: "England", City: "London", ASN: 64501, ASOrg: "演示住宅网络", ISP: "演示宽带", NetworkType: "residential"},
		{IP: "208.67.222.222", CountryCode: "HK", Country: "中国香港", Region: "香港", City: "香港", ASN: 64502, ASOrg: "演示企业网络", ISP: "演示企业出口", NetworkType: "corporate"},
		{IP: "208.67.220.220", CountryCode: "FR", Country: "法国", Region: "Ile-de-France", City: "Paris", ASN: 64503, ASOrg: "演示移动网络", ISP: "演示移动运营商", NetworkType: "mobile"},
		{IP: "80.67.169.12", CountryCode: "SE", Country: "瑞典", Region: "Stockholm", City: "Stockholm", ASN: 64504, ASOrg: "演示匿名出口", ISP: "演示中继网络", NetworkType: "hosting"},
		{IP: "185.199.108.153", CountryCode: "CA", Country: "加拿大", Region: "Ontario", City: "Toronto", ASN: 64505, ASOrg: "演示边缘网络", ISP: "演示托管服务", NetworkType: "hosting"},
		{IP: "2606:4700:4700::1111", CountryCode: "US", Country: "美国", Region: "California", City: "San Francisco", ASN: 64506, ASOrg: "演示 IPv6 宽带", ISP: "演示 IPv6 运营商", NetworkType: "residential"},
		{IP: "2001:4860:4860::8888", CountryCode: "SG", Country: "新加坡", Region: "Singapore", City: "Singapore", ASN: 64507, ASOrg: "演示 IPv6 云网络", ISP: "演示 IPv6 云出口", NetworkType: "hosting"},
		{IP: "4.2.2.2", NetworkType: "unknown"},
	}
	realIPData := regularConfigFile(s.config.GeoCityPath) || regularConfigFile(s.config.GeoASNPath) ||
		regularConfigFile(filepath.Join(s.config.IPIntelligenceDir, "manifest.json"))
	for index, info := range attributes {
		if realIPData {
			break
		}
		info.DataSource = "demo-fixture"
		info.Proxy = map[string]string{"vpn": "no", "public_proxy": "no", "residential_proxy": "no", "tor": "no"}
		if index == 3 {
			info.Proxy["vpn"] = "yes"
		}
		if index == 5 {
			info.Proxy["vpn"], info.Proxy["public_proxy"] = "unknown", "unknown"
		}
		switch index {
		case 6:
			info.Proxy["residential_proxy"] = "yes"
		case 7:
			info.Proxy["public_proxy"] = "yes"
		case 8:
			info.Proxy["vpn"] = "yes"
		case 9:
			info.Proxy["tor"] = "yes"
		case 12, 13:
			info.Proxy = defaultProxyAttributes()
		}
		if err := s.network.SaveAttributes(ctx, info); err != nil {
			return fmt.Errorf("seed demo IP attributes: %w", err)
		}
	}
	callIPs := []string{"1.1.1.1", "8.8.8.8", "1.0.0.1", "8.8.4.4", "9.9.9.9", "149.112.112.112"}
	models := []string{"gpt-4.1-mini", "claude-sonnet-4", "gemini-2.5-flash", "deepseek-chat"}
	emptyResponseThresholds := map[string]uint64{
		"gpt-4.1-mini": 2, "claude-sonnet-4": 8, "gemini-2.5-flash": 18,
	}
	createdAt := map[int64]int64{101: now - 7*86400 + 60, 102: now - 6*86400, 103: now - 7*86400 + 120}
	for userID, at := range createdAt {
		user := byID[userID]
		user.CreatedAt, user.CreatedAtKnown = at, true
		byID[userID] = user
	}
	requests, quotas := map[int64]int64{}, map[int64]int64{}
	lastLogin := map[int64]int64{}
	add := func(event networkEvent) error {
		if event.UserID > 0 {
			user, found := byID[event.UserID]
			if !found || event.OccurredAt < user.CreatedAt || event.OccurredAt > now {
				return fmt.Errorf("demo event %s has an invalid account timeline", event.ID)
			}
		}
		if event.Metadata == nil {
			event.Metadata = map[string]string{}
		}
		event.Metadata["dataset"] = "demo-fixture"
		if event.Source == networkSourceCall {
			var hash uint64 = 14695981039346656037
			for _, value := range []byte(event.ID) {
				hash = (hash ^ uint64(value)) * 1099511628211
			}
			if event.Metadata["quota"] == "" {
				event.Metadata["quota"] = strconv.FormatUint(1000+hash%12000, 10)
			}
			if event.Result == "failure" {
				event.Metadata["quota"] = "0"
			}
			if event.Metadata["model"] == "" {
				event.Metadata["model"] = models[hash%uint64(len(models))]
			}
			event.Metadata["channel_id"] = strconv.FormatUint(1+hash%5, 10)
			event.Metadata["prompt_tokens"] = strconv.FormatUint(120+hash%2400, 10)
			event.Metadata["completion_tokens"] = strconv.FormatUint(80+hash%1800, 10)
			// Keep model-status fixtures in the same observed call dataset as
			// usage and token analysis. Zero output on a successful record is
			// distinct from an error record, with varied rates per model.
			emptyThreshold := emptyResponseThresholds[event.Metadata["model"]]
			if event.Result == "success" && (hash/7)%100 < emptyThreshold {
				event.Metadata["completion_tokens"] = "0"
			}
			if event.Result == "failure" {
				event.Metadata["completion_tokens"] = "0"
			}
			event.Metadata["duration_seconds"] = strconv.FormatFloat(.2+float64(hash%120)/10, 'f', 1, 64)
			event.Metadata["request_id"] = "req-" + event.ID
		}
		if err := s.network.Add(ctx, event); err != nil {
			return err
		}
		if event.Source == networkSourceCall {
			requests[event.UserID]++
			quota, _ := strconv.ParseInt(event.Metadata["quota"], 10, 64)
			quotas[event.UserID] += quota
		}
		if event.Source == networkSourceLogin && event.Result == "success" && event.OccurredAt > lastLogin[event.UserID] {
			lastLogin[event.UserID] = event.OccurredAt
		}
		return nil
	}
	for userIndex, userID := range []int64{101, 102, 103} {
		recentAgo := []int64{120, 600, 36 * 3600}[userIndex]
		step := []int64{2100, 1800, 1500}[userIndex]
		for index := 0; index < 280; index++ {
			ip := callIPs[(index+userIndex)%len(callIPs)]
			switch index % 43 {
			case 0:
				ip = ""
			case 1:
				ip = "10.10.0.8"
			case 2:
				ip = "4.2.2.2"
			}
			result := "success"
			if index%19 == 0 {
				result = "failure"
			}
			quota := int64(1200 + (index%13)*750)
			if result == "failure" {
				quota = 0
			}
			event := networkEvent{
				ID: fmt.Sprintf("demo:call:%d:%03d", userID, index), Source: networkSourceCall,
				OccurredAt: now - recentAgo - int64(index)*step, UserID: userID, TokenID: userID*10 + int64(index%3),
				IP: ip, Result: result,
				Metadata: map[string]string{"quota": strconv.FormatInt(quota, 10), "model": models[index%len(models)], "ip_source": "demo_usage_log"},
			}
			if err := add(event); err != nil {
				return fmt.Errorf("seed demo calls: %w", err)
			}
		}
	}
	for _, old := range []struct {
		userID, days int64
	}{{104, 110}, {105, 12}, {106, 65}} {
		if err := add(networkEvent{ID: fmt.Sprintf("demo:old-call:%d", old.userID), Source: networkSourceCall,
			OccurredAt: now - old.days*86400, UserID: old.userID, TokenID: old.userID * 10,
			IP: "1.1.1.1", Result: "success", Metadata: map[string]string{"quota": "2500", "model": models[0]}}); err != nil {
			return err
		}
	}
	for _, userID := range []int64{101, 102, 103} {
		for index := 0; index < 5; index++ {
			device := "demo-browser-" + strconv.FormatInt(userID, 10)
			if userID == 103 && index == 0 {
				device += "-new"
			}
			if err := add(networkEvent{
				ID: fmt.Sprintf("demo:login:%d:%d", userID, index), Source: networkSourceLogin,
				OccurredAt: now - 900 - int64(index)*86400, UserID: userID, IP: []string{"1.1.1.1", "1.0.0.1", "9.9.9.9"}[int(userID-101)],
				Result: "success", BrowserID: device, Method: demoAutoGroupSource(userID),
				Device: map[string]string{"browser": "Demo browser", "platform": "Demo desktop", "timezone": "Asia/Shanghai"},
			}); err != nil {
				return err
			}
		}
	}
	for index := 0; index < 12; index++ {
		if err := add(networkEvent{
			ID: fmt.Sprintf("demo:attempt:%02d", index), Source: networkSourceLogin,
			OccurredAt: now - 1200 - int64(index)*45, UserID: 0, IP: "8.8.4.4", Result: "failure",
			ClaimedAccount: byID[103].Username, BrowserID: "demo-browser-unknown", Method: "password",
		}); err != nil {
			return err
		}
	}
	for index, userID := range []int64{101, 102, 103, 104, 105, 106} {
		if err := add(networkEvent{
			ID: fmt.Sprintf("demo:registration:%d", userID), Source: networkSourceRegistration,
			OccurredAt: byID[userID].CreatedAt, UserID: userID, IP: []string{"1.1.1.1", "1.0.0.1", "9.9.9.9"}[index%3],
			Result: "success", ClaimedAccount: byID[userID].Username, BrowserID: "demo-browser-" + strconv.FormatInt(userID, 10),
			Method: demoAutoGroupSource(userID),
		}); err != nil {
			return err
		}
	}
	if err := seedExtendedDemoEvents(now, byID, add); err != nil {
		return err
	}
	state := s.userState()
	state.mu.Lock()
	for id, count := range requests {
		user := state.demo[id]
		user.RequestCount, user.UsedQuota = count, quotas[id]
		if at, found := createdAt[id]; found {
			user.CreatedAt, user.CreatedAtKnown = at, true
		}
		user.LastLoginAt = lastLogin[id]
		state.demo[id] = user
	}
	for id, at := range lastLogin {
		user := state.demo[id]
		user.LastLoginAt = at
		state.demo[id] = user
	}
	state.mu.Unlock()
	if err := s.seedDemoRiskRecords(ctx, now); err != nil {
		return err
	}
	if err := seedDemoAutoGroup(s, now); err != nil {
		return err
	}
	if err := s.network.setMeta(ctx, "first_call_import", strconv.FormatInt(now, 10)); err != nil {
		return err
	}
	if err := s.network.setMeta(ctx, "last_call_sync", strconv.FormatInt(now, 10)); err != nil {
		return err
	}
	return s.network.setMeta(ctx, "demo_seed_complete", "true")
}

func seedExtendedDemoEvents(now int64, users map[int64]managedUserRecord, add func(networkEvent) error) error {
	ips := []string{
		"1.1.1.1", "1.0.0.1", "8.8.8.8", "8.8.4.4", "9.9.9.9", "149.112.112.112",
		"93.184.216.34", "208.67.222.222", "208.67.220.220", "80.67.169.12", "185.199.108.153",
		"2606:4700:4700::1111", "2001:4860:4860::8888", "4.2.2.2", "8.26.56.26",
		"", "10.10.0.8", "192.168.1.42", "fd00::8", "127.0.0.1", "::1", "fe80::1234",
		"203.0.113.7", "2001:db8::7", "invalid-demo-ip",
	}
	for userID := int64(107); userID <= 160; userID++ {
		user, found := users[userID]
		if !found {
			return fmt.Errorf("demo account %d is unavailable", userID)
		}
		profile := (userID - 107) % 10
		total, recentAgo, step := 0, int64(180), int64(1800)
		switch profile {
		case 0:
			total = 180
			if userID == 107 {
				total, step = 1200, 360
			}
		case 1:
			total = 180
		case 2:
			total, step = 40, 7200
		case 3:
			total, step = 5, 18000
		case 4:
			total, recentAgo, step = 8, 3*86400, 6*3600
		case 5:
			total, recentAgo, step = 12, 12*86400, 12*3600
		case 6:
			total, recentAgo, step = 8, 65*86400, 86400
		case 7:
			total, recentAgo, step = 5, 120*86400, 86400
		}
		for index := 0; index < total; index++ {
			result := "success"
			if index%17 == 0 {
				result = "failure"
			}
			if err := add(networkEvent{
				ID: fmt.Sprintf("demo:call:%d:extended:%04d", userID, index), Source: networkSourceCall,
				OccurredAt: now - recentAgo - int64(index)*step, UserID: userID, TokenID: userID*10 + int64(index%3),
				IP: ips[(int(userID)+index)%len(ips)], Result: result,
				Metadata: map[string]string{"ip_source": "demo_usage_log"},
			}); err != nil {
				return fmt.Errorf("seed extended demo calls: %w", err)
			}
		}
		// Only call-active fixtures receive daily comparison history.
		if profile <= 2 {
			for day := 0; day < 60; day++ {
				if err := add(networkEvent{
					ID: fmt.Sprintf("demo:call:%d:history:%02d", userID, day), Source: networkSourceCall,
					OccurredAt: now - 1800 - int64(day)*86400 - userID%7*90,
					UserID:     userID, TokenID: userID*10 + int64(day%3), IP: ips[(day+int(userID))%len(ips)],
					Result: "success", Metadata: map[string]string{"ip_source": "demo_usage_log"},
				}); err != nil {
					return err
				}
			}
		}
		for index, day := range []int64{0, 1, 3, 7, 12, 20, 29, 45, 59} {
			at := now - 600 - day*86400 - userID%17*15
			if at < user.CreatedAt {
				continue
			}
			browser := fmt.Sprintf("demo-browser-%d", userID)
			platform, browserName := "Windows", "Chrome"
			if index == 0 && userID%5 == 0 {
				browser, platform, browserName = browser+"-mobile", "Android", "Chrome Mobile"
			} else if index == 1 && userID%4 == 0 {
				browser, platform, browserName = browser+"-laptop", "macOS", "Safari"
			}
			result := "success"
			if index%5 == 2 {
				result = "failure"
			} else if index == 0 && userID%9 == 0 {
				result = "pending"
			}
			if err := add(networkEvent{
				ID: fmt.Sprintf("demo:login:%d:extended:%d", userID, index), Source: networkSourceLogin,
				OccurredAt: at, UserID: userID, IP: ips[(int(userID)+index*3+4)%len(ips)], Result: result,
				BrowserID: browser, SessionID: fmt.Sprintf("demo-session-%d-%d", userID, index),
				Method: demoAutoGroupSource(userID), UserAgent: "Demo fixture browser",
				Device: map[string]string{"browser": browserName, "platform": platform, "timezone": "Asia/Shanghai", "language": "zh-CN"},
			}); err != nil {
				return fmt.Errorf("seed extended demo logins: %w", err)
			}
		}
		if err := add(networkEvent{
			ID: fmt.Sprintf("demo:registration:%d", userID), Source: networkSourceRegistration,
			OccurredAt: user.CreatedAt, UserID: userID, IP: ips[(int(userID)+7)%len(ips)],
			Result: "success", ClaimedAccount: user.Username, BrowserID: fmt.Sprintf("demo-browser-%d", userID),
			Method: demoAutoGroupSource(userID), Metadata: map[string]string{"identity_status": "fixture_account"},
		}); err != nil {
			return err
		}
	}
	for day := 0; day < 60; day++ {
		for _, source := range []string{networkSourceLogin, networkSourceRegistration} {
			if err := add(networkEvent{
				ID: fmt.Sprintf("demo:%s:unassociated:%02d", source, day), Source: source,
				OccurredAt: now - 1200 - int64(day)*86400, IP: ips[(day+6)%len(ips)], Result: "failure",
				ClaimedAccount: fmt.Sprintf("demo_unknown_account_%02d", day), BrowserID: "demo-browser-unassociated",
				Method: "password", Metadata: map[string]string{"identity_status": "unverified"},
			}); err != nil {
				return err
			}
		}
	}
	for index := 0; index < 12; index++ {
		if err := add(networkEvent{
			ID: fmt.Sprintf("demo:login:152:review:%02d", index), Source: networkSourceLogin,
			OccurredAt: now - 300 - int64(index)*30, UserID: 152, IP: "208.67.222.222",
			Result: "failure", BrowserID: "demo-browser-152-new", Method: "password",
		}); err != nil {
			return err
		}
	}
	if err := add(networkEvent{
		ID: "demo:login:152:new-device", Source: networkSourceLogin, OccurredAt: now - 90,
		UserID: 152, IP: "2606:4700:4700::1111", Result: "success", BrowserID: "demo-browser-152-new",
		Method: "password", Device: map[string]string{"browser": "Firefox", "platform": "Linux", "timezone": "Europe/London"},
	}); err != nil {
		return err
	}
	// Matrix fixture: three independent-looking accounts share one verified browser.
	for index := 0; index < 6; index++ {
		if err := add(networkEvent{
			ID: fmt.Sprintf("demo:login:158:matrix:%02d", index), Source: networkSourceLogin,
			OccurredAt: now - 240 - int64(index)*70, UserID: int64(157 + index%3), IP: "185.199.108.153",
			Result: "success", BrowserID: "demo-browser-matrix-157", Method: "password",
		}); err != nil {
			return err
		}
	}
	// Keep the actionable high-risk sample on an existing non-cancelled
	// account. Account 153 is a historical, cancelled fixture.
	const highRiskUserID int64 = 151
	for index := 0; index < 150; index++ {
		if err := add(networkEvent{
			ID: fmt.Sprintf("demo:call:%d:baseline:%03d", highRiskUserID, index), Source: networkSourceCall,
			OccurredAt: now - 7140 + int64(index)*20, UserID: highRiskUserID, TokenID: highRiskUserID * 10, IP: "1.1.1.1", Result: "success",
		}); err != nil {
			return err
		}
	}
	burstStart := (now - 120) / 60 * 60
	for index := 0; index < 660; index++ {
		if err := add(networkEvent{
			ID: fmt.Sprintf("demo:call:%d:burst:%03d", highRiskUserID, index), Source: networkSourceCall,
			OccurredAt: burstStart + int64(index%50), UserID: highRiskUserID, TokenID: highRiskUserID * 10,
			IP: ips[index%6], Result: "success",
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *dashboardService) seedDemoRiskRecords(ctx context.Context, now int64) error {
	users, err := s.managedUsers(ctx, 0)
	if err != nil {
		return err
	}
	activeUsers := make(map[int64]managedUserRecord, len(users))
	for _, user := range users {
		if user.Role < 10 && !user.deletedAtKnown {
			activeUsers[user.ID] = user
		}
	}
	events, err := s.network.Snapshot(ctx)
	if err != nil {
		return err
	}
	metadata, err := s.network.Metadata(ctx)
	if err != nil {
		return err
	}
	assessmentContext := context.WithValue(ctx, riskSnapshotKey{}, newRiskEventSnapshot(events, metadata))
	ids := []int64{101, 102, 103}
	for id := int64(107); id <= 160; id++ {
		ids = append(ids, id)
	}
	for _, userID := range ids {
		if _, exists := activeUsers[userID]; !exists {
			continue
		}
		assessment, err := s.assessRiskUser(assessmentContext, userID, defaultRiskConfig(), fmt.Sprintf("demo-review-%d", userID), now)
		if err != nil {
			return fmt.Errorf("seed demo assessment: %w", err)
		}
		assessment.DataNotes = append(assessment.DataNotes, "演示样例：网络画像与行为仅用于本地演示，不代表真实用户或真实 IP 定位。")
		if err := s.riskModule().put(ctx, "assessment:"+assessment.ID, assessment); err != nil {
			return err
		}
	}
	for index := 0; index < 18; index++ {
		userIDs := make([]int64, 0, 8)
		for _, id := range []int64{101, 102, 103, 107, 108, 109, 151, 152} {
			if _, exists := activeUsers[id]; exists {
				userIDs = append(userIDs, id)
			}
		}
		job := riskScanJob{
			ID: fmt.Sprintf("demo-scan-%02d", index+1), State: "complete", Config: defaultRiskConfig(), UserIDs: userIDs,
			Cursor: len(userIDs), Scanned: len(userIDs), Assessed: len(userIDs), Errors: []string{},
			CreatedAt: now - int64(index+1)*3600, UpdatedAt: now - int64(index+1)*3600 + 45,
			WindowEnd: now - int64(index+1)*3600,
		}
		if index%7 == 6 {
			job.State, job.Cursor, job.Scanned, job.Assessed = "failed", 3, 3, 2
			job.Errors = []string{"演示样例：部分账号事件未能读取，等待复核。"}
		}
		if err := s.riskModule().put(ctx, "job:"+job.ID, job); err != nil {
			return err
		}
	}
	for _, sample := range []struct {
		userID int64
		action string
		key    string
	}{
		{154, "ban", "demo-fixture:manual-ban:154"},
		{155, "ban", "demo-fixture:manual-ban:155"},
		{155, "unban", "demo-fixture:manual-release:155"},
	} {
		if _, exists := activeUsers[sample.userID]; !exists {
			continue
		}
		var notes []string
		if sample.action == "ban" {
			notes = []string{"演示管理员已完成人工复核，后续处理请查看本站审计记录。"}
		}
		if _, err := s.performRiskUserAction(ctx, sample.userID, sample.action,
			"演示样例：人工复核与状态恢复", "demo-fixture", sample.key, notes...); err != nil {
			return fmt.Errorf("seed local demo action: %w", err)
		}
	}
	reviewUsers := make([]int64, 0, 15)
	for _, user := range users {
		if user.ID < 117 || user.Role >= 10 || user.deletedAtKnown {
			continue
		}
		reviewUsers = append(reviewUsers, user.ID)
		if len(reviewUsers) == 15 {
			break
		}
	}
	for index, userID := range reviewUsers {
		current, err := s.riskModule().user(ctx, userID)
		if err != nil {
			return err
		}
		state := []string{"failed", "uncertain", "conflict", "noop", "prepared"}[index%5]
		record := riskAction{
			ID: fmt.Sprintf("demo-action-review-%02d", index+1), UserID: userID, Action: "ban",
			Reason: "演示样例：待核对的历史人工处置，尚未更改当前账号状态", Operator: "demo-fixture", State: state,
			Before: current, After: current, CreatedAt: now - int64(index+1)*7200, UpdatedAt: now - int64(index+1)*7200 + 30,
		}
		if state != "noop" && state != "prepared" {
			record.Error = "演示样例：记录需要人工核对，不代表真实上游故障"
		}
		event := "action_" + state
		if state == "conflict" {
			event = "action_uncertain"
		}
		if err := s.riskModule().saveAction(ctx, record, event); err != nil {
			return err
		}
	}
	return nil
}
