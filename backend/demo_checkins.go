package main

import (
	"context"
	"strconv"
	"time"
)

func checkinSourceDemo(id, message string, complete bool) checkinSource {
	return checkinSource{ID: id, Status: "demo", Message: message, Complete: complete}
}

func demoCheckinReview(userID, asOf int64) *checkinReview {
	// Fixture confirmations illustrate the same explicit review required for
	// actual free-source classification. They never change managed accounts.
	switch userID % 8 {
	case 0, 3, 4, 6:
		return &checkinReview{PaidStatus: "none", Funding: "free_only", HistoryVerified: true,
			Reason: "演示确认：已核对完整付款记录与全部免费奖励来源", UpdatedAt: asOf - 60}
	case 5:
		return &checkinReview{PaidStatus: "paid", Funding: "unknown", HistoryVerified: true,
			Reason: "演示确认：线下付费充值记录", UpdatedAt: asOf - 60}
	}
	return nil
}

func (s *dashboardService) demoCheckinData(ctx context.Context, f checkinFilters, users []managedUserRecord) (
	[]checkinRecord, map[int64]checkinUsage, map[int64]checkinPaymentAggregate, map[int64]checkinRedemptionAggregate,
) {
	records := []checkinRecord{}
	usage := map[int64]checkinUsage{}
	payments := map[int64]checkinPaymentAggregate{}
	redemptions := map[int64]checkinRedemptionAggregate{}
	now := time.Unix(f.AsOf, 0).In(f.start.Location())
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	for _, user := range users {
		if user.Role >= 10 {
			continue
		}
		profile := user.ID % 8
		for index := 0; index < 365; index++ {
			day := today.AddDate(0, 0, -index)
			if day.Unix() < user.CreatedAt-user.CreatedAt%86400 {
				continue
			}
			// Empty profiles stay empty; the others cover uninterrupted,
			// broken and occasional natural-day participation.
			if profile == 4 || profile == 7 || profile == 1 && index%3 != 0 ||
				profile == 2 && index%5 == 1 || profile == 3 && index%9 == 2 ||
				profile == 6 && index%2 == 1 {
				continue
			}
			created := day.Add(7*time.Hour + time.Duration(user.ID%240)*time.Minute).Unix()
			if created > f.AsOf || created < user.CreatedAt {
				continue
			}
			records = append(records, checkinRecord{
				ID: user.ID*10000 + int64(index+1), UserID: user.ID, CheckinDate: day.Format("2006-01-02"),
				QuotaAwarded: 1000 + (user.ID*37+int64(index)*731)%9001, CreatedAt: created,
			})
		}
		if profile == 1 || profile == 2 || profile == 5 {
			value := newCheckinPaymentAggregate()
			amount, _ := value.wallet.SetString("29.90")
			value.wallet = amount
			value.paidCount = 1
			if profile == 2 {
				value.wallet.SetInt64(0)
				value.subscription.SetString("99.00")
			}
			if profile == 5 {
				// A balance-funded subscription is deliberately excluded.
				value.wallet.SetInt64(0)
				value.paidCount = 0
			}
			payments[user.ID] = value
		}
		if profile == 1 || profile == 6 || profile == 7 {
			redemptions[user.ID] = checkinRedemptionAggregate{count: 2, quota: 100000}
		}
	}
	// Use the exact shared API events shown in users, IP, tokens and logs.
	// No extra calls are invented to force a classification.
	if s.network != nil {
		events, err := s.network.Events(ctx, networkSourceCall, 0, f.AsOf+1)
		if err == nil {
			for _, event := range events {
				value := usage[event.UserID]
				value.LifetimeRequests++
				quota, _ := strconv.ParseInt(event.Metadata["quota"], 10, 64)
				if event.Result == "success" {
					value.LifetimeConsumed += quota
				}
				if event.OccurredAt >= f.start.Unix() && event.OccurredAt < f.end.Unix() {
					value.Requests++
					if event.Result == "success" {
						value.Consumed += quota
					}
				}
				if event.OccurredAt > value.LastUsed {
					value.LastUsed = event.OccurredAt
				}
				usage[event.UserID] = value
			}
		}
	}
	// These explicit edge cases reuse real fixture account IDs and never
	// mutate their calls: 115/125 have never called, 116 has missing history.
	filtered := records[:0]
	for _, record := range records {
		if record.UserID == 125 || record.UserID == 135 || record.UserID == 145 {
			continue
		}
		filtered = append(filtered, record)
	}
	records = filtered
	return records, usage, payments, redemptions
}
