package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/oschwald/maxminddb-golang"
)

type networkMaintenanceSettings struct {
	AutoUpdate    bool   `json:"auto_update"`
	IntervalHours int    `json:"interval_hours"`
	ProxyURL      string `json:"proxy_url"`
	RetentionDays int    `json:"retention_days"`
}
type networkMaintenanceState struct {
	Settings     networkMaintenanceSettings `json:"settings"`
	State        string                     `json:"state"`
	Stage        string                     `json:"stage"`
	StartedAt    int64                      `json:"started_at"`
	UpdatedAt    int64                      `json:"updated_at"`
	LastSuccess  int64                      `json:"last_success"`
	LastError    string                     `json:"last_error"`
	Version      string                     `json:"version"`
	NextUpdateAt int64                      `json:"next_update_at"`
}
type networkMaintenance struct {
	mu         sync.Mutex
	generation sync.Mutex
	once       sync.Once
	state      networkMaintenanceState
	wake       chan struct{}
}

func (n *networkStore) intelligenceRoot() string {
	if n.service.config.IPIntelligenceRoot != "" {
		return n.service.config.IPIntelligenceRoot
	}
	return n.service.config.IPIntelligenceDir
}
func (n *networkStore) maintenancePath() string {
	return filepath.Join(n.service.config.DataDir, "ip-maintenance.json")
}
func (n *networkStore) loadMaintenance() {
	n.maintenance.once.Do(func() {
		n.maintenance.mu.Lock()
		defer n.maintenance.mu.Unlock()
		n.maintenance.wake = make(chan struct{}, 1)
		state := networkMaintenanceState{State: "idle", Settings: networkMaintenanceSettings{AutoUpdate: true, IntervalHours: 24}}
		if body, err := os.ReadFile(n.maintenancePath()); err == nil && len(body) < 32<<10 {
			if json.Unmarshal(body, &state) != nil {
				state.State, state.LastError = "failed", "维护配置文件无效，请重新保存设置。"
			}
		}
		if state.Settings.IntervalHours < 6 || state.Settings.IntervalHours > 720 {
			state.Settings.IntervalHours = 24
		}
		if state.State == "running" || state.State == "queued" || state.State == "importing" {
			state.State, state.LastError = "interrupted", "上次更新未完成，旧版本继续提供识别；点击更新重试。"
		}
		if state.LastSuccess == 0 {
			var active struct {
				InstalledAt string `json:"installed_at"`
			}
			body, _ := os.ReadFile(filepath.Join(n.intelligenceRoot(), "active.json"))
			if json.Unmarshal(body, &active) == nil {
				if at, err := time.Parse(time.RFC3339Nano, active.InstalledAt); err == nil {
					state.LastSuccess = at.Unix()
				}
			}
		}
		n.maintenance.state = state
	})
}
func (n *networkStore) maintenanceStatus() networkMaintenanceState {
	n.loadMaintenance()
	n.maintenance.mu.Lock()
	defer n.maintenance.mu.Unlock()
	state := n.maintenance.state
	if state.Settings.AutoUpdate {
		state.NextUpdateAt = state.LastSuccess + int64(state.Settings.IntervalHours)*3600
	}
	return state
}
func (n *networkStore) persistMaintenanceLocked() error {
	n.maintenance.state.UpdatedAt = n.clock().Unix()
	return writePrivateJSON(n.maintenancePath(), n.maintenance.state)
}
func (n *networkStore) queueIntelligenceUpdate() (networkMaintenanceState, error) {
	n.loadMaintenance()
	n.maintenance.mu.Lock()
	defer n.maintenance.mu.Unlock()
	state := &n.maintenance.state
	if state.State != "running" && state.State != "queued" && state.State != "importing" {
		state.State, state.Stage, state.LastError = "queued", "等待更新", ""
		state.StartedAt = n.clock().Unix()
		if err := n.persistMaintenanceLocked(); err != nil {
			state.State = "failed"
			return *state, err
		}
		select {
		case n.maintenance.wake <- struct{}{}:
		default:
		}
	}
	return *state, nil
}
func validateMaintenanceSettings(settings networkMaintenanceSettings) error {
	if settings.IntervalHours < 6 || settings.IntervalHours > 720 || settings.RetentionDays != 0 && (settings.RetentionDays < 30 || settings.RetentionDays > 3650) {
		return errors.New("更新周期须为 6～720 小时，事件保留天数须为 0（永久保留）或 30～3650。")
	}
	if settings.ProxyURL != "" {
		proxy, err := url.Parse(settings.ProxyURL)
		if err != nil || proxy.Host == "" || proxy.User != nil || proxy.RawQuery != "" || proxy.Fragment != "" ||
			proxy.Path != "" && proxy.Path != "/" || proxy.Scheme != "http" && proxy.Scheme != "https" && proxy.Scheme != "socks5" {
			return errors.New("下载代理须为不含凭证的 HTTP(S) 或 SOCKS5 地址。")
		}
	}
	return nil
}
func registerIPMaintenanceRoutes(group *gin.RouterGroup, s *dashboardService) {
	group.GET("/maintenance", s.ipMaintenanceHTTP)
	group.PUT("/maintenance", s.saveIPMaintenanceHTTP)
	group.POST("/intelligence/update", s.updateIPIntelligenceHTTP)
	group.POST("/intelligence/reload", s.reloadIPIntelligenceHTTP)
	group.POST("/intelligence/import", s.importIPDatabaseHTTP)
	group.GET("/retention/preview", s.networkRetentionPreviewHTTP)
	group.POST("/retention/cleanup", s.networkRetentionCleanupHTTP)
	group.GET("/export", s.networkExportHTTP)
	group.GET("/attributes", s.networkAttributesReadHTTP)
}
func (s *dashboardService) ipMaintenanceHTTP(c *gin.Context) {
	c.JSON(200, gin.H{"success": true, "data": gin.H{"maintenance": s.network.maintenanceStatus(), "intelligence": s.network.ipIntelligenceStatus()}})
}
func (s *dashboardService) saveIPMaintenanceHTTP(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8192)
	var settings networkMaintenanceSettings
	if c.ShouldBindJSON(&settings) != nil {
		c.JSON(400, gin.H{"success": false, "error": "维护设置格式无效。"})
		return
	}
	if err := validateMaintenanceSettings(settings); err != nil {
		c.JSON(400, gin.H{"success": false, "error": err.Error()})
		return
	}
	n := s.network
	n.loadMaintenance()
	n.maintenance.mu.Lock()
	old := n.maintenance.state.Settings
	n.maintenance.state.Settings = settings
	err := n.persistMaintenanceLocked()
	if err != nil {
		n.maintenance.state.Settings = old
	}
	n.maintenance.mu.Unlock()
	if err != nil {
		c.JSON(503, gin.H{"success": false, "error": "维护设置保存失败。"})
		return
	}
	c.JSON(200, gin.H{"success": true, "data": n.maintenanceStatus()})
}
func (s *dashboardService) updateIPIntelligenceHTTP(c *gin.Context) {
	state, err := s.network.queueIntelligenceUpdate()
	if err != nil {
		c.JSON(503, gin.H{"success": false, "error": "数据更新任务无法保存。"})
		return
	}
	c.JSON(202, gin.H{"success": true, "data": state})
}
func (s *dashboardService) reloadIPIntelligenceHTTP(c *gin.Context) {
	s.network.maintenance.generation.Lock()
	defer s.network.maintenance.generation.Unlock()
	directory, err := resolveIPIntelligenceDirectory(s.network.intelligenceRoot())
	if err == nil {
		err = s.network.reloadIntelligence(directory)
	}
	if err != nil {
		c.JSON(400, gin.H{"success": false, "error": "新版本校验失败，当前数据未改变。"})
		return
	}
	c.JSON(200, gin.H{"success": true, "data": s.network.ipIntelligenceStatus()})
}
func (n *networkStore) reloadIntelligence(directory string) error {
	intelligence, next, err := n.prepareIntelligence(directory)
	if err != nil {
		return err
	}
	n.swapIntelligence(intelligence, next)
	return nil
}
func (n *networkStore) prepareIntelligence(directory string) (*networkIntelligenceStore, []*maxminddb.Reader, error) {
	n.openGeo()
	_ = n.ipIntelligence()
	intelligence := loadNetworkIntelligence(directory, n.clock())
	next := make([]*maxminddb.Reader, 4)
	for index, spec := range []struct{ name, kind, configured string }{
		{"GeoLite2-City.mmdb", "City", n.service.config.GeoCityPath}, {"GeoLite2-ASN.mmdb", "ASN", n.service.config.GeoASNPath},
		{"GeoIP2-ISP.mmdb", "ISP", n.service.config.GeoISPPath}, {"GeoIP2-Anonymous-IP.mmdb", "Anonymous", n.service.config.GeoAnonymousPath},
	} {
		path := filepath.Join(directory, spec.name)
		if !regularConfigFile(path) {
			path = spec.configured
		}
		if path == "" {
			continue
		}
		reader, err := maxminddb.Open(path)
		if err == nil && !strings.Contains(strings.ToLower(reader.Metadata.DatabaseType), strings.ToLower(spec.kind)) {
			err = errors.New("wrong database type")
			_ = reader.Close()
		}
		if err != nil {
			for _, opened := range next {
				if opened != nil {
					_ = opened.Close()
				}
			}
			return nil, nil, errors.New("a geolocation database is invalid")
		}
		next[index] = reader
	}
	available := intelligence.status.Available
	for _, reader := range next {
		available = available || reader != nil
	}
	if !available {
		return nil, nil, errors.New("no valid intelligence datasets")
	}
	return intelligence, next, nil
}
func (n *networkStore) swapIntelligence(intelligence *networkIntelligenceStore, next []*maxminddb.Reader) {
	n.geoMu.Lock()
	old := []*maxminddb.Reader{n.city, n.asn, n.isp, n.anonymous}
	n.city, n.asn, n.isp, n.anonymous = next[0], next[1], next[2], next[3]
	n.geoError = nil
	n.intelligenceMu.Lock()
	n.intelligence = intelligence
	n.intelligenceMu.Unlock()
	for _, reader := range old {
		if reader != nil {
			_ = reader.Close()
		}
	}
	n.geoMu.Unlock()
	n.revision.Add(1)
}

