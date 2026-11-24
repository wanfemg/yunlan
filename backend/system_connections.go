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
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

var connectionKeys = []string{
	"APP_MODE", "MAIN_DB_DRIVER", "MAIN_DB_DSN", "LOG_DB_DRIVER", "LOG_DB_DSN",
	"MANAGEMENT_DB_DRIVER", "MANAGEMENT_DB_DSN", "NEWAPI_UPSTREAM",
	"NEWAPI_ADMIN_TOKEN", "NEWAPI_ADMIN_USER_ID", "NETWORK_HISTORY_COMPLETE",
	"GEOIP_ISP_PATH", "GEOIP_ANONYMOUS_PATH",
}

var connectionSecrets = map[string]bool{
	"MAIN_DB_DSN": true, "LOG_DB_DSN": true, "MANAGEMENT_DB_DSN": true, "NEWAPI_ADMIN_TOKEN": true,
}

type savedConnections struct {
	Values  map[string]string `json:"values"`
	SavedAt int64             `json:"saved_at"`
}

type connectionCheck struct {
	ID          string `json:"id"`
	State       string `json:"state"`
	Message     string `json:"message"`
	CheckedAt   int64  `json:"checked_at"`
	Fingerprint string `json:"-"`
}

type savedConnectionCheck struct {
	connectionCheck
	Fingerprint string `json:"fingerprint"`
}

type connectionSettings struct {
	mu sync.Mutex
}

func readConnections(path string) (savedConnections, error) {
	value := savedConnections{Values: map[string]string{}}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return value, nil
	}
	if err != nil {
		return value, errors.New("saved connections cannot be read")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() > 48<<10 {
		return value, errors.New("saved connections exceed their size limit")
	}
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || value.Values == nil {
		return value, errors.New("saved connections are invalid")
	}
	allowed := map[string]bool{}
	for _, key := range connectionKeys {
		allowed[key] = true
	}
	for key, item := range value.Values {
		if !allowed[key] || len(item) > 8192 || strings.ContainsRune(item, 0) {
			return value, errors.New("saved connections contain an unsupported setting")
		}
	}
	return value, nil
}

func cloneConnectionValues(values map[string]string) map[string]string {
	copy := make(map[string]string, len(values))
	for key, value := range values {
		copy[key] = value
	}
	return copy
}

func loadSavedConnectionConfig(getenv func(string) string) (Config, error) {
	dataDir := strings.TrimSpace(getenv("DATA_DIR"))
	if dataDir == "" {
		dataDir = "./data"
	}
	settingsPath := strings.TrimSpace(getenv("SITEADMIN_CONFIG_FILE"))
	if settingsPath == "" {
		settingsPath = filepath.Join(dataDir, "connections.json")
	}
	ignored := strings.EqualFold(strings.TrimSpace(getenv("IGNORE_SAVED_CONNECTIONS")), "true")
	saved := savedConnections{Values: map[string]string{}}
	var err error
	if !ignored {
		saved, err = readConnections(settingsPath)
	}
	if err != nil {
		return Config{}, err
	}
	base := map[string]string{}
	for _, key := range connectionKeys {
		base[key] = getenv(key)
	}
	config, err := loadConfig(func(key string) string {
		if value, found := saved.Values[key]; found {
			return value
		}
		return getenv(key)
	})
	if err == nil {
		config.connectionBase = base
		config.connectionLoaded = cloneConnectionValues(saved.Values)
		config.connectionIgnored = ignored
	}
	return config, err
}

// Write in the same directory, flush, then replace the complete document. Never
// partially overwrite a settings file containing connection credentials.
func writePrivateJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".siteadmin-*.tmp")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err = file.Chmod(0600); err == nil {
		encoder := json.NewEncoder(file)
		encoder.SetIndent("", "  ")
		err = encoder.Encode(value)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, path)
}

func (s *dashboardService) connectionCandidate(saved savedConnections) (Config, error) {
	return loadConfig(func(key string) string {
		if value, found := saved.Values[key]; found {
			return value
		}
		if value, found := s.config.connectionBase[key]; found {
			return value
		}
		return os.Getenv(key)
	})
}

type connectionDraft struct {
	Values       map[string]string `json:"values"`
	ClearSecrets []string          `json:"clear_secrets"`
}

