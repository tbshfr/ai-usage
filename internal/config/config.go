package config

import (
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type Config struct {
	BackupS3SessionToken    string
	BackupS3SecretAccessKey string
	BackupS3AccessKeyID     string
	BackupS3Bucket          string
	BackupS3Region          string
	BackupS3Prefix          string
	BackupS3Endpoint        string
	HTTPAddr                string
	OTLPHTTPAddr            string
	OTLPGRPCAddr            string
	DataDir                 string
	DatabasePath            string
	LogLevel                string
	DashboardUser           string
	DashboardPassword       string
	OTLPToken               string
}

type envFunc func(string) (string, bool)

func Load(args []string, lookup envFunc, goos, homeDir string) (*Config, error) {
	fs := flag.NewFlagSet("ai-usage", flag.ContinueOnError)
	httpAddr := fs.String("http", "", "dashboard + API listen address")
	otlpHTTP := fs.String("otlp-http", "", "OTLP/HTTP listen address (disabled unless set)")
	otlpGRPC := fs.String("otlp-grpc", "", "OTLP gRPC listen address (disabled unless set)")
	dataDir := fs.String("data-dir", "", "data directory")
	database := fs.String("database", "", "SQLite database path")
	logLevel := fs.String("log-level", "", "log level (debug|info|warn|error)")
	dashUser := fs.String("dashboard-user", "", "dashboard login username (required for non-loopback binds)")
	dashPass := fs.String("dashboard-password", "", "dashboard login password (required for non-loopback binds)")
	otlpToken := fs.String("otlp-token", "", "bearer token OTLP clients must send (required for non-loopback binds)")

	backupBucket := fs.String("backup-s3-bucket", "", "backup bucket (empty disables backups)")
	backupRegion := fs.String("backup-s3-region", "", "backup region (auto for R2)")
	backupPrefix := fs.String("backup-s3-prefix", "", "dedicated backup object prefix ending in /")
	backupEndpoint := fs.String("backup-s3-endpoint", "", "S3-compatible endpoint URL")
	backupAccessKeyID := fs.String("backup-s3-access-key-id", "", "backup access key ID")
	backupSecretAccessKey := fs.String("backup-s3-secret-access-key", "", "backup secret access key")
	backupSessionToken := fs.String("backup-s3-session-token", "", "backup optional session token")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	c := &Config{}
	c.BackupS3Bucket, _ = flagOrEnv("backup-s3-bucket", *backupBucket, set, lookup)
	c.BackupS3Region, _ = flagOrEnv("backup-s3-region", *backupRegion, set, lookup)
	c.BackupS3Prefix, _ = flagOrEnv("backup-s3-prefix", *backupPrefix, set, lookup)
	c.BackupS3Endpoint, _ = flagOrEnv("backup-s3-endpoint", *backupEndpoint, set, lookup)
	c.BackupS3AccessKeyID, _ = flagOrEnv("backup-s3-access-key-id", *backupAccessKeyID, set, lookup)
	c.BackupS3SecretAccessKey, _ = flagOrEnv("backup-s3-secret-access-key", *backupSecretAccessKey, set, lookup)
	c.BackupS3SessionToken, _ = flagOrEnv("backup-s3-session-token", *backupSessionToken, set, lookup)
	if c.BackupS3Bucket == "" {
		for name, value := range map[string]string{
			"backup-s3-region":            *backupRegion,
			"backup-s3-prefix":            *backupPrefix,
			"backup-s3-endpoint":          *backupEndpoint,
			"backup-s3-access-key-id":     *backupAccessKeyID,
			"backup-s3-secret-access-key": *backupSecretAccessKey,
			"backup-s3-session-token":     *backupSessionToken,
		} {
			if _, supplied := flagOrEnv(name, value, set, lookup); supplied {
				return nil, fmt.Errorf("--%s requires --backup-s3-bucket", name)
			}
		}
	} else {
		if strings.TrimSpace(c.BackupS3Bucket) != c.BackupS3Bucket || strings.ContainsAny(c.BackupS3Bucket, "/\\:@?#") {
			return nil, fmt.Errorf("invalid --backup-s3-bucket")
		}
		prefix := c.BackupS3Prefix
		if strings.TrimSpace(prefix) != prefix || prefix == "" || prefix == "/" || strings.HasPrefix(prefix, "/") || !strings.HasSuffix(prefix, "/") || strings.ContainsAny(prefix, "\r\n\\") {
			return nil, fmt.Errorf("--backup-s3-prefix requires a dedicated nonempty prefix ending in /")
		}
		if c.BackupS3Endpoint != "" {
			u, e := url.Parse(c.BackupS3Endpoint)
			if e != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
				return nil, fmt.Errorf("invalid --backup-s3-endpoint: use an http(s) origin without credentials, query or path")
			}
		}
	}
	var err error

	c.HTTPAddr, err = resolve("http", *httpAddr, set, lookup, "127.0.0.1:8080")
	if err != nil {
		return nil, err
	}
	c.OTLPHTTPAddr, err = resolve("otlp-http", *otlpHTTP, set, lookup, "")
	if err != nil {
		return nil, err
	}
	c.OTLPGRPCAddr, err = resolve("otlp-grpc", *otlpGRPC, set, lookup, "")
	if err != nil {
		return nil, err
	}
	c.LogLevel, err = resolve("log-level", *logLevel, set, lookup, "info")
	if err != nil {
		return nil, err
	}
	c.DashboardUser, err = resolve("dashboard-user", *dashUser, set, lookup, "")
	if err != nil {
		return nil, err
	}
	c.DashboardPassword, err = resolve("dashboard-password", *dashPass, set, lookup, "")
	if err != nil {
		return nil, err
	}
	c.OTLPToken, err = resolve("otlp-token", *otlpToken, set, lookup, "")
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

	if err := c.validateAuth(); err != nil {
		return nil, err
	}

	return c, nil
}