type networkDownloadSpec struct {
	Name string
	URLs []string
	Max  int64
	Kind string
}

var networkDownloads = []networkDownloadSpec{
	{"vpn-ipv4.txt", []string{"https://raw.githubusercontent.com/X4BNet/lists_vpn/main/output/vpn/ipv4.txt"}, 16 << 20, "list"},
	{"vpn-ipv6.txt", []string{"https://raw.githubusercontent.com/X4BNet/lists_vpn/main/output/vpn/ipv6.txt"}, 16 << 20, "list"},
	{"non-residential-ipv4.txt", []string{"https://raw.githubusercontent.com/X4BNet/lists_vpn/main/output/datacenter/ipv4.txt"}, 32 << 20, "list"},
	{"non-residential-ipv6.txt", []string{"https://raw.githubusercontent.com/X4BNet/lists_vpn/main/output/datacenter/ipv6.txt"}, 32 << 20, "list"},
	{"tor-exits.txt", []string{"https://check.torproject.org/torbulkexitlist"}, 8 << 20, "list"},
	{"X4BNet-README.md", []string{"https://raw.githubusercontent.com/X4BNet/lists_vpn/main/README.md"}, 1 << 20, "notice"},
	{"GeoLite-README.md", []string{"https://raw.githubusercontent.com/P3TERX/GeoLite.mmdb/master/README.md", "https://raw.githubusercontent.com/P3TERX/GeoLite.mmdb/main/README.md"}, 1 << 20, "notice"},
	{"GeoLite2-City.mmdb", []string{"https://github.com/P3TERX/GeoLite.mmdb/releases/latest/download/GeoLite2-City.mmdb", "https://raw.githubusercontent.com/adysec/IP_database/main/geolite/GeoLite2-City.mmdb"}, 160 << 20, "City"},
	{"GeoLite2-ASN.mmdb", []string{"https://github.com/P3TERX/GeoLite.mmdb/releases/latest/download/GeoLite2-ASN.mmdb", "https://raw.githubusercontent.com/adysec/IP_database/main/geolite/GeoLite2-ASN.mmdb"}, 64 << 20, "ASN"},
}

