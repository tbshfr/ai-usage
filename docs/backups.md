# Backups and restore

AI Usage can back up your database once a day to Amazon S3 or an S3-compatible
service such as Cloudflare R2. Each backup is a complete copy of the database,
compressed into a `.sqlite.gz` file. Backups are off until you configure a bucket.

- [Set up backups](#set-up-backups)
- [Manage backups](#manage-backups)
- [Delete old backups automatically](#delete-old-backups-automatically)
- [Download a backup](#download-a-backup)
- [Restore](#restore)
- [Permissions](#permissions)
- [Troubleshooting](#troubleshooting)

## Set up backups

### 1. Create a bucket and access key

Create a private bucket and an access key that can upload, list, and download
files in it. A dedicated bucket makes permissions and retention easier to manage.

For Cloudflare R2:

1. Create a bucket in the Cloudflare dashboard.
2. Under R2's Account Details, select Manage next to API Tokens. Create a token
   with Object Read & Write permission for your backup bucket.
3. Save the Access Key ID and Secret Access Key. Note your S3 API endpoint,
   `https://<ACCOUNT_ID>.r2.cloudflarestorage.com`. If your bucket has a
   jurisdiction, include it, such as `<ACCOUNT_ID>.eu.r2.cloudflarestorage.com`.
   The bucket's settings page shows this URL followed by the bucket name; leave
   the bucket name out, or AI Usage won't start.

See [R2 authentication](https://developers.cloudflare.com/r2/api/tokens/) for
Cloudflare's setup instructions.

### 2. Configure AI Usage

Set these environment variables for the application. This example uses R2;
replace the placeholders with your values:

```dotenv
AI_USAGE_BACKUP_S3_BUCKET=ai-usage-backups
AI_USAGE_BACKUP_S3_REGION=auto
AI_USAGE_BACKUP_S3_PREFIX=prod/
AI_USAGE_BACKUP_S3_ENDPOINT=https://<ACCOUNT_ID>.r2.cloudflarestorage.com
AI_USAGE_BACKUP_S3_ACCESS_KEY_ID=<R2_ACCESS_KEY_ID>
AI_USAGE_BACKUP_S3_SECRET_ACCESS_KEY=<R2_SECRET_ACCESS_KEY>
```

The prefix is the folder-like path where backups are stored. Give each AI Usage
instance its own prefix ending in `/`.

For Amazon S3, set the bucket's region and leave out `AI_USAGE_BACKUP_S3_ENDPOINT`.
For other providers, use their region and authenticated S3 API endpoint. If your
credentials include a session token, also set `AI_USAGE_BACKUP_S3_SESSION_TOKEN`.
The application needs these credentials explicitly; it does not use AWS CLI
profiles or automatically discover AWS roles.

With Docker Compose, copy the backup entries from [.env.example](../.env.example)
into your `.env`, fill them in, and uncomment the backup environment block in
[compose-example.yaml](../compose-example.yaml). Adding values to `.env` alone
does not pass them into the container.

### 3. Apply the settings

Restart the application. For Docker Compose, run this from the directory
containing your Compose file so the updated environment is applied:

```sh
docker compose up -d
```

Invalid backup configuration can prevent startup. Missing or incorrect
credentials cause backups to fail while the application keeps running. The
first backup starts right away. Open Settings, then Backups, to check that it
succeeds.

Leave enough free space next to the database file, which is in the data
directory unless you set a different path, for a full database copy and its
compressed file, plus space for incoming data during the backup.

## Manage backups

In Settings, under Backups, you can check the last successful backup and the next
scheduled run, change the daily backup time, or select Back up now.

Until you choose a time, backups run daily at the time of the first backup.
Your chosen time uses your browser's time zone and follows daylight saving
changes. It takes effect without a restart. Running a backup manually does not
change the daily time. If the app is stopped when a backup is due, it runs when
the app starts again. The backup status and stored backups list show times in
UTC.

Stored backups shows the latest file, its size and upload time, and the total
number and size of your backups. Expand the older backups to see more files.

Backups contain everything stored in the database, including usage records and
stored settings. Anyone with dashboard access can download them, so keep your
bucket and downloaded files private.

## Delete old backups automatically

The application does not delete backups. Set a lifecycle rule in your storage
provider's dashboard to remove old files after a period you choose, such as
seven days.

In R2, open your bucket's Settings, then Object Lifecycle Rules. Add a rule for
your backup prefix, such as `prod/`, and set it to delete objects after
seven days. Keep any existing rules. See
[R2 object lifecycles](https://developers.cloudflare.com/r2/buckets/object-lifecycles/).

Deletion can take time, so expired files may remain visible for a while. Rules
continue deleting old backups even if new uploads stop; check regularly that
backups are succeeding. If you use S3 versioning, configure expiration for older
versions too.

## Download a backup

Choose whichever method is available:

- Use your storage provider's web UI, if it has one. For example, open your R2
  bucket in the Cloudflare dashboard, browse to the backup prefix, and download
  the `.sqlite.gz` file you want.
- If AI Usage is still running, open Settings, then Backups. Under Stored
  backups, select Download next to the backup you want.
- Use an S3 CLI, such as the AWS CLI, to download directly from the bucket.

### Using the AWS CLI

Configure the CLI with `aws configure`, using an access key that can list and
read your backups. The CLI has its own credentials; it does not read the
application's `AI_USAGE_BACKUP_S3_*` variables.

For R2, list the available backups:

```sh
aws s3 ls s3://ai-usage-backups/prod/ \
  --endpoint-url 'https://<ACCOUNT_ID>.r2.cloudflarestorage.com' --region auto
```

Then download the file you want, saving it as `backup.sqlite.gz`:

```sh
aws s3 cp 's3://ai-usage-backups/prod/<backup-filename>.sqlite.gz' backup.sqlite.gz \
  --endpoint-url 'https://<ACCOUNT_ID>.r2.cloudflarestorage.com' --region auto
```

Replace the bucket, prefix, endpoint, and filename with your values. For Amazon
S3, omit `--endpoint-url` and use your bucket's region. See the
[AWS CLI download examples](https://docs.aws.amazon.com/cli/latest/reference/s3/cp.html#examples).

## Restore

Restoring replaces the current database with the backup. Data recorded after
that backup will be lost. This includes OTLP tokens: tokens created since the
backup stop working, and tokens deleted since then work again. Use the same
application version that created the backup, or a compatible newer version.

The example below uses the supplied Docker Compose setup, where
`./data/usage.db` on the host is `/data/usage.db` inside the container. Run the
commands on the host, from the directory containing your Compose file. If any
command fails, resolve the error before continuing.

1. Download a backup using one of the methods above. Copy it to the host running
   AI Usage and save it as `backup.sqlite.gz` in your Compose directory.

2. Stop the application before replacing any database files:

   ```sh
   docker compose stop ai-usage
   ```

3. Decompress the backup. This creates `backup.sqlite` and keeps the compressed
   file:

   ```sh
   gzip -dk backup.sqlite.gz
   ```

4. Copy the database into place and remove any old WAL and SHM files left beside
   it. These files belong to the previous database. Set ownership so the
   container can read and write the restored file:

   ```sh
   sudo cp backup.sqlite ./data/usage.db
   sudo rm -f ./data/usage.db-wal ./data/usage.db-shm
   sudo chown 65532:65532 ./data/usage.db
   ```

5. Start the application and open the dashboard to check your restored data:

   ```sh
   docker compose start ai-usage
   ```

If you run AI Usage without Docker, follow the same steps using your usual stop
and start commands. Put the file at the path set by `--database` or
`AI_USAGE_DATABASE`; by default it is `usage.db` in the application's data
directory. Adjust the WAL and SHM filenames to match, and make the restored file
writable by the user running AI Usage. See [configuration](configuration.md#options)
for the database and data directory settings.

## Permissions

The application needs permission to upload backups, list files under its
prefix, and download them. In AWS S3, these are `s3:PutObject`, `s3:ListBucket`,
and `s3:GetObject`. In R2, use Object Read & Write scoped to the backup bucket.
It does not need an administrator token or permission to manage lifecycle rules.

An upload-only key can still create backups, but the app cannot list or download
them. A separate key used only for downloading needs read and list permissions;
R2 calls this Object Read only.

## Troubleshooting

If a backup fails, the dashboard shows an error and the application retries
automatically. You can also select Back up now to retry immediately. Check the
last successful backup time: a working daily schedule usually means losing at
most about a day's data, but failures or downtime can leave a larger gap.

For upload errors, check the access key, bucket, region, endpoint, and network
connection. Restart the app after changing credentials. Changing the bucket,
region, prefix, endpoint, or database path counts as a new destination, so a
backup runs right after the restart. For failures while creating the local
backup, check free space and write permissions next to the database file. A
failure during `size_limit` means the compressed backup is larger than 5 GB,
which AI Usage can't upload. Backup failures do not stop the dashboard or
incoming usage data.

If the stored backup list or a download fails, check the key's read and list
permissions. The list can take a few minutes to reflect changes made directly
in the bucket.
