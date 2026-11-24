package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	networkIntelligenceMaxFileBytes = 32 << 20
	networkIntelligenceMaxEntries   = 500000
)

type networkIntelligenceMatch struct {
	Kind   string `json:"kind"`
	Prefix string `json:"prefix"`
	Source string `json:"source"`
}

type networkIntelligenceResult struct {
	Available bool                       `json:"available"`
	Version   string                     `json:"version"`
	Matches   []networkIntelligenceMatch `json:"matches"`
}

type networkIntelligenceFileStatus struct {
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	Source       string `json:"source"`
	Status       string `json:"status"`
	PrefixCount  int    `json:"prefix_count"`
	LastModified int64  `json:"last_modified"`
	Version      string `json:"version"`
	Error        string `json:"error,omitempty"`
	Stale        bool   `json:"stale"`
	Expired      bool   `json:"expired"`
}

type networkIntelligenceStatus struct {
	Configured bool                            `json:"configured"`
	Available  bool                            `json:"available"`
	LoadedAt   int64                           `json:"loaded_at"`
	Version    string                          `json:"version"`
	Files      []networkIntelligenceFileStatus `json:"files"`
	Warnings   []string                        `json:"warnings"`
	Geo        map[string]bool                 `json:"geo"`
}

// A lookup masks the address once per populated prefix length, rather than
// comparing it against every network in the downloaded dataset.
type localPrefixDB struct {
	byBits map[int]map[netip.Addr]struct{}
	bits   []int
	count  int
}

func newLocalPrefixDB() *localPrefixDB {
	return &localPrefixDB{byBits: map[int]map[netip.Addr]struct{}{}}
}

func (db *localPrefixDB) add(prefix netip.Prefix) {
	prefix = prefix.Masked()
	group := db.byBits[prefix.Bits()]
	if group == nil {
		group = map[netip.Addr]struct{}{}
		db.byBits[prefix.Bits()] = group
		db.bits = append(db.bits, prefix.Bits())
	}
	if _, exists := group[prefix.Addr()]; !exists {
		group[prefix.Addr()] = struct{}{}
		db.count++
	}
}

func (db *localPrefixDB) match(address netip.Addr) (netip.Prefix, bool) {
	for _, bits := range db.bits {
		prefix := netip.PrefixFrom(address, bits)
		if !prefix.IsValid() {
			continue
		}
		prefix = prefix.Masked()
		if _, exists := db.byBits[bits][prefix.Addr()]; exists {
			return prefix, true
		}
	}
	return netip.Prefix{}, false
}

type networkIntelligenceDataset struct {
	status networkIntelligenceFileStatus
	index  *localPrefixDB
}

type networkIntelligenceStore struct {
	mu       sync.RWMutex
	status   networkIntelligenceStatus
	datasets []networkIntelligenceDataset
}

func parseNetworkIntelligencePrefix(value string, addressBits int) (netip.Prefix, error) {
	var prefix netip.Prefix
	var err error
	if strings.Contains(value, "/") {
		prefix, err = netip.ParsePrefix(value)
	} else {
		var address netip.Addr
		address, err = netip.ParseAddr(value)
		if err == nil && address.Zone() == "" {
			address = address.Unmap()
			prefix = netip.PrefixFrom(address, address.BitLen())
		}
	}
	if err != nil || !prefix.IsValid() || prefix.Addr().Zone() != "" || prefix.Bits() == 0 {
		return netip.Prefix{}, errors.New("dataset contains an invalid address or prefix")
	}
	if prefix.Addr().Is4In6() {
		if prefix.Bits() < 96 {
			return netip.Prefix{}, errors.New("dataset contains an invalid mapped address prefix")
		}
		prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
	}
	if prefix.Bits() == 0 || addressBits != 0 && prefix.Addr().BitLen() != addressBits {
		return netip.Prefix{}, errors.New("dataset address family does not match its file")
	}
	return prefix.Masked(), nil
}

