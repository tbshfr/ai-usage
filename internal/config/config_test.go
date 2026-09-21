package config

import (
	"os"
	"path/filepath"
	"testing"
)

func noEnv(string) (string, bool) { return "", false }

func envOf(m map[string]string) envFunc {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func TestDefaults(t *testing.T) {
	home := t.TempDir()
	data := filepath.Join(home, ".local", "share", "ai-usage")

	c, err := Load([]string{}, noEnv, "linux", home)
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPAddr != "127.0.0.1:8080" {
		t.Errorf(`HTTPAddr = %q, want 127.0.0.1:8080 (loopback default)`, c.HTTPAddr)
	}
	if c.OTLPHTTPAddr != "" {
		t.Errorf("OTLPHTTPAddr = %q, want empty (disabled unless configured)", c.OTLPHTTPAddr)
	}
	if c.OTLPGRPCAddr != "" {
		t.Errorf("OTLPGRPCAddr = %q, want empty (disabled unless configured)", c.OTLPGRPCAddr)
	}
	if c.DataDir != data {
		t.Errorf("DataDir = %q, want %q", c.DataDir, data)
	}
	if c.DatabasePath != filepath.Join(data, "usage.db") {
		t.Errorf("DatabasePath = %q", c.DatabasePath)
	}
	if c.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want info", c.LogLevel)
	}
	if fi, err := os.Stat(data); err != nil || !fi.IsDir() {
		t.Errorf("data dir not created: %v", err)
	}
}

func TestEnvOverride(t *testing.T) {
	home := t.TempDir()
	env := envOf(map[string]string{
		"AI_USAGE_HTTP_ADDR":      "127.0.0.1:9000",
		"AI_USAGE_OTLP_GRPC_ADDR": "",
		"AI_USAGE_DATA_DIR":       filepath.Join(home, "data"),
		"AI_USAGE_DATABASE":       filepath.Join(home, "data", "custom.db"),
		"AI_USAGE_LOG_LEVEL":      "debug",
	})

	c, err := Load([]string{}, env, "linux", home)
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPAddr != "127.0.0.1:9000" {
		t.Errorf("HTTPAddr = %q, want 127.0.0.1:9000", c.HTTPAddr)
	}
	if c.OTLPGRPCAddr != "" {
		t.Errorf("OTLPGRPCAddr = %q, want empty (disabled)", c.OTLPGRPCAddr)
	}
	if c.DatabasePath != filepath.Join(home, "data", "custom.db") {
		t.Errorf("DatabasePath = %q", c.DatabasePath)
	}
	if c.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want debug", c.LogLevel)
	}
}

func TestFlagOverEnv(t *testing.T) {
	home := t.TempDir()
	env := envOf(map[string]string{
		"AI_USAGE_HTTP_ADDR": "127.0.0.1:9000",
		"AI_USAGE_DATA_DIR":  filepath.Join(home, "env-dir"),
	})

	c, err := Load([]string{"--http", "127.0.0.1:9001", "--data-dir", filepath.Join(home, "flag-dir")}, env, "linux", home)
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPAddr != "127.0.0.1:9001" {
		t.Errorf("HTTPAddr = %q, want 127.0.0.1:9001 (flag must win)", c.HTTPAddr)
	}
	if c.DataDir != filepath.Join(home, "flag-dir") {
		t.Errorf("DataDir = %q, want flag dir", c.DataDir)
	}
	if c.DatabasePath != filepath.Join(home, "flag-dir", "usage.db") {
		t.Errorf("DatabasePath = %q", c.DatabasePath)
	}
}

func TestDisabledListener(t *testing.T) {
	home := t.TempDir()
	env := envOf(map[string]string{"AI_USAGE_OTLP_HTTP_ADDR": "127.0.0.1:4318"})

	c, err := Load([]string{"--otlp-grpc="}, env, "linux", home)
	if err != nil {
		t.Fatal(err)
	}
	if c.OTLPGRPCAddr != "" {
		t.Errorf("OTLPGRPCAddr = %q, want empty (disabled)", c.OTLPGRPCAddr)
	}
	if c.OTLPHTTPAddr != "127.0.0.1:4318" {
		t.Errorf("OTLPHTTPAddr = %q, want 127.0.0.1:4318 from env", c.OTLPHTTPAddr)
	}
}

func TestListenerEnabledOnlyWhenConfigured(t *testing.T) {
	home := t.TempDir()

	c, err := Load([]string{}, noEnv, "linux", home)
	if err != nil {
		t.Fatal(err)
	}
	if c.OTLPHTTPAddr != "" || c.OTLPGRPCAddr != "" {
		t.Errorf("OTLP listeners = %q/%q, want empty (absent flag/env must not start them)", c.OTLPHTTPAddr, c.OTLPGRPCAddr)
	}

	c, err = Load([]string{"--otlp-http", "127.0.0.1:4318"}, noEnv, "linux", home)
	if err != nil {
		t.Fatal(err)
	}
	if c.OTLPHTTPAddr != "127.0.0.1:4318" {
		t.Errorf("OTLPHTTPAddr = %q, want 127.0.0.1:4318 (flag enables listener)", c.OTLPHTTPAddr)
	}
	if c.OTLPGRPCAddr != "" {
		t.Errorf("OTLPGRPCAddr = %q, want empty (flag absent)", c.OTLPGRPCAddr)
	}
}

