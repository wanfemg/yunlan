package main

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"
)

type checkinUsage struct {
	Requests, Consumed, LifetimeRequests, LifetimeConsumed, LastUsed int64
}

type checkinPaymentAggregate struct {
	wallet, subscription, windowWallet, windowSubscription *big.Rat
	paidCount, uncertain                                   int64
}

type checkinRedemptionAggregate struct {
	count, quota int64
}

func inspectCheckinColumns(ctx context.Context, db queryDB, table string, required ...string) (map[string]bool, error) {
	if db.db == nil {
		return nil, errors.New("数据源未配置")
	}
	// Table names are compile-time constants supplied by this module.
	rows, err := db.query(ctx, "SELECT * FROM "+table+" WHERE 1 = 0")
	if err != nil {
		return nil, errors.New("记录表不可用")
	}
	columns, err := rows.Columns()
	_ = rows.Close()
	if err != nil {
		return nil, errors.New("字段信息不可用")
	}
	has := make(map[string]bool, len(columns))
	for _, column := range columns {
		has[strings.ToLower(column)] = true
	}
	for _, column := range required {
		if !has[column] {
			return has, fmt.Errorf("当前版本缺少 %s 字段", column)
		}
	}
	return has, nil
}

func checkinStatus(id, name string, err error, complete bool) checkinSource {
	if err != nil {
		message, status := name+"不可用，请检查数据表、字段和连接权限", "unavailable"
		if strings.HasPrefix(err.Error(), "当前版本缺少 ") {
			message, status = name+"："+err.Error(), "unsupported"
		}
		return checkinSource{ID: id, Status: status, Message: message, Complete: false}
	}
	return checkinSource{ID: id, Status: "ok", Message: name + "已读取", Complete: complete}
}

func (s *dashboardService) readCheckinAccounts(ctx context.Context, f checkinFilters) ([]managedUserRecord, checkinSource, error) {
	if s.config.Mode == "demo" {
		users, err := s.managedUsers(ctx, 0)
		return users, checkinSource{ID: "main_users", Status: "demo", Message: "本地演示账号", Complete: true}, err
	}
	db := s.main
	has, err := inspectCheckinColumns(ctx, db, "users", "id", "username", "status")
	if err != nil {
		return nil, checkinStatus("main_users", "主库账号", err, false), err
	}
	column := func(name, fallback string) string {
		if !has[name] {
			return fallback
		}
		return "COALESCE(" + tokenIdentifier(db.driver, name) + "," + fallback + ")"
	}
	query := "SELECT id,username," + column("display_name", "''") + "," + column("group", "'default'") +
		",status," + column("quota", "0") + "," + column("created_at", "0") + "," +
		column("request_count", "0") + "," + column("used_quota", "0") + " FROM users WHERE 1=1"
	if has["deleted_at"] {
		query += " AND deleted_at IS NULL"
	}
	if has["created_at"] {
		query += " AND (created_at<=? OR created_at IS NULL)"
	}
	var args []any
	if has["created_at"] {
		args = append(args, f.AsOf)
	}
	rows, err := db.query(ctx, query, args...)
	if err != nil {
		return nil, checkinStatus("main_users", "主库账号", errors.New("查询失败"), false), err
	}
	defer rows.Close()
	users := []managedUserRecord{}
	for rows.Next() {
		var user managedUserRecord
		if err := rows.Scan(&user.ID, &user.Username, &user.DisplayName, &user.Group, &user.Status, &user.Quota, &user.CreatedAt, &user.RequestCount, &user.UsedQuota); err != nil {
			return nil, checkinStatus("main_users", "主库账号", errors.New("读取失败"), false), err
		}
		user.CreatedAtKnown = has["created_at"] && user.CreatedAt > 0
		users = append(users, user)
	}
	return users, checkinStatus("main_users", "主库账号", rows.Err(), true), rows.Err()
}

