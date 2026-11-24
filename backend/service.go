package main

import (
	"context"
	"errors"
	"time"
)

var errInvalidPeriod = errors.New("period must be 24h, 7d or 30d")

type dashboardService struct {
	config             Config
	main               queryDB
	logs               queryDB
	management         queryDB
	separateManagement bool
	autoGroup          *autoGroupState
	separateLogs       bool
	now                func() time.Time
	network            *networkStore
	userManagement     *userManagementState
	risk               *riskControlStore
	tokenDemo          *demoTokenStore
	redemptionDemo     *redemptionDemoStore
	checkins           *checkinModule
	connections        connectionSettings
	ownerAuth          *ownerAuthStore
}

func newDashboardService(config Config) (*dashboardService, error) {
	autoGroup, err := newAutoGroupState(config.AutoGroupStatePath, config.Mode == "demo")
	if err != nil {
		return nil, err
	}
	s := &dashboardService{config: config, now: time.Now, autoGroup: autoGroup}
	s.network = newNetworkStore(s)
	s.userManagement = newUserManagementState()
	s.risk = newRiskControlStore(s)
	if config.Mode == "demo" {
		// The generated fixture is the complete local observation history.
		s.config.NetworkHistoryComplete = true
		if err := s.seedDemoWorkspace(); err != nil {
			return nil, err
		}
		s.tokenDemo = newDemoTokenStore(s)
		return s, nil
	}
	main, err := openDatabase(config.MainDriver, config.MainDSN)
	if err != nil {
		return nil, errors.New("main database configuration is invalid")
	}
	s.main = queryDB{db: main, driver: config.MainDriver}
	// Use the main connection for writes unless an explicit management connection is set.
	s.management = s.main
	s.logs = s.main
	if config.LogsDSN != "" {
		s.separateLogs = true
		logs, err := openDatabase(config.LogsDriver, config.LogsDSN)
		if err != nil {
			_ = main.Close()
			return nil, errors.New("log database configuration is invalid")
		}
		s.logs = queryDB{db: logs, driver: config.LogsDriver}
	}
	if config.ManagementDSN != "" {
		management, err := openDatabase(config.ManagementDriver, config.ManagementDSN)
		if err != nil {
			s.Close()
			return nil, errors.New("management database configuration is invalid")
		}
		s.management = queryDB{db: management, driver: config.ManagementDriver}
		s.separateManagement = true
	}
	return s, nil
}

func (s *dashboardService) Close() {
	if s.network != nil {
		_ = s.network.Close()
	}
	if s.separateLogs && s.logs.db != nil {
		_ = s.logs.db.Close()
	}
	if s.main.db != nil {
		_ = s.main.db.Close()
	}
	if s.separateManagement && s.management.db != nil {
		_ = s.management.db.Close()
	}
}

func periodDuration(period Period) (time.Duration, error) {
	switch period {
	case Period24Hours:
		return 24 * time.Hour, nil
	case Period7Days:
		return 7 * 24 * time.Hour, nil
	case Period30Days:
		return 30 * 24 * time.Hour, nil
	default:
		return 0, errInvalidPeriod
	}
}

// Dashboard windows are rolling durations, with an equally long preceding
// comparison window. Both windows use [start, end), including at bucket edges.
func dashboardRanges(now time.Time, period Period, location *time.Location) (TimeRange, TimeRange, []TrendPoint, error) {
	duration, err := periodDuration(period)
	if err != nil {
		return TimeRange{}, TimeRange{}, nil, err
	}
	end := now.In(location)
	window := TimeRange{Start: end.Add(-duration), End: end}
	previous := TimeRange{Start: window.Start.Add(-duration), End: window.Start}
	return window, previous, trendBuckets(window, period, location), nil
}

func trendBuckets(window TimeRange, period Period, location *time.Location) []TrendPoint {
	points := make([]TrendPoint, 0, 32)
	for at := window.Start.In(location); at.Before(window.End); {
		points = append(points, TrendPoint{At: at})
		// Advance absolute minutes so repeated or skipped local times at DST
		// transitions remain ordered, including transitions at local midnight.
		for next := at.Truncate(time.Minute).Add(time.Minute); ; next = next.Add(time.Minute) {
			local := next.In(location)
			if next.Equal(window.End) || next.After(window.End) ||
				(period == Period24Hours && local.Minute() == 0) ||
				(period != Period24Hours && local.YearDay() != at.YearDay()) {
				at = local
				break
			}
		}
	}
	return points
}

func (s *dashboardService) Dashboard(ctx context.Context, period Period) (DashboardData, error) {
	if err := ctx.Err(); err != nil {
		return DashboardData{}, err
	}
	window, previous, points, err := dashboardRanges(s.now(), period, s.config.Location)
	if err != nil {
		return DashboardData{}, err
	}
	data := DashboardData{
		Mode: s.config.Mode, Period: period, Timezone: s.config.Timezone,
		GeneratedAt: window.End, Range: window, QuotaPerUnit: s.config.QuotaPerUnit,
		Trend: []TrendPoint{}, Models: []ModelUsage{}, Sources: []SourceStatus{}, Warnings: []string{},
	}
	if s.config.Mode == "demo" {
		return s.demo(ctx, data, previous, points)
	}
	return s.live(ctx, data, previous, points)
}