func loadNetworkIntelligenceFile(directory, name string, addressBits int) (*localPrefixDB, int64, error) {
	file, err := os.Open(filepath.Join(directory, name))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, 0, os.ErrNotExist
		}
		return nil, 0, errors.New("dataset could not be opened")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, 0, errors.New("dataset is not a readable regular file")
	}
	if info.Size() == 0 || info.Size() > networkIntelligenceMaxFileBytes {
		return nil, info.ModTime().Unix(), errors.New("dataset size is outside the supported bounds")
	}
	index := newLocalPrefixDB()
	limited := &io.LimitedReader{R: file, N: networkIntelligenceMaxFileBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 4096), 4096)
	entries := 0
	for scanner.Scan() {
		value := strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "\ufeff"))
		if value == "" || strings.HasPrefix(value, "#") || strings.HasPrefix(value, ";") {
			continue
		}
		entries++
		if entries > networkIntelligenceMaxEntries {
			return nil, info.ModTime().Unix(), errors.New("dataset exceeds the supported entry limit")
		}
		prefix, err := parseNetworkIntelligencePrefix(value, addressBits)
		if err != nil {
			return nil, info.ModTime().Unix(), err
		}
		index.add(prefix)
	}
	if err := scanner.Err(); err != nil || limited.N == 0 {
		return nil, info.ModTime().Unix(), errors.New("dataset is truncated or exceeds its read limit")
	}
	if index.count == 0 {
		return nil, info.ModTime().Unix(), errors.New("dataset has no usable prefixes")
	}
	sort.Sort(sort.Reverse(sort.IntSlice(index.bits)))
	return index, info.ModTime().Unix(), nil
}

func loadNetworkIntelligence(directory string, now time.Time) *networkIntelligenceStore {
	store := &networkIntelligenceStore{status: networkIntelligenceStatus{
		Configured: strings.TrimSpace(directory) != "", LoadedAt: now.Unix(),
		Files: []networkIntelligenceFileStatus{}, Warnings: []string{}, Geo: map[string]bool{},
	}}
	if !store.status.Configured {
		store.status.Warnings = append(store.status.Warnings, "离线 IP 情报目录尚未配置。")
		return store
	}
	latestModified := int64(0)
	for _, spec := range []struct {
		name, kind, source string
		addressBits        int
	}{
		{"vpn-ipv4.txt", "vpn", "x4bnet_vpn", 32},
		{"vpn-ipv6.txt", "vpn", "x4bnet_vpn", 128},
		{"non-residential-ipv4.txt", "non_residential", "x4bnet_non_residential", 32},
		{"non-residential-ipv6.txt", "non_residential", "x4bnet_non_residential", 128},
		{"tor-exits.txt", "tor", "tor_project_exit_list", 0},
	} {
		state := networkIntelligenceFileStatus{Name: spec.name, Kind: spec.kind, Source: spec.source}
		index, modified, err := loadNetworkIntelligenceFile(directory, spec.name, spec.addressBits)
		state.LastModified = modified
		if modified > 0 {
			state.Version = time.Unix(modified, 0).UTC().Format(time.RFC3339)
		}
		if err != nil {
			state.Status = "invalid"
			state.Error = err.Error()
			if errors.Is(err, os.ErrNotExist) {
				state.Status, state.Error = "missing", "dataset is not installed"
			}
			store.status.Warnings = append(store.status.Warnings, state.Name+"："+state.Error)
		} else {
			state.Status, state.PrefixCount = "loaded", index.count
			store.datasets = append(store.datasets, networkIntelligenceDataset{status: state, index: index})
			store.status.Available = true
			latestModified = max(latestModified, modified)
		}
		store.status.Files = append(store.status.Files, state)
	}
	if latestModified > 0 {
		store.status.Version = time.Unix(latestModified, 0).UTC().Format(time.RFC3339)
	}
	// The manifest supplies a deployment version only; dataset validity is
	// always established by parsing the bounded, fixed-name data files.
	if file, err := os.Open(filepath.Join(directory, "manifest.json")); err == nil {
		var manifest struct {
			Version string `json:"version"`
		}
		decoder := json.NewDecoder(io.LimitReader(file, 64<<10))
		if decoder.Decode(&manifest) == nil && validLogFilter(manifest.Version, 128) && manifest.Version != "" {
			store.status.Version = manifest.Version
		}
		_ = file.Close()
	}
	return store
}