func (s *dashboardService) readCheckinRecords(ctx context.Context, f checkinFilters) ([]checkinRecord, checkinSource, error) {
	db := s.main
	_, err := inspectCheckinColumns(ctx, db, "checkins", "id", "user_id", "checkin_date", "quota_awarded", "created_at")
	if err != nil {
		return nil, checkinStatus("checkins", "签到记录", err, false), err
	}
	// Date validation is done against the original business date. Do not apply
	// a lower bound: streaks and classification need all observed history.
	rows, err := db.query(ctx, `SELECT id,user_id,checkin_date,quota_awarded,created_at FROM checkins WHERE created_at<=?`, f.AsOf)
	if err != nil {
		return nil, checkinStatus("checkins", "签到记录", errors.New("查询失败"), false), err
	}
	defer rows.Close()
	records := []checkinRecord{}
	for rows.Next() {
		var record checkinRecord
		if err := rows.Scan(&record.ID, &record.UserID, &record.CheckinDate, &record.QuotaAwarded, &record.CreatedAt); err != nil {
			return nil, checkinStatus("checkins", "签到记录", errors.New("读取失败"), false), err
		}
		records = append(records, record)
	}
	source := checkinStatus("checkins", "签到记录", rows.Err(), false)
	if rows.Err() == nil {
		source.Message = "已读取当前主库保留的签到记录；上游迁移、清理前的完整历史未确认"
	}
	return records, source, rows.Err()
}

func (s *dashboardService) readCheckinUsage(ctx context.Context, f checkinFilters) (map[int64]checkinUsage, checkinSource, error) {
	if s.logs.db == nil {
		err := errors.New("日志库未配置")
		return nil, checkinStatus("logs", "调用日志", err, false), err
	}
	rows, err := s.logs.query(ctx, `SELECT user_id,COUNT(*),
		COALESCE(SUM(CASE WHEN type=? THEN quota ELSE 0 END),0),
		COUNT(CASE WHEN created_at>=? AND created_at<? THEN 1 END),
		COALESCE(SUM(CASE WHEN type=? AND created_at>=? AND created_at<? THEN quota ELSE 0 END),0),
		MAX(created_at) FROM logs WHERE user_id>0 AND created_at<=? AND type IN (?,?) GROUP BY user_id`,
		logConsume, f.start.Unix(), f.end.Unix(), logConsume, f.start.Unix(), f.end.Unix(),
		f.AsOf, logConsume, logError)
	if err != nil {
		return nil, checkinStatus("logs", "调用日志", errors.New("查询失败或版本字段不支持"), false), err
	}
	defer rows.Close()
	result := map[int64]checkinUsage{}
	for rows.Next() {
		var id int64
		var usage checkinUsage
		if err := rows.Scan(&id, &usage.LifetimeRequests, &usage.LifetimeConsumed, &usage.Requests, &usage.Consumed, &usage.LastUsed); err != nil {
			return nil, checkinStatus("logs", "调用日志", errors.New("读取失败"), false), err
		}
		result[id] = usage
	}
	err = rows.Err()
	source := checkinStatus("logs", "调用日志", err, s.config.NetworkHistoryComplete)
	if err == nil && !s.config.NetworkHistoryComplete {
		source.Message = "已读取当前保留日志；全部历史覆盖尚未确认"
	}
	return result, source, err
}

func newCheckinPaymentAggregate() checkinPaymentAggregate {
	return checkinPaymentAggregate{wallet: new(big.Rat), subscription: new(big.Rat), windowWallet: new(big.Rat), windowSubscription: new(big.Rat)}
}

func checkinExternalPayment(method, provider string) bool {
	method, provider = strings.ToLower(strings.TrimSpace(method)), strings.ToLower(strings.TrimSpace(provider))
	if method == "balance" || provider == "balance" {
		return false
	}
	switch provider {
	case "epay", "stripe", "creem", "waffo", "waffo_pancake", "paypal":
		return true
	}
	switch method {
	case "alipay", "wxpay", "wechat", "zfb", "stripe", "creem", "waffo", "waffo_pancake", "paypal":
		return true
	}
	return false
}