func (s *dashboardService) mergeConnectionDraft(draft connectionDraft) (savedConnections, Config, error) {
	saved, err := readConnections(s.config.ConnectionSettingsFile)
	if err != nil {
		return saved, Config{}, err
	}
	allowed := map[string]bool{}
	for _, key := range connectionKeys {
		allowed[key] = true
	}
	for key, value := range draft.Values {
		if !allowed[key] || len(value) > 8192 || strings.ContainsRune(value, 0) ||
			(connectionSecrets[key] && strings.ContainsAny(value, "\r\n")) {
			return saved, Config{}, errors.New("连接参数无效或包含不支持的配置项。")
		}
		value = strings.TrimSpace(value)
		if connectionSecrets[key] && value == "" {
			continue // An empty password field retains the existing secret.
		}
		saved.Values[key] = value
	}
	for _, key := range draft.ClearSecrets {
		if !connectionSecrets[key] {
			return saved, Config{}, errors.New("不支持清除此配置项。")
		}
		saved.Values[key] = ""
	}
	candidate, err := s.connectionCandidate(saved)
	if err != nil {
		// loadConfig emits field names only, never a raw DSN or credential.
		return saved, Config{}, err
	}
	previous, previousErr := readConnections(s.config.ConnectionSettingsFile)
	if previousErr != nil {
		return saved, Config{}, previousErr
	}
	old, oldErr := s.connectionCandidate(previous)
	if oldErr == nil {
		for _, database := range []struct {
			prefix, previousDriver, nextDriver, dsn string
		}{
			{"MAIN_DB", old.MainDriver, candidate.MainDriver, candidate.MainDSN},
			{"LOG_DB", old.LogsDriver, candidate.LogsDriver, candidate.LogsDSN},
			{"MANAGEMENT_DB", old.ManagementDriver, candidate.ManagementDriver, candidate.ManagementDSN},
		} {
			// A blank secret retains the existing DSN. It cannot safely be
			// reinterpreted under a newly selected database driver.
			if database.previousDriver != database.nextDriver && database.dsn != "" &&
				strings.TrimSpace(draft.Values[database.prefix+"_DSN"]) == "" {
				return saved, Config{}, fmt.Errorf("%s 数据库类型已变化，请填写对应的新连接字符串；原连接不会用于新类型。", database.prefix)
			}
		}
	}
	// Parse connection strings before saving, without establishing any network
	// connection. A syntactically invalid DSN must not break the next restart.
	for _, database := range []struct {
		prefix, driver, dsn string
	}{
		{"MAIN_DB", candidate.MainDriver, candidate.MainDSN},
		{"LOG_DB", candidate.LogsDriver, candidate.LogsDSN},
		{"MANAGEMENT_DB", candidate.ManagementDriver, candidate.ManagementDSN},
	} {
		if database.dsn == "" {
			continue
		}
		parsed, err := openDatabase(database.driver, database.dsn)
		if err != nil {
			return saved, Config{}, fmt.Errorf("%s 连接字符串与数据库类型不匹配或格式无效。", database.prefix)
		}
		_ = parsed.Close()
	}
	if oldErr == nil && candidate.NewAPIAdminToken != "" &&
		fmt.Sprint(candidate.NewAPIUpstream) != fmt.Sprint(old.NewAPIUpstream) &&
		strings.TrimSpace(draft.Values["NEWAPI_ADMIN_TOKEN"]) == "" {
		return saved, Config{}, errors.New("NewAPI 地址已变化，请填写新地址对应的管理员令牌。")
	}
	return saved, candidate, nil
}

