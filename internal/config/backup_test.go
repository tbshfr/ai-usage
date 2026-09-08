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