func checkinMoney(value *big.Rat) string {
	// Database decimal/float text uses a finite decimal. Derive the exact
	// terminating scale so even tiny positive amounts cannot display as zero.
	denominator := new(big.Int).Set(value.Denom())
	scale := 0
	for _, prime := range []int64{2, 5} {
		power := 0
		for {
			quotient, remainder := new(big.Int), new(big.Int)
			quotient.QuoRem(denominator, big.NewInt(prime), remainder)
			if remainder.Sign() != 0 {
				break
			}
			denominator = quotient
			power++
		}
		if power > scale {
			scale = power
		}
	}
	if denominator.Cmp(big.NewInt(1)) != 0 {
		scale = max(18, scale)
	}
	text := value.FloatString(scale)
	if strings.Contains(text, ".") {
		text = strings.TrimRight(strings.TrimRight(text, "0"), ".")
	}
	return text
}

func (s *dashboardService) readCheckinPayments(ctx context.Context, f checkinFilters) (map[int64]checkinPaymentAggregate, []checkinSource, bool, bool) {
	result := map[int64]checkinPaymentAggregate{}
	types := []struct {
		table, source, name string
	}{{"subscription_orders", "subscriptions", "外部订阅订单"}, {"top_ups", "wallet", "钱包订单"}}
	sources := []checkinSource{}
	available := map[string]bool{}
	columns := map[string]map[string]bool{}
	for _, kind := range types {
		has, err := inspectCheckinColumns(ctx, s.main, kind.table,
			"id", "user_id", "money", "trade_no", "status", "create_time", "complete_time")
		columns[kind.table] = has
		available[kind.table] = err == nil
		source := checkinStatus(kind.source, kind.name, err, false)
		if err == nil {
			source.Message = kind.name + "已读取；历史完整性须由站长确认"
		}
		sources = append(sources, source)
	}
	// Subscription trade numbers are authoritative for wallet mirror removal,
	// even when a subscription was not successful or is outside the date range.
	mirrors := map[string]bool{}
	if available["subscription_orders"] {
		rows, err := s.main.query(ctx, "SELECT trade_no FROM subscription_orders WHERE trade_no<>'' AND create_time<=?", f.AsOf)
		if err == nil {
			for rows.Next() {
				var trade string
				if rows.Scan(&trade) == nil {
					mirrors[trade] = true
				}
			}
			err = rows.Err()
			rows.Close()
		}
		if err != nil {
			available["subscription_orders"] = false
			sources[0] = checkinStatus("subscriptions", "外部订阅订单", errors.New("交易号去重信息不可用"), false)
		}
	}
	for index, kind := range types {
		if !available[kind.table] {
			continue
		}
		has := columns[kind.table]
		method, provider, complete := "''", "''", "create_time"
		if has["payment_method"] {
			method = "COALESCE(payment_method,'')"
		}
		if has["payment_provider"] {
			provider = "COALESCE(payment_provider,'')"
		}
		if has["complete_time"] {
			complete = "CASE WHEN complete_time>0 THEN complete_time ELSE create_time END"
		}
		query := "SELECT user_id,money,trade_no," + method + "," + provider + ",create_time," + complete +
			" FROM " + kind.table + " WHERE status=? AND create_time<=?"
		rows, err := s.main.query(ctx, query, "success", f.AsOf)
		if err != nil {
			available[kind.table] = false
			sources[index] = checkinStatus(kind.source, kind.name, errors.New("查询失败"), false)
			continue
		}
		seen := map[string]bool{}
		for rows.Next() {
			var id, createdAt, completedAt int64
			var money, trade, method, provider string
			if err = rows.Scan(&id, &money, &trade, &method, &provider, &createdAt, &completedAt); err != nil {
				break
			}
			if completedAt > f.AsOf {
				continue
			}
			if kind.table == "top_ups" && trade != "" && mirrors[trade] {
				continue
			}
			if trade != "" && seen[trade] {
				continue
			}
			seen[trade] = trade != ""
			amount, valid := new(big.Rat).SetString(money)
			if !valid || amount.Sign() <= 0 {
				continue
			}
			value, exists := result[id]
			if !exists {
				value = newCheckinPaymentAggregate()
			}
			if !checkinExternalPayment(method, provider) {
				if strings.EqualFold(method, "balance") || strings.EqualFold(provider, "balance") {
					continue
				}
				value.uncertain++
				result[id] = value
				continue
			}
			if kind.table == "top_ups" && !available["subscription_orders"] {
				// A positive external wallet payment still proves this account
				// paid, but its amount may contain an undetectable mirror.
				value.uncertain++
			}
			value.paidCount++
			inWindow := completedAt >= f.start.Unix() && completedAt < f.end.Unix()
			if kind.table == "top_ups" {
				value.wallet.Add(value.wallet, amount)
				if inWindow {
					value.windowWallet.Add(value.windowWallet, amount)
				}
			} else {
				value.subscription.Add(value.subscription, amount)
				if inWindow {
					value.windowSubscription.Add(value.windowSubscription, amount)
				}
			}
			result[id] = value
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			available[kind.table] = false
			sources[index] = checkinStatus(kind.source, kind.name, errors.New("读取失败"), false)
		}
	}
	walletComplete := available["top_ups"] && available["subscription_orders"]
	if available["top_ups"] && !available["subscription_orders"] {
		sources[1].Status = "partial"
		sources[1].Message = "钱包付款事实已读取，订阅镜像无法核对，金额合计暂不展示"
	}
	return result, sources, walletComplete, available["subscription_orders"]
}