func connectionFingerprint(config Config) string {
	value, _ := json.Marshal([]any{config.Mode, config.MainDriver, config.MainDSN,
		config.LogsDriver, config.LogsDSN, config.ManagementDriver, config.ManagementDSN,
		config.NewAPIUpstream, config.NewAPIAdminToken, config.NewAPIAdminUserID})
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func registerSystemConnectionRoutes(router *gin.Engine, service *dashboardService, token string) {
	group := router.Group("/api/system", requireAdminToken(token))
	group.GET("/connections", service.systemConnectionsHTTP)
	group.PUT("/connections", service.saveSystemConnectionsHTTP)
	group.POST("/connections/check", service.checkSystemConnectionsHTTP)
	group.GET("/readiness", service.systemReadinessHTTP)
}

func (s *dashboardService) connectionChecks(config Config) []connectionCheck {
	checks := []savedConnectionCheck{}
	file, err := os.Open(filepath.Join(s.config.DataDir, "connection-checks.json"))
	if err == nil {
		_ = json.NewDecoder(io.LimitReader(file, 32<<10)).Decode(&checks)
		_ = file.Close()
	}
	fingerprint := connectionFingerprint(config)
	output := []connectionCheck{}
	for _, item := range checks {
		if item.Fingerprint == fingerprint {
			output = append(output, item.connectionCheck)
		}
	}
	return output
}

func (s *dashboardService) connectionView(saved savedConnections, config Config) gin.H {
	values := map[string]string{
		"APP_MODE": config.Mode, "MAIN_DB_DRIVER": config.MainDriver, "LOG_DB_DRIVER": config.LogsDriver,
		"MANAGEMENT_DB_DRIVER": config.ManagementDriver, "NEWAPI_ADMIN_USER_ID": "",
		"NETWORK_HISTORY_COMPLETE": strconv.FormatBool(config.NetworkHistoryComplete),
		"GEOIP_ISP_PATH":           config.GeoISPPath, "GEOIP_ANONYMOUS_PATH": config.GeoAnonymousPath,
	}
	if config.NewAPIUpstream != nil {
		values["NEWAPI_UPSTREAM"] = config.NewAPIUpstream.String()
	}
	if config.NewAPIAdminUserID > 0 {
		values["NEWAPI_ADMIN_USER_ID"] = strconv.FormatInt(config.NewAPIAdminUserID, 10)
	}
	secrets := map[string]bool{
		"MAIN_DB_DSN": config.MainDSN != "", "LOG_DB_DSN": config.LogsDSN != "",
		"MANAGEMENT_DB_DSN": config.ManagementDSN != "", "NEWAPI_ADMIN_TOKEN": config.NewAPIAdminToken != "",
	}
	return gin.H{
		"values": values, "secrets": secrets, "saved_at": saved.SavedAt,
		"restart_required": !reflect.DeepEqual(saved.Values, s.config.connectionLoaded) && len(saved.Values) > 0,
		"active_mode":      s.config.Mode, "checks": s.connectionChecks(config),
		"preview_isolated": s.config.connectionIgnored,
		"integrations": gin.H{
			"probe_configured":   s.config.ProbeSharedSecret != "",
			"finance_configured": s.config.FinanceUpstream != nil,
			"newapi_configured":  config.NewAPIUpstream != nil && config.NewAPIAdminToken != "" && config.NewAPIAdminUserID > 0,
		},
	}
}

func (s *dashboardService) systemConnectionsHTTP(c *gin.Context) {
	s.connections.mu.Lock()
	defer s.connections.mu.Unlock()
	saved, err := readConnections(s.config.ConnectionSettingsFile)
	if err != nil {
		c.JSON(503, gin.H{"success": false, "error": "连接配置读取失败。"})
		return
	}
	config, err := s.connectionCandidate(saved)
	if err != nil {
		c.JSON(400, gin.H{"success": false, "error": err.Error()})
		return
	}
	c.JSON(200, gin.H{"success": true, "data": s.connectionView(saved, config)})
}

func (s *dashboardService) saveSystemConnectionsHTTP(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 48<<10)
	var draft connectionDraft
	if c.ShouldBindJSON(&draft) != nil {
		c.JSON(400, gin.H{"success": false, "error": "连接配置格式无效。"})
		return
	}
	s.connections.mu.Lock()
	defer s.connections.mu.Unlock()
	saved, config, err := s.mergeConnectionDraft(draft)
	if err != nil {
		c.JSON(400, gin.H{"success": false, "error": err.Error()})
		return
	}
	saved.SavedAt = time.Now().Unix()
	if writePrivateJSON(s.config.ConnectionSettingsFile, saved) != nil {
		c.JSON(503, gin.H{"success": false, "error": "连接配置保存失败，请检查数据目录权限。"})
		return
	}
	c.JSON(200, gin.H{"success": true, "data": s.connectionView(saved, config)})
}

