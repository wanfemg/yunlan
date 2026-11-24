package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
)

type Config struct {
	Port                   int
	ListenHost             string
	Mode                   string
	AdminToken             string
	MainDriver             string
	MainDSN                string
	LogsDriver             string
	LogsDSN                string
	ManagementDriver       string
	ManagementDSN          string
	DataDir                string
	AutoGroupStatePath     string
	Timezone               string
	QuotaPerUnit           float64
	StaticDir              string
	Location               *time.Location
	FinanceUpstream        *url.URL
	FinanceProxySecret     string
	NewAPIUpstream         *url.URL
	NewAPIAdminToken       string
	NewAPIAdminUserID      int64
	ProbeSharedSecret      string
	GeoCityPath            string
	GeoASNPath             string
	GeoISPPath             string
	GeoAnonymousPath       string
	IPIntelligenceDir      string
	IPIntelligenceRoot     string
	NetworkHistoryComplete bool
	connectionBase         map[string]string
	connectionLoaded       map[string]string
	ConnectionSettingsFile string
	connectionIgnored      bool
}

func LoadConfig() (Config, error) {
	return loadSavedConnectionConfig(os.Getenv)
}

func loadConfig(getenv func(string) string) (Config, error) {
	config := Config{
		Port:              18080,
		ListenHost:        strings.TrimSpace(getenv("LISTEN_HOST")),
		Mode:              strings.ToLower(strings.TrimSpace(getenv("APP_MODE"))),
		AdminToken:        strings.TrimSpace(getenv("ADMIN_TOKEN")),
		MainDriver:        strings.ToLower(strings.TrimSpace(getenv("MAIN_DB_DRIVER"))),
		MainDSN:           strings.TrimSpace(getenv("MAIN_DB_DSN")),
		LogsDriver:        strings.ToLower(strings.TrimSpace(getenv("LOG_DB_DRIVER"))),
		LogsDSN:           strings.TrimSpace(getenv("LOG_DB_DSN")),
		ManagementDriver:  strings.ToLower(strings.TrimSpace(getenv("MANAGEMENT_DB_DRIVER"))),
		ManagementDSN:     strings.TrimSpace(getenv("MANAGEMENT_DB_DSN")),
		Timezone:          strings.TrimSpace(getenv("TIMEZONE")),
		StaticDir:         strings.TrimSpace(getenv("STATIC_DIR")),
		QuotaPerUnit:      500000,
		NewAPIAdminToken:  strings.TrimSpace(getenv("NEWAPI_ADMIN_TOKEN")),
		ProbeSharedSecret: strings.TrimSpace(getenv("PROBE_SHARED_SECRET")),
		GeoCityPath:       strings.TrimSpace(getenv("GEOIP_CITY_PATH")),
		GeoASNPath:        strings.TrimSpace(getenv("GEOIP_ASN_PATH")),
		GeoISPPath:        strings.TrimSpace(getenv("GEOIP_ISP_PATH")),
		GeoAnonymousPath:  strings.TrimSpace(getenv("GEOIP_ANONYMOUS_PATH")),
		IPIntelligenceDir: strings.TrimSpace(getenv("IP_INTELLIGENCE_DIR")),
	}
	if config.Mode == "" {
		config.Mode = "demo"
	}
	if config.Mode != "demo" && config.Mode != "live" {
		return Config{}, fmt.Errorf("APP_MODE must be demo or live")
	}
	if rawPort := strings.TrimSpace(getenv("PORT")); rawPort != "" {
		port, err := strconv.Atoi(rawPort)
		if err != nil || port < 1 || port > 65535 {
			return Config{}, fmt.Errorf("PORT must be between 1 and 65535")
		}
		config.Port = port
	}
	if config.Timezone == "" {
		config.Timezone = "Asia/Shanghai"
	}
	config.DataDir = strings.TrimSpace(getenv("DATA_DIR"))
	if config.DataDir == "" {
		config.DataDir = "./data"
	}
	config.ConnectionSettingsFile = strings.TrimSpace(getenv("SITEADMIN_CONFIG_FILE"))
	if config.ConnectionSettingsFile == "" {
		config.ConnectionSettingsFile = filepath.Join(config.DataDir, "connections.json")
	}
	if config.IPIntelligenceDir == "" {
		config.IPIntelligenceDir = filepath.Join(config.DataDir, "ip-intelligence")
	}
	config.IPIntelligenceRoot = config.IPIntelligenceDir
	intelligenceDir, intelligenceErr := resolveIPIntelligenceDirectory(config.IPIntelligenceDir)
	if intelligenceErr != nil {
		return Config{}, intelligenceErr
	}
	config.IPIntelligenceDir = intelligenceDir
	if config.GeoCityPath == "" {
		if candidate := filepath.Join(config.IPIntelligenceDir, "GeoLite2-City.mmdb"); regularConfigFile(candidate) {
			config.GeoCityPath = candidate
		}
	}
	if config.GeoASNPath == "" {
		if candidate := filepath.Join(config.IPIntelligenceDir, "GeoLite2-ASN.mmdb"); regularConfigFile(candidate) {
			config.GeoASNPath = candidate
		}
	}
	for _, extra := range []struct {
		name string
		path *string
	}{
		{"GeoIP2-ISP.mmdb", &config.GeoISPPath},
		{"GeoIP2-Anonymous-IP.mmdb", &config.GeoAnonymousPath},
	} {
		if *extra.path == "" {
			if candidate := filepath.Join(config.IPIntelligenceDir, extra.name); regularConfigFile(candidate) {
				*extra.path = candidate
			}
		}
	}
	config.AutoGroupStatePath = filepath.Join(config.DataDir, "auto-group-state.json")
	location, err := time.LoadLocation(config.Timezone)
	if err != nil || config.Timezone == "Local" {
		return Config{}, fmt.Errorf("TIMEZONE must be a valid IANA time zone")
	}
	config.Location = location
	if raw := strings.TrimSpace(getenv("NEWAPI_UPSTREAM")); raw != "" {
		upstream, parseErr := url.Parse(raw)
		if parseErr != nil || (upstream.Scheme != "http" && upstream.Scheme != "https") ||
			upstream.Host == "" || upstream.User != nil || upstream.RawQuery != "" || upstream.Fragment != "" ||
			(upstream.Path != "" && upstream.Path != "/") {
			return Config{}, fmt.Errorf("NEWAPI_UPSTREAM must be an HTTP(S) origin without credentials, query or path")
		}
		upstream.Path = ""
		config.NewAPIUpstream = upstream
	}
	if raw := strings.TrimSpace(getenv("NEWAPI_ADMIN_USER_ID")); raw != "" {
		id, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || id <= 0 {
			return Config{}, fmt.Errorf("NEWAPI_ADMIN_USER_ID must be positive")
		}
		config.NewAPIAdminUserID = id
	}
	if config.NewAPIAdminToken != "" && (config.NewAPIUpstream == nil || config.NewAPIAdminUserID == 0) {
		return Config{}, fmt.Errorf("NEWAPI_UPSTREAM and NEWAPI_ADMIN_USER_ID are required with NEWAPI_ADMIN_TOKEN")
	}
	if config.ProbeSharedSecret != "" && !validFinanceProxySecret(config.ProbeSharedSecret) {
		return Config{}, fmt.Errorf("PROBE_SHARED_SECRET must be a 32–128 character URL-safe machine key")
	}
	if raw := strings.TrimSpace(getenv("NETWORK_HISTORY_COMPLETE")); raw != "" {
		complete, parseErr := strconv.ParseBool(raw)
		if parseErr != nil {
			return Config{}, fmt.Errorf("NETWORK_HISTORY_COMPLETE must be true or false")
		}
		config.NetworkHistoryComplete = complete
	}
	config.FinanceUpstream, err = parseFinanceUpstream(getenv("FINANCE_UPSTREAM"))
	if err != nil {
		return Config{}, err
	}
	config.FinanceProxySecret = getenv("FINANCE_PROXY_SECRET")
	if config.FinanceUpstream != nil && !validFinanceProxySecret(config.FinanceProxySecret) {
		return Config{}, fmt.Errorf("FINANCE_PROXY_SECRET must be a 32–128 character URL-safe key when FINANCE_UPSTREAM is configured")
	}
	if config.FinanceUpstream != nil && config.AdminToken == "" {
		return Config{}, fmt.Errorf("ADMIN_TOKEN is required when FINANCE_UPSTREAM is configured")
	}
	if rawQuota := strings.TrimSpace(getenv("QUOTA_PER_UNIT")); rawQuota != "" {
		quota, err := strconv.ParseFloat(rawQuota, 64)
		if err != nil || quota <= 0 || math.IsNaN(quota) || math.IsInf(quota, 0) {
			return Config{}, fmt.Errorf("QUOTA_PER_UNIT must be a positive number")
		}
		config.QuotaPerUnit = quota
	}
	if config.StaticDir == "" {
		config.StaticDir = "../frontend/dist"
	}
	if config.Mode == "live" {
		if config.AdminToken == "" {
			return Config{}, fmt.Errorf("ADMIN_TOKEN is required in live mode")
		}
		if config.MainDSN == "" || !validDriver(config.MainDriver) {
			return Config{}, fmt.Errorf("MAIN_DB_DRIVER and MAIN_DB_DSN are required in live mode")
		}
	}
	if (config.LogsDSN == "") != (config.LogsDriver == "") {
		return Config{}, fmt.Errorf("LOG_DB_DRIVER and LOG_DB_DSN must be configured together")
	}
	if config.LogsDSN != "" && !validDriver(config.LogsDriver) {
		return Config{}, fmt.Errorf("LOG_DB_DRIVER must be postgres or mysql")
	}
	if (config.ManagementDSN == "") != (config.ManagementDriver == "") {
		return Config{}, fmt.Errorf("MANAGEMENT_DB_DRIVER and MANAGEMENT_DB_DSN must be configured together")
	}
	if config.ManagementDSN != "" && !validDriver(config.ManagementDriver) {
		return Config{}, fmt.Errorf("MANAGEMENT_DB_DRIVER must be postgres or mysql")
	}
	return config, nil
}

func validDriver(driver string) bool {
	return driver == "postgres" || driver == "mysql"
}

func regularConfigFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func resolveIPIntelligenceDirectory(directory string) (string, error) {
	file, err := os.Open(filepath.Join(directory, "active.json"))
	if os.IsNotExist(err) {
		return directory, nil
	}
	if err != nil {
		return "", fmt.Errorf("IP intelligence active generation is unreadable")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() > 4096 {
		return "", fmt.Errorf("IP intelligence active generation is invalid")
	}
	var active struct {
		Directory string `json:"directory"`
	}
	if json.NewDecoder(file).Decode(&active) != nil || active.Directory == "" || filepath.IsAbs(active.Directory) {
		return "", fmt.Errorf("IP intelligence active generation is invalid")
	}
	base, err := filepath.Abs(directory)
	if err != nil {
		return "", fmt.Errorf("IP intelligence directory is invalid")
	}
	resolved := filepath.Join(base, filepath.FromSlash(active.Directory))
	relative, err := filepath.Rel(base, resolved)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("IP intelligence active generation leaves its data directory")
	}
	state, err := os.Stat(resolved)
	if err != nil || !state.IsDir() {
		return "", fmt.Errorf("IP intelligence active generation is not installed")
	}
	return resolved, nil
}
