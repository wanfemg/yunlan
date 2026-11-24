package main

import (
	"context"
	"sync"
	"time"
)

type dashboardLoad func(context.Context, Period) (DashboardData, error)

type cacheEntry struct {
	data      DashboardData
	expiresAt time.Time
}

type pendingDashboard struct {
	done chan struct{}
	data DashboardData
	err  error
}

// DashboardCache coalesces each period separately. A request that leaves early
// cannot cancel another caller's refresh.
type DashboardCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	load    dashboardLoad
	entries map[Period]cacheEntry
	pending map[Period]*pendingDashboard
}

func NewDashboardCache(ttl time.Duration, load dashboardLoad) *DashboardCache {
	return &DashboardCache{
		ttl: ttl, now: time.Now, load: load,
		entries: make(map[Period]cacheEntry),
		pending: make(map[Period]*pendingDashboard),
	}
}

func (cache *DashboardCache) Get(ctx context.Context, period Period) (DashboardData, error) {
	cache.mu.Lock()
	if entry, ok := cache.entries[period]; ok && cache.now().Before(entry.expiresAt) {
		cache.mu.Unlock()
		return entry.data, nil
	}
	pending, ok := cache.pending[period]
	if !ok {
		pending = &pendingDashboard{done: make(chan struct{})}
		cache.pending[period] = pending
		go cache.refresh(period, pending)
	}
	cache.mu.Unlock()

	select {
	case <-ctx.Done():
		return DashboardData{}, ctx.Err()
	case <-pending.done:
		return pending.data, pending.err
	}
}

func (cache *DashboardCache) refresh(period Period, pending *pendingDashboard) {
	data, err := cache.load(context.Background(), period)
	cache.mu.Lock()
	pending.data, pending.err = data, err
	if err == nil {
		cache.entries[period] = cacheEntry{data: data, expiresAt: cache.now().Add(cache.ttl)}
	}
	delete(cache.pending, period)
	close(pending.done)
	cache.mu.Unlock()
}
