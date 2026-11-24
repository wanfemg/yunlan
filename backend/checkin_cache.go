package main

import (
	"context"
	"fmt"
	"time"
)

// Each request's filters and pagination are applied outside the cached batch.
// A fixed statistics point therefore shares one primary/log source read
// across overview, list, detail, pagination and export.
func (s *dashboardService) cachedCheckinSnapshot(ctx context.Context, f checkinFilters) (checkinSnapshot, error) {
	module := s.checkinState()
	module.mu.Lock()
	if module.cache == nil {
		module.cache = map[string]checkinCacheEntry{}
	}
	if module.pending == nil {
		module.pending = map[string]*checkinPending{}
	}
	key := fmt.Sprintf("%d:%s:%s:%s:%d", f.AsOf, f.StartDate, f.EndDateExclusive, f.Timezone, module.version)
	if entry, exists := module.cache[key]; exists && time.Now().Before(entry.expiresAt) {
		module.mu.Unlock()
		return entry.data, nil
	}
	pending := module.pending[key]
	if pending == nil {
		pending = &checkinPending{done: make(chan struct{})}
		module.pending[key] = pending
		version := module.version
		go func() {
			loadContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			data, err := s.loadCheckinSnapshot(loadContext, f)
			module.mu.Lock()
			defer module.mu.Unlock()
			pending.data, pending.err = data, err
			if err == nil && version == module.version {
				now := time.Now()
				for name, entry := range module.cache {
					if !now.Before(entry.expiresAt) {
						delete(module.cache, name)
					}
				}
				if len(module.cache) >= 8 {
					oldestName := ""
					oldestTime := now.Add(24 * time.Hour)
					for name, entry := range module.cache {
						if entry.expiresAt.Before(oldestTime) {
							oldestName, oldestTime = name, entry.expiresAt
						}
					}
					delete(module.cache, oldestName)
				}
				module.cache[key] = checkinCacheEntry{data: data, expiresAt: now.Add(20 * time.Second)}
			}
			delete(module.pending, key)
			close(pending.done)
		}()
	}
	module.mu.Unlock()
	select {
	case <-ctx.Done():
		return checkinSnapshot{}, ctx.Err()
	case <-pending.done:
		return pending.data, pending.err
	}
}