func (n *networkStore) ipIntelligence() *networkIntelligenceStore {
	n.intelligenceOnce.Do(func() {
		n.intelligenceMu.Lock()
		defer n.intelligenceMu.Unlock()
		n.intelligence = loadNetworkIntelligence(n.service.config.IPIntelligenceDir, n.clock())
	})
	n.intelligenceMu.RLock()
	store := n.intelligence
	n.intelligenceMu.RUnlock()
	return store
}

func networkDatasetFreshness(state networkIntelligenceFileStatus, now int64) networkIntelligenceFileStatus {
	staleAfter, expireAfter := int64(3*86400), int64(14*86400)
	if state.Kind == "tor" {
		staleAfter, expireAfter = 86400, 3*86400
	}
	if state.Status == "loaded" && state.LastModified > 0 {
		state.Stale = now-state.LastModified > staleAfter
		state.Expired = now-state.LastModified > expireAfter
	}
	return state
}

func (n *networkStore) ipIntelligenceStatus() networkIntelligenceStatus {
	store := n.ipIntelligence()
	store.mu.RLock()
	status := store.status
	status.Files = append([]networkIntelligenceFileStatus{}, store.status.Files...)
	status.Warnings = append([]string{}, store.status.Warnings...)
	store.mu.RUnlock()
	status.Available = false
	for i, state := range status.Files {
		state = networkDatasetFreshness(state, n.clock().Unix())
		status.Files[i] = state
		if state.Status == "loaded" && !state.Expired {
			status.Available = true
		}
		if state.Expired {
			status.Warnings = append(status.Warnings, state.Name+"：名单已过期，已停止将其命中作为当前代理证据。")
		} else if state.Stale {
			status.Warnings = append(status.Warnings, state.Name+"：名单更新时间较早，请更新数据。")
		}
	}
	n.openGeo()
	n.geoMu.RLock()
	status.Geo = map[string]bool{
		"city_available": n.city != nil, "asn_available": n.asn != nil,
		"isp_available": n.isp != nil, "anonymous_available": n.anonymous != nil,
	}
	n.geoMu.RUnlock()
	return status
}

func (n *networkStore) applyIPIntelligence(info *networkIPInfo) {
	address, err := netip.ParseAddr(info.IP)
	if err != nil || info.AddressClass != "public" {
		return
	}
	store := n.ipIntelligence()
	store.mu.RLock()
	defer store.mu.RUnlock()
	result := &networkIntelligenceResult{
		Available: false, Version: store.status.Version, Matches: []networkIntelligenceMatch{},
	}
	applied := false
	for _, dataset := range store.datasets {
		if networkDatasetFreshness(dataset.status, n.clock().Unix()).Expired {
			continue
		}
		result.Available = true
		prefix, matched := dataset.index.match(address.Unmap())
		if !matched {
			continue
		}
		result.Matches = append(result.Matches, networkIntelligenceMatch{
			Kind: dataset.status.Kind, Prefix: prefix.String(), Source: dataset.status.Source,
		})
		switch dataset.status.Kind {
		case "vpn", "tor":
			// Positive lists can prove a listed match only. A non-match must
			// never clear an unknown field or contradict an explicit provider.
			if value := info.Proxy[dataset.status.Kind]; value == "" || value == "unknown" {
				info.Proxy[dataset.status.Kind] = "yes"
				info.FieldSources["proxy_"+dataset.status.Kind] = dataset.status.Source
				applied = true
			}
		case "non_residential":
			if info.NetworkType == "" || info.NetworkType == "unknown" {
				info.NetworkType = "non_residential"
				info.FieldSources["network_type"] = dataset.status.Source
				applied = true
			}
		}
	}
	info.Intelligence = result
	if applied {
		if info.DataSource == "" || info.DataSource == "unavailable" {
			info.DataSource = "offline_intelligence"
		} else if info.DataSource == "offline_maxmind" {
			info.DataSource = "mixed_offline"
		}
		if info.ProxyVersion == "" {
			info.ProxyVersion = store.status.Version
		}
	}
}

func (s *dashboardService) ipIntelligenceStatusHTTP(c *gin.Context) {
	if s.network == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "error": "IP 情报模块尚未初始化"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": s.network.ipIntelligenceStatus()})
}