func checkDatabaseConnection(parent context.Context, id, driver, dsn, table string) connectionCheck {
	result := connectionCheck{ID: id, CheckedAt: time.Now().Unix(), State: "missing", Message: "尚未填写连接。"}
	if dsn == "" || !validDriver(driver) {
		return result
	}
	result.State, result.Message = "failed", "数据库连接失败，请检查地址、凭证、网络与访问权限。"
	db, err := openDatabase(driver, dsn)
	if err != nil {
		return result
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	if db.PingContext(ctx) != nil {
		return result
	}
	if table != "" {
		rows, err := db.QueryContext(ctx, "SELECT * FROM "+table+" WHERE 1=0")
		if err != nil {
			result.Message = "连接成功，但无法读取所需表 " + table + "。"
			return result
		}
		_ = rows.Close()
	}
	result.State, result.Message = "connected", "数据库已连接，所需数据表可读。"
	if id == "management" {
		result.Message = "管理库已连接；创建、修改与删除权限由具体管理操作验证。"
	}
	return result
}

func checkNewAPIConnection(parent context.Context, config Config) connectionCheck {
	result := connectionCheck{ID: "newapi", CheckedAt: time.Now().Unix(), State: "missing", Message: "请填写 NewAPI 地址、管理员 ID 和令牌。"}
	if config.NewAPIUpstream == nil || config.NewAPIAdminToken == "" || config.NewAPIAdminUserID <= 0 {
		return result
	}
	result.State, result.Message = "failed", "NewAPI 管理连接未通过，请核对地址、管理员 ID、令牌及访问限制。"
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	target := *config.NewAPIUpstream
	target.Path = "/api/user/self"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return result
	}
	request.Header.Set("Authorization", "Bearer "+config.NewAPIAdminToken)
	request.Header.Set("New-Api-User", strconv.FormatInt(config.NewAPIAdminUserID, 10))
	client := &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return result
	}
	defer response.Body.Close()
	var payload struct {
		Success bool `json:"success"`
		Data    struct {
			ID   int64 `json:"id"`
			Role int   `json:"role"`
		} `json:"data"`
	}
	if response.StatusCode == 200 && json.NewDecoder(io.LimitReader(response.Body, 128<<10)).Decode(&payload) == nil &&
		payload.Success && payload.Data.ID == config.NewAPIAdminUserID && payload.Data.Role >= 10 {
		result.State, result.Message = "connected", "NewAPI 管理员身份已验证，管理桥接可用。"
	}
	return result
}

func (s *dashboardService) checkSystemConnectionsHTTP(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 48<<10)
	var draft connectionDraft
	if c.ShouldBindJSON(&draft) != nil {
		c.JSON(400, gin.H{"success": false, "error": "连接配置格式无效。"})
		return
	}
	s.connections.mu.Lock()
	_, config, err := s.mergeConnectionDraft(draft)
	s.connections.mu.Unlock()
	if err != nil {
		c.JSON(400, gin.H{"success": false, "error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 12*time.Second)
	defer cancel()
	logDriver, logDSN := config.LogsDriver, config.LogsDSN
	if logDSN == "" {
		logDriver, logDSN = config.MainDriver, config.MainDSN
	}
	managementDriver, managementDSN := config.ManagementDriver, config.ManagementDSN
	if managementDSN == "" {
		managementDriver, managementDSN = config.MainDriver, config.MainDSN
	}
	tasks := []func() connectionCheck{
		func() connectionCheck {
			return checkDatabaseConnection(ctx, "main", config.MainDriver, config.MainDSN, "users")
		},
		func() connectionCheck { return checkDatabaseConnection(ctx, "logs", logDriver, logDSN, "logs") },
		func() connectionCheck {
			return checkDatabaseConnection(ctx, "management", managementDriver, managementDSN, "users")
		},
		func() connectionCheck { return checkNewAPIConnection(ctx, config) },
	}
	results := make(chan connectionCheck, len(tasks))
	for _, task := range tasks {
		go func() { results <- task() }()
	}
	checks := make([]connectionCheck, 0, len(tasks))
	saved := make([]savedConnectionCheck, 0, len(tasks))
	fingerprint := connectionFingerprint(config)
	for range tasks {
		check := <-results
		checks = append(checks, check)
		saved = append(saved, savedConnectionCheck{connectionCheck: check, Fingerprint: fingerprint})
	}
	sort.Slice(checks, func(i, j int) bool { return checks[i].ID < checks[j].ID })
	s.connections.mu.Lock()
	writeErr := writePrivateJSON(filepath.Join(s.config.DataDir, "connection-checks.json"), saved)
	s.connections.mu.Unlock()
	if writeErr != nil {
		c.JSON(503, gin.H{"success": false, "error": "诊断已完成，但结果保存失败。"})
		return
	}
	c.JSON(200, gin.H{"success": true, "data": gin.H{"checks": checks, "read_only": true}})
}
