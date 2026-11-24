package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestCacheRefreshSurvivesCanceledCaller(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	cache := NewDashboardCache(30*time.Second, func(ctx context.Context, period Period) (DashboardData, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-ctx.Done():
			return DashboardData{}, ctx.Err()
		case <-release:
			return DashboardData{Period: period}, nil
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	canceled := make(chan error, 1)
	go func() {
		_, err := cache.Get(ctx, Period7Days)
		canceled <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("cache refresh did not start")
	}
	cancel()
	select {
	case err := <-canceled:
		if !errors.Is(err, context.Canceled) {
			close(release)
			t.Fatalf("canceled request returned %v", err)
		}
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("canceled request did not return")
	}
	close(release)
	waiter, stopWait := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopWait()
	data, err := cache.Get(waiter, Period7Days)
	if err != nil || data.Period != Period7Days || calls.Load() != 1 {
		t.Fatalf("remaining request did not reuse the refresh: %+v, %v, %d refreshes", data, err, calls.Load())
	}
}
