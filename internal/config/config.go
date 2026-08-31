package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

type Config struct {
	HTTPAddr     string
	OTLPHTTPAddr string
	OTLPGRPCAddr string
	DataDir      string
	DatabasePath string
	LogLevel     string
}

type envFunc func(string) (string, bool)

func Load(args []string, lookup envFunc, goos, homeDir string) (*Config, error) {
	fs := flag.NewFlagSet("ai-usage", flag.ContinueOnError)
	httpAddr := fs.String("http", "", "dashboard + API listen address")
	otlpHTTP := fs.String("otlp-http", "", "OTLP/HTTP listen address")
	otlpGRPC := fs.String("otlp-grpc", "", "OTLP gRPC listen address (empty disables)")
	dataDir := fs.String("data-dir", "", "data directory")
	database := fs.String("database", "", "SQLite database path")
	logLevel := fs.String("log-level", "", "log level (debug|info|warn|error)")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	c := &Config{}
	var err error

	c.HTTPAddr, err = resolve("http", *httpAddr, set, lookup, ":8080")
	if err != nil {
		return nil, err
	}
	c.OTLPHTTPAddr, err = resolve("otlp-http", *otlpHTTP, set, lookup, ":4318")
	if err != nil {
		return nil, err
	}
	c.OTLPGRPCAddr, err = resolve("otlp-grpc", *otlpGRPC, set, lookup, ":4317")
	if err != nil {
		return nil, err
	}
	c.LogLevel, err = resolve("log-level", *logLevel, set, lookup, "info")
	if err != nil {
		return nil, err
	}

	if dir, ok := flagOrEnv("data-dir", *dataDir, set, lookup); ok {
		c.DataDir = dir
	} else {
		c.DataDir = filepath.Join(userDataDir(goos, homeDir, lookup), "ai-usage")
	}
	if err := os.MkdirAll(c.DataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	if path, ok := flagOrEnv("database", *database, set, lookup); ok {
		c.DatabasePath = path
	} else {
		c.DatabasePath = filepath.Join(c.DataDir, "usage.db")
	}

	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return nil, fmt.Errorf("invalid --log-level %q", c.LogLevel)
	}

	return c, nil
}

func resolve(name, value string, set map[string]bool, lookup envFunc, def string) (string, error) {
	v, ok := flagOrEnv(name, value, set, lookup)
	if !ok {
		return def, nil
	}
	return v, nil
}

func flagOrEnv(name, value string, set map[string]bool, lookup envFunc) (string, bool) {
	if set[name] {
		return value, true
	}
	if v, ok := lookup(envNames[name]); ok {
		return v, true
	}
	return "", false
}

var envNames = map[string]string{
	"http":      "AI_USAGE_HTTP_ADDR",
	"otlp-http": "AI_USAGE_OTLP_HTTP_ADDR",
	"otlp-grpc": "AI_USAGE_OTLP_GRPC_ADDR",
	"data-dir":  "AI_USAGE_DATA_DIR",
	"database":  "AI_USAGE_DATABASE",
	"log-level": "AI_USAGE_LOG_LEVEL",
}

func userDataDir(goos, homeDir string, lookup envFunc) string {
	switch goos {
	case "windows":
		if v, ok := lookup("LOCALAPPDATA"); ok && v != "" {
			return v
		}
		return filepath.Join(homeDir, "AppData", "Local")
	case "darwin":
		return filepath.Join(homeDir, "Library", "Application Support")
	default:
		if v, ok := lookup("XDG_DATA_HOME"); ok && v != "" {
			if !filepath.IsAbs(v) {
				return filepath.Join(homeDir, ".local", "share")
			}
			return v
		}
		return filepath.Join(homeDir, ".local", "share")
	}
}

func LoadOS() (*Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home dir: %w", err)
	}
	return Load(os.Args[1:], os.LookupEnv, runtime.GOOS, home)
}
