package main

import (
	"strings"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	defaults, err := loadConfig(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if defaults.Port != 18080 || defaults.Mode != "demo" || defaults.Timezone != "Asia/Shanghai" ||
		defaults.QuotaPerUnit != 500000 || defaults.StaticDir != "../frontend/dist" {
		t.Fatalf("wrong defaults: %+v", defaults)
	}
	tests := []struct {
		name string
		env  map[string]string
		err  string
	}{
		{"live needs secret", map[string]string{"APP_MODE": "live"}, "ADMIN_TOKEN"},
		{"live needs database", map[string]string{"APP_MODE": "live", "ADMIN_TOKEN": "secret"}, "MAIN_DB_DSN"},
		{"live is configurable", map[string]string{
			"APP_MODE": " LIVE ", "ADMIN_TOKEN": "secret", "MAIN_DB_DRIVER": "Postgres",
			"MAIN_DB_DSN": "postgres://localhost/newapi", "LISTEN_HOST": "127.0.0.1",
			"LOG_DB_DRIVER": "mysql", "LOG_DB_DSN": "tcp(127.0.0.1)/logs",
		}, ""},
		{"incomplete log database", map[string]string{"LOG_DB_DRIVER": "mysql"}, "configured together"},
		{"unsupported log driver", map[string]string{"LOG_DB_DRIVER": "sqlite", "LOG_DB_DSN": "file.db"}, "LOG_DB_DRIVER"},
		{"invalid quota", map[string]string{"QUOTA_PER_UNIT": "NaN"}, "QUOTA_PER_UNIT"},
		{"invalid port", map[string]string{"PORT": "65536"}, "PORT"},
		{"timezone special value", map[string]string{"TIMEZONE": "Local"}, "TIMEZONE"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadConfig(func(key string) string { return tc.env[key] })
			if tc.err == "" && err != nil || tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)) {
				t.Fatalf("expected error fragment %q, got %v", tc.err, err)
			}
		})
	}
}

func TestDatabaseOpenErrorsDoNotExposeDSN(t *testing.T) {
	const secret = "this-must-not-be-printed"
	const invalidDSN = "postgres://root:" + secret + "@[malformed"
	config := Config{Mode: "live", MainDriver: "postgres", MainDSN: invalidDSN}
	if _, err := newDashboardService(config); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("unexpected main database error: %v", err)
	}
	config.MainDSN = "postgres://readonly@localhost/newapi"
	config.LogsDriver, config.LogsDSN = "postgres", invalidDSN
	if _, err := newDashboardService(config); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("unexpected log database error: %v", err)
	}
}
