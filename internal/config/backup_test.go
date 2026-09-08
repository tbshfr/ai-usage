package config

import "testing"

func TestBackupConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		env  map[string]string
		bad  bool
	}{
		{name: "disabled"},
		{name: "enabled R2", args: []string{"--backup-s3-bucket=bucket", "--backup-s3-prefix=ai-usage/home/", "--backup-s3-region=auto", "--backup-s3-endpoint=https://account.r2.cloudflarestorage.com"}},
		{name: "missing prefix", args: []string{"--backup-s3-bucket=bucket"}, bad: true},
		{name: "missing bucket", args: []string{"--backup-s3-region=auto"}, bad: true},
		{name: "explicit empty option", args: []string{"--backup-s3-region="}, bad: true},
		{name: "env missing bucket", env: map[string]string{"AI_USAGE_BACKUP_S3_PREFIX": "home/"}, bad: true},
		{name: "root prefix", args: []string{"--backup-s3-bucket=bucket", "--backup-s3-prefix=/"}, bad: true},
		{name: "unterminated prefix", args: []string{"--backup-s3-bucket=bucket", "--backup-s3-prefix=home"}, bad: true},
		{name: "endpoint credentials", args: []string{"--backup-s3-bucket=bucket", "--backup-s3-prefix=home/", "--backup-s3-endpoint=https://user:secret@host"}, bad: true},
		{name: "endpoint query", args: []string{"--backup-s3-bucket=bucket", "--backup-s3-prefix=home/", "--backup-s3-endpoint=https://host?secret=secret"}, bad: true},
		{name: "endpoint path", args: []string{"--backup-s3-bucket=bucket", "--backup-s3-prefix=home/", "--backup-s3-endpoint=https://host/path"}, bad: true},
		{name: "endpoint scheme", args: []string{"--backup-s3-bucket=bucket", "--backup-s3-prefix=home/", "--backup-s3-endpoint=ftp://host"}, bad: true},
		{name: "bucket URL", args: []string{"--backup-s3-bucket=https://host", "--backup-s3-prefix=home/"}, bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(tc.args, envOf(tc.env), "linux", t.TempDir())
			if (err != nil) != tc.bad {
				t.Fatalf("error = %v, want error %v", err, tc.bad)
			}
		})
	}
}
func TestBackupPrecedence(t *testing.T) {
	c, err := Load([]string{"--backup-s3-bucket=flag", "--backup-s3-region=auto", "--backup-s3-prefix=flag/", "--backup-s3-endpoint=https://flag.example"}, envOf(map[string]string{
		"AI_USAGE_BACKUP_S3_BUCKET": "env", "AI_USAGE_BACKUP_S3_REGION": "env",
		"AI_USAGE_BACKUP_S3_PREFIX": "env/", "AI_USAGE_BACKUP_S3_ENDPOINT": "https://env.example",
	}), "linux", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if c.BackupS3Bucket != "flag" || c.BackupS3Region != "auto" || c.BackupS3Prefix != "flag/" || c.BackupS3Endpoint != "https://flag.example" {
		t.Fatalf("%+v", c)
	}
}

func TestBackupCredentialPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "environment", want: "env"},
		{name: "flags", args: []string{"--backup-s3-access-key-id=flag", "--backup-s3-secret-access-key=flag", "--backup-s3-session-token=flag"}, want: "flag"},
		{name: "explicit empty flags", args: []string{"--backup-s3-access-key-id=", "--backup-s3-secret-access-key=", "--backup-s3-session-token="}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"--backup-s3-bucket=bucket", "--backup-s3-prefix=home/", "--backup-s3-region=auto"}, tc.args...)
			c, err := Load(args, envOf(map[string]string{
				"AI_USAGE_BACKUP_S3_ACCESS_KEY_ID":     "env",
				"AI_USAGE_BACKUP_S3_SECRET_ACCESS_KEY": "env",
				"AI_USAGE_BACKUP_S3_SESSION_TOKEN":     "env",
			}), "linux", t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if c.BackupS3AccessKeyID != tc.want || c.BackupS3SecretAccessKey != tc.want || c.BackupS3SessionToken != tc.want {
				t.Fatal("incorrect credential precedence")
			}
		})
	}
}

func TestBackupCredentialsRequireBucket(t *testing.T) {
	for _, name := range []string{"access-key-id", "secret-access-key", "session-token"} {
		t.Run(name, func(t *testing.T) {
			for _, value := range []string{"", "secret"} {
				if _, err := Load([]string{"--backup-s3-" + name + "=" + value}, envOf(nil), "linux", t.TempDir()); err == nil {
					t.Fatal("credential flag without bucket accepted")
				}
			}
		})
	}
	for _, name := range []string{"ACCESS_KEY_ID", "SECRET_ACCESS_KEY", "SESSION_TOKEN"} {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(nil, envOf(map[string]string{"AI_USAGE_BACKUP_S3_" + name: "secret"}), "linux", t.TempDir()); err == nil {
				t.Fatal("credential environment variable without bucket accepted")
			}
		})
	}
}

func TestLegacyBackupEnvironmentIgnored(t *testing.T) {
	c, err := Load([]string{"--backup-s3-bucket=bucket", "--backup-s3-prefix=home/"}, envOf(map[string]string{
		"S3_ACCESS_KEY_ID": "old", "S3_SECRET_ACCESS_KEY": "old", "S3_SESSION_TOKEN": "old",
		"S3_REGION": "old", "S3_DEFAULT_REGION": "old",
	}), "linux", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if c.BackupS3AccessKeyID != "" || c.BackupS3SecretAccessKey != "" || c.BackupS3SessionToken != "" || c.BackupS3Region != "" {
		t.Fatal("legacy environment variables were used")
	}
}