func downloadNetworkFile(ctx context.Context, client *http.Client, spec networkDownloadSpec, directory string) (map[string]any, error) {
	path := filepath.Join(directory, spec.Name)
	for _, origin := range spec.URLs {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "SiteAdmin-IPData/1.0")
		response, err := client.Do(req)
		if err != nil {
			continue
		}
		if response.StatusCode != 200 || response.ContentLength > spec.Max {
			_ = response.Body.Close()
			continue
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil {
			_ = response.Body.Close()
			return nil, err
		}
		hash := sha256.New()
		written, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(response.Body, spec.Max+1))
		_ = response.Body.Close()
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil || written == 0 || written > spec.Max {
			continue
		}
		valid := true
		if spec.Kind != "list" && spec.Kind != "notice" {
			reader, err := maxminddb.Open(path)
			valid = err == nil
			if reader != nil {
				valid = valid && strings.Contains(reader.Metadata.DatabaseType, spec.Kind)
				_ = reader.Close()
			}
		}
		if valid {
			return map[string]any{"name": spec.Name, "source": origin, "bytes": written, "sha256": hex.EncodeToString(hash.Sum(nil)), "retrieved_at": time.Now().UTC().Format(time.RFC3339)}, nil
		}
	}
	return nil, fmt.Errorf("%s 下载或校验失败。", spec.Name)
}
func copyNetworkFile(from, to string) error {
	input, err := os.Open(from)
	if err != nil {
		return err
	}
	defer input.Close()
	stat, err := input.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Size() > 192<<20 {
		return errors.New("invalid data source file")
	}
	output, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = io.Copy(output, io.LimitReader(input, 192<<20))
	if closeErr := output.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Chtimes(to, stat.ModTime(), stat.ModTime())
	}
	return err
}