func TestUserDataDirPerGOOS(t *testing.T) {
	home := "/home/u"
	cases := []struct {
		goos string
		env  map[string]string
		want string
	}{
		{"linux", nil, filepath.Join(home, ".local", "share")},
		{"linux", map[string]string{"XDG_DATA_HOME": "/xdg"}, "/xdg"},
		{"linux", map[string]string{"XDG_DATA_HOME": "relative"}, filepath.Join(home, ".local", "share")},
		{"darwin", nil, filepath.Join(home, "Library", "Application Support")},
		{"windows", map[string]string{"LOCALAPPDATA": `C:\Users\u\AppData\Local`}, `C:\Users\u\AppData\Local`},
		{"windows", nil, filepath.Join(home, "AppData", "Local")},
	}
	for _, tc := range cases {
		got := userDataDir(tc.goos, home, envOf(tc.env))
		if got != tc.want {
			t.Errorf("userDataDir(%s) = %q, want %q", tc.goos, got, tc.want)
		}
	}
}

func TestInvalidLogLevel(t *testing.T) {
	home := t.TempDir()
	if _, err := Load([]string{"--log-level", "verbose"}, noEnv, "linux", home); err == nil {
		t.Error("want error for invalid log level")
	}
}

func TestDashboardAuthResolution(t *testing.T) {
	home := t.TempDir()
	c, err := Load([]string{
		"--dashboard-user", "admin",
		"--dashboard-password", "s3cret",
		"--otlp-token", "tok",
	}, noEnv, "linux", home)
	if err != nil {
		t.Fatal(err)
	}
	if c.DashboardUser != "admin" || c.DashboardPassword != "s3cret" || c.OTLPToken != "tok" {
		t.Errorf("auth config = %q/%q/%q", c.DashboardUser, c.DashboardPassword, c.OTLPToken)
	}

	env := envOf(map[string]string{
		"AI_USAGE_DASHBOARD_USER":     "envadmin",
		"AI_USAGE_DASHBOARD_PASSWORD": "envpass",
		"AI_USAGE_OTLP_TOKEN":         "envtok",
	})
	c, err = Load([]string{}, env, "linux", home)
	if err != nil {
		t.Fatal(err)
	}
	if c.DashboardUser != "envadmin" || c.DashboardPassword != "envpass" || c.OTLPToken != "envtok" {
		t.Errorf("auth config from env = %q/%q/%q", c.DashboardUser, c.DashboardPassword, c.OTLPToken)
	}

	c, err = Load([]string{"--dashboard-user", "flaguser"}, env, "linux", home)
	if err != nil {
		t.Fatal(err)
	}
	if c.DashboardUser != "flaguser" || c.DashboardPassword != "envpass" {
		t.Errorf("flag must override env per field: got %q/%q", c.DashboardUser, c.DashboardPassword)
	}
}

func TestDashboardUserWithoutPassword(t *testing.T) {
	home := t.TempDir()
	if _, err := Load([]string{"--dashboard-user", "admin"}, noEnv, "linux", home); err == nil {
		t.Error("user without password must error")
	}
	if _, err := Load([]string{"--dashboard-password", "s3cret"}, noEnv, "linux", home); err == nil {
		t.Error("password without user must error")
	}
}

func TestRefusesNonLoopbackWithoutAuth(t *testing.T) {
	home := t.TempDir()
	cases := []struct {
		name string
		args []string
	}{
		{"dashboard wildcard", []string{"--http", ":8080"}},
		{"dashboard all interfaces", []string{"--http", "0.0.0.0:8080"}},
		{"dashboard ipv6 wildcard", []string{"--http", "[::]:8080"}},
		{"dashboard public ip", []string{"--http", "10.0.0.5:8080"}},
		{"dashboard hostname", []string{"--http", "dash.example.com:8080"}},
		{"otlp http wildcard", []string{"--otlp-http", ":4318"}},
		{"otlp grpc wildcard", []string{"--otlp-grpc", ":4317"}},
		{"dashboard creds but no otlp token", []string{"--http", ":8080", "--dashboard-user", "u", "--dashboard-password", "p", "--otlp-http", ":4318"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Load(tc.args, noEnv, "linux", home); err == nil {
				t.Error("want error for non-loopback bind without credentials")
			}
		})
	}
}

func TestLoopbackAndAuthenticatedBindsPass(t *testing.T) {
	home := t.TempDir()
	cases := []struct {
		name string
		args []string
	}{
		{"dashboard loopback defaults", []string{}},
		{"dashboard localhost", []string{"--http", "localhost:8080"}},
		{"dashboard ipv6 loopback", []string{"--http", "[::1]:8080"}},
		{"otlp loopback", []string{"--otlp-http", "127.0.0.1:4318", "--otlp-grpc", "localhost:4317"}},
		{"wildcard with auth", []string{"--http", ":8080", "--otlp-http", ":4318", "--dashboard-user", "u", "--dashboard-password", "p", "--otlp-token", "t"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Load(tc.args, noEnv, "linux", home); err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestInvalidListenAddress(t *testing.T) {
	home := t.TempDir()
	if _, err := Load([]string{"--http", "no-port"}, noEnv, "linux", home); err == nil {
		t.Error("want error for address without port")
	}
}

func TestManualPricingFileFlagAndEnv(t *testing.T) {
	env := envOf(map[string]string{"AI_USAGE_PRICING_FILE": "env-prices.json"})
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "env-prices.json"},
		{[]string{"--pricing-file", "flag-prices.json"}, "flag-prices.json"},
		{[]string{"--pricing-file", ""}, ""},
	} {
		cfg, err := Load(tc.args, env, "linux", t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if cfg.PricingFile != tc.want {
			t.Fatalf("pricing file %q want %q", cfg.PricingFile, tc.want)
		}
	}
}