func (s *dashboardService) readCheckinRedemptions(ctx context.Context, f checkinFilters) (map[int64]checkinRedemptionAggregate, checkinSource, error) {
	_, err := inspectCheckinColumns(ctx, s.main, "redemptions", "used_user_id", "redeemed_time", "quota")
	if err != nil {
		return nil, checkinStatus("redemptions", "兑换记录", err, false), err
	}
	rows, err := s.main.query(ctx, `SELECT used_user_id,COUNT(*),COALESCE(SUM(quota),0)
		FROM redemptions WHERE used_user_id>0 AND redeemed_time>0 AND redeemed_time<=? GROUP BY used_user_id`, f.AsOf)
	if err != nil {
		return nil, checkinStatus("redemptions", "兑换记录", errors.New("查询失败"), false), err
	}
	defer rows.Close()
	result := map[int64]checkinRedemptionAggregate{}
	for rows.Next() {
		var id int64
		var value checkinRedemptionAggregate
		if err := rows.Scan(&id, &value.count, &value.quota); err != nil {
			return nil, checkinStatus("redemptions", "兑换记录", errors.New("读取失败"), false), err
		}
		result[id] = value
	}
	return result, checkinStatus("redemptions", "兑换记录", rows.Err(), false), rows.Err()
}

func (s *dashboardService) loadCheckinSnapshot(ctx context.Context, f checkinFilters) (checkinSnapshot, error) {
	data := checkinSnapshot{checkinRange: f.checkinRange,
		quality: map[string]int64{"duplicate_user_dates": 0, "invalid_dates": 0, "future_dates": 0, "negative_rewards": 0, "orphan_users": 0}}
	users, userSource, err := s.readCheckinAccounts(ctx, f)
	if err != nil {
		return data, err
	}
	var records []checkinRecord
	var usage map[int64]checkinUsage
	var payments map[int64]checkinPaymentAggregate
	var redemptions map[int64]checkinRedemptionAggregate
	var checkinsSource, logSource, redemptionSource checkinSource
	var paymentSources []checkinSource
	var walletOK, subscriptionOK, redemptionOK bool
	if s.config.Mode == "demo" {
		records, usage, payments, redemptions = s.demoCheckinData(ctx, f, users)
		checkinsSource = checkinSourceDemo("checkins", "本地演示签到记录", true)
		logSource = checkinSourceDemo("logs", "与用户管理共用演示调用记录", true)
		redemptionSource = checkinSourceDemo("redemptions", "演示兑换来源，不代表销售付款", true)
		paymentSources = []checkinSource{checkinSourceDemo("subscriptions", "演示外部订阅付款与余额转购", true), checkinSourceDemo("wallet", "演示钱包付款，已排除订阅镜像", true)}
		walletOK, subscriptionOK, redemptionOK = true, true, true
		data.checkinsOK, data.logsOK = true, true
	} else {
		records, checkinsSource, err = s.readCheckinRecords(ctx, f)
		data.checkinsOK = err == nil
		usage, logSource, err = s.readCheckinUsage(ctx, f)
		data.logsOK = err == nil
		payments, paymentSources, walletOK, subscriptionOK = s.readCheckinPayments(ctx, f)
		redemptions, redemptionSource, err = s.readCheckinRedemptions(ctx, f)
		redemptionOK = err == nil
	}
	reviews, reviewErr := s.readCheckinReviews()
	data.SourceStatus = append(data.SourceStatus, userSource, checkinsSource, logSource)
	data.SourceStatus = append(data.SourceStatus, paymentSources...)
	data.SourceStatus = append(data.SourceStatus, redemptionSource, checkinStatus("reviews", "站长运营标注", reviewErr, reviewErr == nil))
	for _, source := range data.SourceStatus {
		if source.Status == "unavailable" || source.Status == "unsupported" || source.Status == "partial" {
			data.Warnings = append(data.Warnings, source.Message)
		}
	}
	if s.config.Mode != "demo" {
		data.Warnings = append(data.Warnings, "订单记录可证明已观测付款；未付费与免费来源完整性须由站长确认，查询到零记录不等于完整历史。")
	}
	if !data.checkinsOK && f.scope == "participants" {
		data.Warnings = append(data.Warnings, "签到来源不可用，参与用户范围无法确定；切换全部用户仍可查看其余来源。")
	}
	userIDs := map[int64]bool{}
	for _, user := range users {
		userIDs[user.ID] = true
	}
	validRecords := []checkinRecord{}
	seenDates := map[string]bool{}
	today := time.Unix(f.AsOf, 0).In(f.start.Location()).Format("2006-01-02")
	for _, record := range records {
		if !userIDs[record.UserID] {
			data.quality["orphan_users"]++
			continue
		}
		date, err := time.ParseInLocation("2006-01-02", record.CheckinDate, f.start.Location())
		if err != nil || date.Format("2006-01-02") != record.CheckinDate {
			data.quality["invalid_dates"]++
			continue
		}
		if record.CheckinDate > today || record.CreatedAt > f.AsOf {
			data.quality["future_dates"]++
			continue
		}
		if record.QuotaAwarded < 0 {
			data.quality["negative_rewards"]++
		}
		key := strconv.FormatInt(record.UserID, 10) + ":" + record.CheckinDate
		if seenDates[key] {
			data.quality["duplicate_user_dates"]++
		}
		seenDates[key] = true
		validRecords = append(validRecords, record)
	}
	data.records = validRecords
	byUser := map[int64][]checkinRecord{}
	for _, record := range validRecords {
		byUser[record.UserID] = append(byUser[record.UserID], record)
	}
	for _, account := range users {
		user := checkinUser{ID: account.ID, Username: account.Username, DisplayName: account.DisplayName,
			Group: account.Group, Status: account.Status, Quota: account.Quota, CreatedAt: account.CreatedAt,
			ParticipationBasis: "可观测窗口参与率；签到功能启用历史未确认", CategoryReasons: []string{}}
		if data.checkinsOK {
			windowDates, allDates := map[string]bool{}, map[string]bool{}
			var totalCount, totalAward, windowCount, windowAward int64
			for _, record := range byUser[account.ID] {
				allDates[record.CheckinDate] = true
				totalCount++
				totalAward += record.QuotaAwarded
				if record.CheckinDate > user.LastCheckinDate {
					user.LastCheckinDate = record.CheckinDate
				}
				if record.CheckinDate >= f.StartDate && record.CheckinDate < f.EndDateExclusive {
					windowDates[record.CheckinDate] = true
					windowCount++
					windowAward += record.QuotaAwarded
				}
			}
			wc, ac := int64(len(windowDates)), int64(len(allDates))
			user.CheckinDays, user.CheckinCount, user.AwardedQuota = &wc, &wc, &windowAward
			user.LifetimeCheckinDays, user.LifetimeCheckinCount, user.LifetimeAwardedQuota = &ac, &totalCount, &totalAward
			_ = windowCount // reward sums deliberately retain anomalous duplicate rows.
			eligible := f.days
			if account.CreatedAtKnown {
				createdDay := time.Unix(account.CreatedAt, 0).In(f.start.Location()).Format("2006-01-02")
				if createdDay > f.StartDate {
					eligible = 0
					for day := f.start; day.Format("2006-01-02") < f.EndDateExclusive; day = day.AddDate(0, 0, 1) {
						if day.Format("2006-01-02") >= createdDay {
							eligible++
						}
					}
				}
			}
			if eligible > 0 {
				user.ParticipationRate = float64Pointer(min(1, float64(wc)/float64(eligible)))
			}
		}
		if data.logsOK {
			value := usage[account.ID]
			user.Requests, user.ConsumedQuota = int64Pointer(value.Requests), int64Pointer(value.Consumed)
			user.LifetimeRequests, user.LifetimeConsumed = int64Pointer(value.LifetimeRequests), int64Pointer(value.LifetimeConsumed)
			user.LastUsedAt = value.LastUsed
		}
		payment := payments[account.ID]
		if payment.wallet == nil {
			payment = newCheckinPaymentAggregate()
		}
		if walletOK {
			value, window := checkinMoney(payment.wallet), checkinMoney(payment.windowWallet)
			user.Payment.WalletMoney, user.Payment.WindowWalletMoney = &value, &window
		}
		if subscriptionOK {
			value, window := checkinMoney(payment.subscription), checkinMoney(payment.windowSubscription)
			user.Payment.SubscriptionMoney, user.Payment.WindowSubscriptionMoney = &value, &window
		}
		if walletOK && subscriptionOK {
			user.Payment.PaidCount = int64Pointer(payment.paidCount)
		} else if payment.paidCount > 0 {
			user.Payment.PaidCount = int64Pointer(payment.paidCount)
		}
		user.Payment.UnverifiedPayments = payment.uncertain
		user.Payment.RedemptionStatus = "unavailable"
		if redemptionOK {
			value := redemptions[account.ID]
			user.Payment.RedemptionCount, user.Payment.RedemptionQuota = &value.count, &value.quota
			user.Payment.RedemptionStatus = "none_observed"
			if value.count > 0 {
				user.Payment.RedemptionStatus = "unverified"
			}
		}
		// Operational confirmations describe facts about the account and apply
		// to the selected statistics snapshot even when saved after its as_of.
		if review, exists := reviews[account.ID]; exists {
			user.Review = &review
		} else if s.config.Mode == "demo" {
			user.Review = demoCheckinReview(account.ID, f.AsOf)
		}
		if user.Review != nil && user.Review.HistoryVerified && user.Review.PaidStatus == "none" {
			user.Payment.HistoryComplete = true
		}
		if s.config.Mode == "demo" {
			user.Payment.HistoryComplete = true
		}
		userLogsComplete := s.config.NetworkHistoryComplete
		if account.RequestCount > checkinValue(user.LifetimeRequests) || account.UsedQuota > checkinValue(user.LifetimeConsumed) {
			userLogsComplete = false
		}
		classifyCheckinUser(&user, data.logsOK, userLogsComplete, walletOK && subscriptionOK, redemptionOK)
		data.users = append(data.users, user)
	}
	sort.Slice(data.users, func(i, j int) bool { return data.users[i].ID < data.users[j].ID })
	return data, nil
}

