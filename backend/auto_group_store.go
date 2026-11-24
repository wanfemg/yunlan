package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

type autoGroupConfig struct {
	Enabled      bool              `json:"enabled"`
	Mode         string            `json:"mode"`
	TargetGroup  string            `json:"target_group"`
	SourceRules  map[string]string `json:"source_rules"`
	ScanInterval int               `json:"scan_interval_minutes"`
	AutoScan     bool              `json:"auto_scan_enabled"`
	Whitelist    []int64           `json:"whitelist_ids"`
	LastScan     int64             `json:"last_scan_time"`
}

type autoGroupLog struct {
	ID        int64  `json:"id"`
	UserID    int64  `json:"user_id"`
	Username  string `json:"username"`
	OldGroup  string `json:"old_group"`
	NewGroup  string `json:"new_group"`
	Action    string `json:"action"`
	Source    string `json:"source"`
	Operator  string `json:"operator"`
	CreatedAt int64  `json:"created_at"`
	Reverted  bool   `json:"reverted"`
	Reverts   int64  `json:"reverts,omitempty"`
}

type autoGroupJournal struct {
	Config        autoGroupConfig `json:"config"`
	Logs          []autoGroupLog  `json:"logs"`
	NextID        int64           `json:"next_id"`
	TotalAssigned int64           `json:"total_assigned"`
	NextScan      int64           `json:"next_scan"`
	Pending       []autoGroupLog  `json:"pending,omitempty"`
}

type autoGroupState struct {
	mu    sync.Mutex
	path  string
	state autoGroupJournal
	users []autoGroupUser
}

func newAutoGroupState(path string, demo bool) (*autoGroupState, error) {
	state := &autoGroupState{
		path: path,
		state: autoGroupJournal{
			Config: autoGroupConfig{Mode: "simple", ScanInterval: 60, SourceRules: map[string]string{}, Whitelist: []int64{}},
			NextID: 1,
		},
	}
	if demo {
		state.path = ""
		state.state.Config.Enabled = true
		state.state.Config.TargetGroup = "standard"
		state.state.Config.SourceRules = map[string]string{"github": "standard", "linux_do": "standard", "password": "standard"}
		state.state.Config.Whitelist = []int64{106}
		state.users = autoGroupDemoUsers()
	}
	if state.path == "" {
		return state, nil
	}
	data, err := os.ReadFile(state.path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil || json.Unmarshal(data, &state.state) != nil || state.state.NextID < 1 {
		return nil, errors.New("cannot read automatic grouping state")
	}
	if err := validateAutoGroupConfig(state.state.Config); err != nil {
		return nil, errors.New("automatic grouping state has an invalid configuration")
	}
	return state, nil
}

func (s *autoGroupState) save(next autoGroupJournal) error {
	if s.path != "" {
		if err := writeAutoGroupState(s.path, next); err != nil {
			return errors.New("自动分组状态写入失败")
		}
	}
	s.state = next
	return nil
}

func cloneAutoGroupJournal(state autoGroupJournal) autoGroupJournal {
	data, _ := json.Marshal(state)
	var clone autoGroupJournal
	_ = json.Unmarshal(data, &clone)
	return clone
}

func writeAutoGroupState(path string, state autoGroupJournal) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".auto-group-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	if err := errors.Join(writeErr, syncErr, file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
