package main

import (
	"context"
	"fmt"
)

// The journal describes complete local transitions ending in the actual user
// group. Reverted examples stay marked; every remaining assignment can be
// reverted through the normal handler without inventing a different user state.
func seedDemoAutoGroup(s *dashboardService, now int64) error {
	if s.config.Mode != "demo" {
		return nil
	}
	if s.autoGroup == nil {
		state, err := newAutoGroupState("", true)
		if err != nil {
			return err
		}
		s.autoGroup = state
	}
	users, err := s.managedUsers(context.Background(), 0)
	if err != nil {
		return fmt.Errorf("read demo grouping accounts: %w", err)
	}
	s.autoGroup.mu.Lock()
	defer s.autoGroup.mu.Unlock()
	if len(s.autoGroup.state.Logs) > 0 {
		return nil
	}
	next := cloneAutoGroupJournal(s.autoGroup.state)
	index := int64(0)
	for _, user := range users {
		if user.ID < 107 || user.ID > 130 {
			continue
		}
		group := normalizedUserGroup(user.Group)
		at := now - 3*3600 + index*180
		if at < user.CreatedAt {
			return fmt.Errorf("demo grouping history predates account %d", user.ID)
		}
		if index < 8 {
			addAutoGroupLog(&next, autoGroupLog{
				UserID: user.ID, Username: user.Username, OldGroup: "default", NewGroup: "standard",
				Action: "move", Source: demoAutoGroupSource(user.ID), Operator: "admin",
				CreatedAt: at - 120,
			})
			addAutoGroupLog(&next, autoGroupLog{
				UserID: user.ID, Username: user.Username, OldGroup: "standard", NewGroup: "default",
				Action: "move", Source: demoAutoGroupSource(user.ID), Operator: "admin",
				CreatedAt: at - 60,
			})
		}
		firstID := next.NextID
		addAutoGroupLog(&next, autoGroupLog{
			UserID: user.ID, Username: user.Username, OldGroup: "default", NewGroup: "community",
			Action: "assign", Source: demoAutoGroupSource(user.ID), Operator: "system",
			CreatedAt: at, Reverted: true,
		})
		addAutoGroupLog(&next, autoGroupLog{
			UserID: user.ID, Username: user.Username, OldGroup: "community", NewGroup: "default",
			Action: "revert", Source: demoAutoGroupSource(user.ID), Operator: "admin",
			CreatedAt: at + 60, Reverts: firstID,
		})
		if group != "default" {
			addAutoGroupLog(&next, autoGroupLog{
				UserID: user.ID, Username: user.Username, OldGroup: "default", NewGroup: group,
				Action: "assign", Source: demoAutoGroupSource(user.ID), Operator: "system",
				CreatedAt: at + 120,
			})
		}
		index++
	}
	if len(next.Logs) < 30 {
		return fmt.Errorf("demo grouping accounts produced only %d history entries", len(next.Logs))
	}
	next.Config.LastScan = now - 1800
	if next.Config.AutoScan {
		next.NextScan = next.Config.LastScan + int64(next.Config.ScanInterval)*60
	} else {
		next.NextScan = 0
	}
	if err := s.autoGroup.save(next); err != nil {
		return fmt.Errorf("seed demo grouping journal: %w", err)
	}
	// The legacy in-memory view mirrors the same records used by user management.
	s.autoGroup.users = demoAutoGroupUsers(users)
	return nil
}