// validateAuth refuses non-loopback listener binds without credentials:
// anything reachable beyond this machine must be authenticated.
func (c *Config) validateAuth() error {
	if (c.DashboardUser == "") != (c.DashboardPassword == "") {
		return fmt.Errorf("--dashboard-user and --dashboard-password must be set together (env: AI_USAGE_DASHBOARD_USER / AI_USAGE_DASHBOARD_PASSWORD)")
	}
	if c.HTTPAddr != "" && !c.DashboardAuthEnabled() {
		if err := requireLoopback(c.HTTPAddr, "--http", "AI_USAGE_DASHBOARD_USER and AI_USAGE_DASHBOARD_PASSWORD (or --dashboard-user/--dashboard-password)"); err != nil {
			return err
		}
	}
	if c.OTLPHTTPAddr != "" && c.OTLPToken == "" {
		if err := requireLoopback(c.OTLPHTTPAddr, "--otlp-http", "AI_USAGE_OTLP_TOKEN (or --otlp-token)"); err != nil {
			return err
		}
	}
	if c.OTLPGRPCAddr != "" && c.OTLPToken == "" {
		if err := requireLoopback(c.OTLPGRPCAddr, "--otlp-grpc", "AI_USAGE_OTLP_TOKEN (or --otlp-token)"); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) DashboardAuthEnabled() bool {
	return c.DashboardUser != "" && c.DashboardPassword != ""
}

func requireLoopback(addr, flagName, creds string) error {
	loop, err := isLoopbackAddr(addr)
	if err != nil {
		return fmt.Errorf("invalid %s %q: %w", flagName, addr, err)
	}
	if !loop {
		return fmt.Errorf("refusing to bind %s to non-loopback %q without credentials; set %s (or bind to 127.0.0.1)", flagName, addr, creds)
	}
	return nil
}

func isLoopbackAddr(addr string) (bool, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false, err
	}
	if host == "" {
		return false, nil
	}
	if strings.EqualFold(host, "localhost") {
		return true, nil
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback(), nil
	}
	return false, nil
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
	"backup-s3-session-token":     "AI_USAGE_BACKUP_S3_SESSION_TOKEN",
	"backup-s3-secret-access-key": "AI_USAGE_BACKUP_S3_SECRET_ACCESS_KEY",
	"backup-s3-access-key-id":     "AI_USAGE_BACKUP_S3_ACCESS_KEY_ID",
	"backup-s3-bucket":            "AI_USAGE_BACKUP_S3_BUCKET",
	"backup-s3-region":            "AI_USAGE_BACKUP_S3_REGION",
	"backup-s3-prefix":            "AI_USAGE_BACKUP_S3_PREFIX",
	"backup-s3-endpoint":          "AI_USAGE_BACKUP_S3_ENDPOINT",
	"http":                        "AI_USAGE_HTTP_ADDR",
	"otlp-http":                   "AI_USAGE_OTLP_HTTP_ADDR",
	"otlp-grpc":                   "AI_USAGE_OTLP_GRPC_ADDR",
	"data-dir":                    "AI_USAGE_DATA_DIR",
	"database":                    "AI_USAGE_DATABASE",
	"log-level":                   "AI_USAGE_LOG_LEVEL",
	"dashboard-user":              "AI_USAGE_DASHBOARD_USER",
	"dashboard-password":          "AI_USAGE_DASHBOARD_PASSWORD",
	"otlp-token":                  "AI_USAGE_OTLP_TOKEN",
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