var networkGenerationFiles = []string{
	"vpn-ipv4.txt", "vpn-ipv6.txt", "non-residential-ipv4.txt", "non-residential-ipv6.txt", "tor-exits.txt",
	"GeoLite2-City.mmdb", "GeoLite2-ASN.mmdb", "GeoIP2-ISP.mmdb", "GeoIP2-Anonymous-IP.mmdb",
	"DATA-SOURCES.txt", "X4BNet-README.md", "GeoLite-README.md",
}

func (n *networkStore) activateIntelligenceGeneration(root, directory, version string) error {
	n.maintenance.generation.Lock()
	defer n.maintenance.generation.Unlock()
	intelligence, next, err := n.prepareIntelligence(directory)
	if err != nil {
		return errors.New("数据热加载校验失败，旧版本保留。")
	}
	err = writePrivateJSON(filepath.Join(root, "active.json"), map[string]any{"directory": "versions/" + version, "version": version, "installed_at": time.Now().UTC().Format(time.RFC3339Nano)})
	if err != nil {
		for _, reader := range next {
			if reader != nil {
				_ = reader.Close()
			}
		}
		return errors.New("启动指针保存失败，当前数据未改变。")
	}
	n.swapIntelligence(intelligence, next)
	return nil
}

// Only discard a newly generated, inactive child directory. Resolve paths
// before recursive removal and never remove the configured data root.
func discardNetworkGeneration(root, directory string) {
	absoluteRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return
	}
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return
	}
	relative, err := filepath.Rel(absoluteRoot, resolved)
	if err != nil || filepath.Dir(relative) != "versions" || filepath.Base(relative) == "." {
		return
	}
	if active, err := resolveIPIntelligenceDirectory(root); err == nil {
		if resolvedActive, err := filepath.EvalSymlinks(active); err == nil && resolvedActive == resolved {
			return
		}
	}
	_ = os.RemoveAll(resolved)
}
func (n *networkStore) downloadIntelligence(ctx context.Context, settings networkMaintenanceSettings) (string, error) {
	root := n.intelligenceRoot()
	version := time.Now().UTC().Format("20060102T150405.000000000Z")
	directory := filepath.Join(root, "versions", version)
	if os.MkdirAll(directory, 0700) != nil {
		return "", errors.New("IP 数据目录不可写。")
	}
	activated := false
	defer func() {
		if !activated {
			discardNetworkGeneration(root, directory)
		}
	}()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if settings.ProxyURL != "" {
		proxy, _ := url.Parse(settings.ProxyURL)
		transport.Proxy = http.ProxyURL(proxy)
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 150 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 8 || req.URL.Scheme != "https" {
			return errors.New("unsupported redirect")
		}
		return nil
	}}
	files := []map[string]any{}
	for _, spec := range networkDownloads {
		n.maintenance.mu.Lock()
		n.maintenance.state.Stage = "下载并校验 " + spec.Name
		_ = n.persistMaintenanceLocked()
		n.maintenance.mu.Unlock()
		record, err := downloadNetworkFile(ctx, client, spec, directory)
		if err != nil {
			return "", fmt.Errorf("%s 旧版本仍在使用。", err.Error())
		}
		files = append(files, record)
	}
	for _, state := range loadNetworkIntelligence(directory, n.clock()).status.Files {
		if state.Status != "loaded" {
			return "", errors.New("识别名单校验未通过；旧版本仍在使用。")
		}
	}
	previous, _ := resolveIPIntelligenceDirectory(root)
	for _, name := range []string{"GeoIP2-ISP.mmdb", "GeoIP2-Anonymous-IP.mmdb", "DATA-SOURCES.txt"} {
		if regularConfigFile(filepath.Join(previous, name)) {
			if err := copyNetworkFile(filepath.Join(previous, name), filepath.Join(directory, name)); err != nil {
				return "", errors.New("保留独立数据源失败。")
			}
		}
	}
	if err := writePrivateJSON(filepath.Join(directory, "manifest.json"), map[string]any{"version": version, "created_at": time.Now().UTC().Format(time.RFC3339), "files": files}); err != nil {
		return "", errors.New("版本清单写入失败。")
	}
	if err := n.activateIntelligenceGeneration(root, directory, version); err != nil {
		return "", err
	}
	activated = true
	return version, nil
}
func (s *dashboardService) runIPMaintenanceScheduler(ctx context.Context) {
	n := s.network
	n.loadMaintenance()
	// Populate the persistent IP intelligence directory on first boot instead
	// of waiting for the periodic five-minute tick. The web service remains
	// available while datasets download; status and failures are visible in the
	// IP maintenance panel.
	initial := n.maintenanceStatus()
	if initial.Settings.AutoUpdate && initial.LastSuccess == 0 {
		_, _ = n.queueIntelligenceUpdate()
	}
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-n.maintenance.wake:
			n.maintenance.mu.Lock()
			if n.maintenance.state.State != "queued" {
				n.maintenance.mu.Unlock()
				continue
			}
			settings := n.maintenance.state.Settings
			n.maintenance.state.State = "running"
			_ = n.persistMaintenanceLocked()
			n.maintenance.mu.Unlock()
			taskCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
			version, err := n.downloadIntelligence(taskCtx, settings)
			cancel()
			n.maintenance.mu.Lock()
			n.maintenance.state.State, n.maintenance.state.Stage = "completed", ""
			if err != nil {
				n.maintenance.state.State, n.maintenance.state.LastError = "failed", err.Error()
			} else {
				n.maintenance.state.LastSuccess, n.maintenance.state.Version = n.clock().Unix(), version
				n.maintenance.state.LastError = ""
			}
			_ = n.persistMaintenanceLocked()
			n.maintenance.mu.Unlock()
		case <-ticker.C:
			state := n.maintenanceStatus()
			if state.Settings.AutoUpdate && n.clock().Unix() >= state.NextUpdateAt && n.clock().Unix()-state.UpdatedAt >= 3600 {
				_, _ = n.queueIntelligenceUpdate()
			}
		}
	}
}
func (s *dashboardService) importIPDatabaseHTTP(c *gin.Context) {
	kind := c.Query("kind")
	name, expected := "", ""
	switch kind {
	case "isp":
		name, expected = "GeoIP2-ISP.mmdb", "ISP"
	case "anonymous":
		name, expected = "GeoIP2-Anonymous-IP.mmdb", "Anonymous"
	default:
		c.JSON(400, gin.H{"success": false, "error": "仅支持 ISP 或 Anonymous-IP MMDB 数据源。"})
		return
	}
	n := s.network
	n.loadMaintenance()
	n.maintenance.mu.Lock()
	if state := n.maintenance.state.State; state == "running" || state == "queued" || state == "importing" {
		n.maintenance.mu.Unlock()
		c.JSON(409, gin.H{"success": false, "error": "数据更新进行中，请完成后再导入。"})
		return
	}
	n.maintenance.state.State = "importing"
	n.maintenance.mu.Unlock()
	imported := false
	importVersion := ""
	defer func() {
		n.maintenance.mu.Lock()
		if imported {
			n.maintenance.state.State, n.maintenance.state.LastError, n.maintenance.state.Stage = "completed", "", ""
			n.maintenance.state.Version = importVersion
		} else {
			n.maintenance.state.State, n.maintenance.state.LastError = "failed", "独立数据源导入失败，当前数据未改变。"
		}
		_ = n.persistMaintenanceLocked()
		n.maintenance.mu.Unlock()
	}()
	// Stream straight into this generation, rather than spooling large MMDBs
	// through the container's small /tmp filesystem.
	controller := http.NewResponseController(c.Writer)
	_ = controller.SetReadDeadline(time.Now().Add(3 * time.Minute))
	_ = controller.SetWriteDeadline(time.Now().Add(4 * time.Minute))
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 192<<20)
	multipartReader, multipartErr := c.Request.MultipartReader()
	if multipartErr != nil {
		c.JSON(400, gin.H{"success": false, "error": "上传文件超出大小限制或格式无效。"})
		return
	}
	var input io.ReadCloser
	for {
		part, err := multipartReader.NextPart()
		if err != nil {
			break
		}
		if part.FormName() == "file" && part.FileName() != "" {
			input = part
			break
		}
		_ = part.Close()
	}
	var err error
	if input == nil {
		c.JSON(400, gin.H{"success": false, "error": "请选择 MMDB 文件。"})
		return
	}
	defer input.Close()
	root := n.intelligenceRoot()
	version := time.Now().UTC().Format("20060102T150405.000000000Z") + "-import"
	directory := filepath.Join(root, "versions", version)
	if os.MkdirAll(directory, 0700) != nil {
		c.JSON(503, gin.H{"success": false, "error": "数据目录不可写。"})
		return
	}
	defer func() {
		if !imported {
			discardNetworkGeneration(root, directory)
		}
	}()
	path := filepath.Join(directory, name)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err == nil {
		var written int64
		written, err = io.Copy(file, io.LimitReader(input, (192<<20)+1))
		if written > 192<<20 {
			err = errors.New("database exceeds upload size limit")
		}
		err = errors.Join(err, file.Close())
	}
	if err == nil {
		reader, openErr := maxminddb.Open(path)
		if openErr != nil {
			err = openErr
		} else {
			if !strings.Contains(reader.Metadata.DatabaseType, expected) {
				err = errors.New("wrong mmdb type")
			}
			_ = reader.Close()
		}
	}
	if err != nil {
		c.JSON(400, gin.H{"success": false, "error": "MMDB 无效或类型不匹配，当前数据未改变。"})
		return
	}
	current, err := resolveIPIntelligenceDirectory(root)
	if err == nil {
		for _, candidate := range networkGenerationFiles {
			if candidate != name && regularConfigFile(filepath.Join(current, candidate)) {
				if err = copyNetworkFile(filepath.Join(current, candidate), filepath.Join(directory, candidate)); err != nil {
					break
				}
			}
		}
	}
	if err == nil {
		err = writePrivateJSON(filepath.Join(directory, "manifest.json"), map[string]any{"version": version, "imported_kind": kind})
	}
	if err == nil {
		err = n.activateIntelligenceGeneration(root, directory, version)
	}
	if err != nil {
		c.JSON(503, gin.H{"success": false, "error": "导入未完成，请检查数据目录权限。"})
		return
	}
	imported, importVersion = true, version
	c.JSON(200, gin.H{"success": true, "data": n.ipIntelligenceStatus()})
}