func classifyCheckinUser(user *checkinUser, logsOK, logsComplete, paymentOK, redemptionsOK bool) {
	review := user.Review
	paid := checkinValue(user.Payment.PaidCount) > 0 || review != nil && review.PaidStatus == "paid"
	if paid {
		user.Category = "paid"
		user.CategoryReasons = append(user.CategoryReasons, "已观测成功外部付款，或站长确认线下付费；历史曾付费不因当前窗口无订单而清零")
		return
	}
	if !logsOK {
		user.Category = "unknown"
		user.CategoryReasons = append(user.CategoryReasons, "调用来源不可用，无法判断已使用或未使用")
		return
	}
	if checkinValue(user.LifetimeConsumed) > 0 {
		confirmedFree := review != nil && review.PaidStatus == "none" && review.HistoryVerified && review.Funding == "free_only"
		if paymentOK && user.Payment.UnverifiedPayments == 0 && redemptionsOK && confirmedFree {
			user.Category = "pure_free"
			user.CategoryReasons = append(user.CategoryReasons, "站长确认完整付款历史无付费且全部额度来源免费，账号已有正额度消费")
			if checkinValue(user.Payment.RedemptionCount) > 0 {
				user.Payment.RedemptionStatus = "confirmed_free_by_review"
			}
		} else {
			user.Category = "unpaid_usage"
			user.CategoryReasons = append(user.CategoryReasons, "已有正额度消费，未观测成功外部付款；免费来源或付款历史尚待确认")
			if !paymentOK {
				user.CategoryReasons = append(user.CategoryReasons, "钱包或订阅来源不可用，不能将缺失记录当作零付款")
			}
			if !redemptionsOK || checkinValue(user.Payment.RedemptionCount) > 0 {
				user.CategoryReasons = append(user.CategoryReasons, "兑换码来源需核实，不能自动当作免费或已付费")
			}
		}
		return
	}
	if !logsComplete {
		user.Category = "unknown"
		user.CategoryReasons = append(user.CategoryReasons, "当前保留日志未观测正消费，但全部历史覆盖不完整")
		return
	}
	if checkinValue(user.LifetimeRequests) > 0 {
		user.Category = "unpaid_usage"
		user.CategoryReasons = append(user.CategoryReasons, "仅观测到零额度调用；不作为已消耗奖励")
		return
	}
	if !paymentOK || !redemptionsOK || !user.Payment.HistoryComplete {
		user.Category = "unknown"
		user.CategoryReasons = append(user.CategoryReasons, "无已观测调用，付款或奖励来源完整性仍待确认")
		return
	}
	if user.Payment.UnverifiedPayments > 0 {
		user.Category = "unknown"
		user.CategoryReasons = append(user.CategoryReasons, "存在正金额成功订单，但支付渠道属性未能确认，须人工核实")
		return
	}
	if (checkinValue(user.Payment.RedemptionCount) > 0 || user.Quota > 0) && (review == nil || review.Funding != "free_only") {
		user.Category = "unknown"
		user.CategoryReasons = append(user.CategoryReasons, "余额或兑换码来源未知，不按余额反推免费来源")
		return
	}
	if checkinValue(user.LifetimeAwardedQuota) > 0 || user.Review != nil && user.Review.Funding == "free_only" && checkinValue(user.Payment.RedemptionQuota) > 0 {
		user.Category = "reward_only"
		user.CategoryReasons = append(user.CategoryReasons, "已领取可识别免费奖励，完整调用历史尚无请求")
		return
	}
	user.Category = "unused"
	user.CategoryReasons = append(user.CategoryReasons, "完整观测中未发现付款、可识别奖励或使用记录")
}
