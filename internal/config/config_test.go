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
	if c.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want :8080", c.HTTPAddr)
	}
	if c.OTLPHTTPAddr != ":4318" {
		t.Errorf("OTLPHTTPAddr = %q, want :4318", c.OTLPHTTPAddr)
	}
	if c.OTLPGRPCAddr != ":4317" {
		t.Errorf("OTLPGRPCAddr = %q, want :4317", c.OTLPGRPCAddr)
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
		"AI_USAGE_HTTP_ADDR":      ":9000",
		"AI_USAGE_OTLP_GRPC_ADDR": "",
		"AI_USAGE_DATA_DIR":       filepath.Join(home, "data"),
		"AI_USAGE_DATABASE":       filepath.Join(home, "data", "custom.db"),
		"AI_USAGE_LOG_LEVEL":      "debug",
	})

	c, err := Load([]string{}, env, "linux", home)
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPAddr != ":9000" {
		t.Errorf("HTTPAddr = %q, want :9000", c.HTTPAddr)
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
		"AI_USAGE_HTTP_ADDR": ":9000",
		"AI_USAGE_DATA_DIR":  filepath.Join(home, "env-dir"),
	})

	c, err := Load([]string{"--http", ":9001", "--data-dir", filepath.Join(home, "flag-dir")}, env, "linux", home)
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPAddr != ":9001" {
		t.Errorf("HTTPAddr = %q, want :9001 (flag must win)", c.HTTPAddr)
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
	env := envOf(map[string]string{"AI_USAGE_OTLP_HTTP_ADDR": ":4318"})

	c, err := Load([]string{"--otlp-grpc="}, env, "linux", home)
	if err != nil {
		t.Fatal(err)
	}
	if c.OTLPGRPCAddr != "" {
		t.Errorf("OTLPGRPCAddr = %q, want empty (disabled)", c.OTLPGRPCAddr)
	}
	if c.OTLPHTTPAddr != ":4318" {
		t.Errorf("OTLPHTTPAddr = %q, want :4318 from env", c.OTLPHTTPAddr)
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
