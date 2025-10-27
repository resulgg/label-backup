<p align="center">
  <img src="cover-image.png" alt="Label Backup Cover Image"/>
</p>

# Label Backup

**Label Backup is a Docker-aware backup agent that automatically discovers and backs up your containerized databases based on Docker labels.**

Simply add labels to your database containers, and Label Backup will automatically discover them, schedule backups according to your cron expressions, and manage the entire backup lifecycle. No manual configuration needed - just label your containers and let Label Backup handle the rest.

## What Problem Does It Solve?

Managing database backups in containerized environments can be complex and error-prone. You need to:

- Manually configure backup schedules for each database
- Ensure backup tools are available in each container
- Handle different database types with different backup commands
- Manage backup storage and retention policies
- Monitor backup success/failure

Label Backup solves this by using Docker labels to automatically discover databases and their backup requirements, providing a unified backup solution that works across your entire containerized infrastructure.

## Quick Start

### 1. Basic Setup with Docker Compose

Create a `docker-compose.yml` file:

```yaml
version: "3.8"

services:
  postgres:
    image: postgres:15
    environment:
      POSTGRES_DB: myapp
      POSTGRES_USER: appuser
      POSTGRES_PASSWORD: apppass
    labels:
      backup.enabled: "true"
      backup.cron: "0 2 * * *" # Daily at 2 AM
      backup.type: "postgres"
      backup.conn: "postgresql://appuser:apppass@postgres:5432/myapp"
      backup.dest: "local"
      backup.prefix: "postgres-backups"
      backup.retention: "7d"
    networks:
      - backup_network

  label-backup:
    image: resulgg/label-backup
    environment:
      LOCAL_BACKUP_PATH: "/backups"
      GLOBAL_RETENTION_PERIOD: "30d"
      LOG_LEVEL: "info"
    volumes:
      - ./backups:/backups
      - /var/run/docker.sock:/var/run/docker.sock:ro
    depends_on:
      - postgres
    ports:
      - "8080:8080"
    networks:
      - backup_network

networks:
  backup_network:
    driver: bridge
```

### 2. Run the Stack

```bash
docker-compose up -d
```

### 3. Alternative: Using Docker Run

If you prefer using `docker run` instead of Docker Compose:

```bash
# Run your database container with labels
docker run -d \
  --name postgres-db \
  -e POSTGRES_DB=myapp \
  -e POSTGRES_USER=appuser \
  -e POSTGRES_PASSWORD=apppass \
  -l backup.enabled="true" \
  -l backup.cron="0 2 * * *" \
  -l backup.type="postgres" \
  -l backup.conn="postgresql://appuser:apppass@postgres-db:5432/myapp" \
  -l backup.dest="local" \
  -l backup.prefix="postgres-backups" \
  -l backup.retention="7d" \
  postgres:15

# Run Label Backup
docker run -d \
  --name label-backup \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  -v ./backups:/backups \
  -e LOCAL_BACKUP_PATH=/backups \
  -e GLOBAL_RETENTION_PERIOD="30d" \
  -e LOG_LEVEL="info" \
  -p 8080:8080 \
  --network container:postgres-db \
  resulgg/label-backup
```

## Key Features

### 🔍 **Automatic Discovery**

- Watches Docker events and inspects container labels to manage backup jobs dynamically
- No manual configuration needed - just add labels to your database containers
- Automatically handles container start/stop events

### ⏰ **Flexible Scheduling**

- Uses `cron` expressions (with seconds field support) for per-container backup schedules
- Supports complex schedules like `"0 */6 * * *"` (every 6 hours) or `"0 2 * * 0"` (weekly)
- Each container can have its own schedule

### 🗄️ **Multiple Database Engines**

- **PostgreSQL**: Uses `pg_dump` with connection string support
- **MySQL**: Uses `mysqldump` with authentication handling
- **MongoDB**: Uses `mongodump` with database-specific backups
- **Redis**: Uses `redis-cli --rdb` for Redis database snapshots

### 📦 **Multiple Destinations**

- **Local Storage**: Store backups on local filesystem with disk space checks
- **S3-Compatible**: Amazon S3, MinIO, Cloudflare R2, and other S3-compatible services
- Automatic bucket validation and connection testing

### 🗜️ **Compression & Integrity**

- On-the-fly Gzip compression of backup streams
- SHA256 checksum calculation for backup verification
- Backup metadata files with detailed information
- **Metadata API**: Query backup metadata via `/metadata?object=<backup-name>` endpoint

### 🗑️ **Retention Policies**

- Automatic pruning of old backups based on configurable retention periods
- Global retention policy with per-container overrides
- Dry-run mode to preview what would be deleted
- Supports days, hours, minutes (e.g., `"7d"`, `"24h"`, `"90m"`)

### 🏥 **Health Monitoring**

- Health check endpoints: `/healthz`, `/readyz`
- Database connection pre-validation before backups
- Docker daemon connectivity monitoring
- Disk space monitoring for local backups

## Configuration

### Environment Variables

#### Core Settings

- `LOG_LEVEL`: Logging level (`debug`, `info`, `warn`, `error`). Default: `info`
- `GLOBAL_RETENTION_PERIOD`: Default retention period. Examples: `"7d"`, `"24h"`, `"90m"`. Default: `"7d"`
- `GC_DRY_RUN`: If `"true"`, only log what would be deleted. Default: `"false"`
- `LOCAL_BACKUP_PATH`: Base path for local backups. Default: `/backups`
- `RECONCILE_INTERVAL_SECONDS`: How often to check for new containers. Default: `10`

#### S3 Configuration

- `BUCKET_NAME`: S3 bucket name (required for S3 backups)
- `REGION`: AWS region. Default: `us-east-1`
- `ENDPOINT`: Custom S3 endpoint (e.g., `http://minio:9000` for MinIO)
- `ACCESS_KEY_ID`: S3 access key
- `SECRET_ACCESS_KEY`: S3 secret key
- `S3_USE_PATH_STYLE`: Use path-style addressing. Default: `false`

#### Webhook Notifications

- `WEBHOOK_URL`: Global webhook URL for notifications
- `WEBHOOK_SECRET`: Secret for HMAC-SHA256 signing
- `WEBHOOK_TIMEOUT_SECONDS`: HTTP timeout. Default: `10`
- `WEBHOOK_MAX_RETRIES`: Maximum retries. Default: `3`

#### Advanced Settings

- `CONCURRENT_BACKUP_LIMIT`: Maximum concurrent backups. Default: `20`
- `BACKUP_TIMEOUT_MINUTES`: Timeout for backup operations in minutes. Default: `30`
- `GPG_PUBLIC_KEY_PATH`: Path to GPG public key for encryption (optional)

### Docker Labels

Add these labels to your database containers:

#### Required Labels

- `backup.enabled`: `"true"` or `"false"` - Master switch
- `backup.type`: Database type (`postgres`, `mysql`, `mongodb`, `redis`)
- `backup.cron`: Cron expression for scheduling

#### Connection Labels

- `backup.conn`: Connection string/URI for the database
- `backup.database`: Specific database name (for MongoDB)

#### Optional Labels

- `backup.dest`: Destination (`local` or `remote`). Default: `local`
- `backup.prefix`: Prefix for backup filenames
- `backup.retention`: Retention period (overrides global)
- `backup.webhook`: Custom webhook URL (overrides global)

#### Example Labels

```yaml
backup.enabled: "true"
backup.cron: "0 2 * * *" # Daily at 2 AM
backup.type: "postgres"
backup.conn: "postgresql://user:pass@host:5432/db"
backup.dest: "remote"
backup.prefix: "production/postgres"
backup.retention: "14d"
backup.webhook: "https://hooks.slack.com/services/your/webhook"
```

## Examples

Check out the `examples/` directory for complete working examples:

- **[PostgreSQL with Local Storage](examples/docker-compose.postgres-local.yml)** - Simple local backup setup
- **[MySQL with S3 Storage](examples/docker-compose.mysql-s3.yml)** - Cloud backup with S3
- **[MongoDB with MinIO](examples/docker-compose.mongodb-minio.yml)** - S3-compatible storage with MinIO
- **[Multi-Database Setup](examples/docker-compose.multi-database.yml)** - Multiple databases with different schedules
- **[Development Environment](examples/docker-compose.development.yml)** - Debug mode with dry-run

## Webhook Notifications

### Overview

Label Backup sends detailed JSON payloads to your specified URLs on backup success or failure with HMAC-SHA256 signed payloads for security.

### Webhook Payload Structure

**Success Payload Example:**

```json
{
  "container_id": "dbe737c824731c8f8c34e356d38a183502024c136a180618cf34ae08783ef649",
  "container_name": "myapp_postgres",
  "database_type": "postgres",
  "database_name": "myapp",
  "destination_url": "s3://my-backup-bucket/postgres-backups/postgres-myapp-20250124105400.sql.gz",
  "success": true,
  "backup_size_bytes": 1048576,
  "duration_seconds": 2.45,
  "timestamp_utc": "2025-01-24T10:54:00.123456Z",
  "cron_schedule": "0 2 * * *",
  "backup_prefix": "postgres-backups",
  "destination_type": "s3"
}
```

**Failure Payload Example:**

```json
{
  "container_id": "dbe737c824731c8f8c34e356d38a183502024c136a180618cf34ae08783ef649",
  "container_name": "myapp_postgres",
  "database_type": "postgres",
  "database_name": "myapp",
  "destination_url": "",
  "success": false,
  "error": "connection refused: database server is not responding",
  "duration_seconds": 30.0,
  "timestamp_utc": "2025-01-24T10:54:00.123456Z",
  "cron_schedule": "0 2 * * *",
  "backup_prefix": "postgres-backups",
  "destination_type": "s3"
}
```

### Security & Signature Verification

All webhook payloads are signed using HMAC-SHA256:

**HTTP Headers:**

- `Content-Type: application/json`
- `User-Agent: LabelBackupAgent/1.0`
- `X-LabelBackup-Signature-SHA256: <hex-encoded-hmac-signature>`

**Signature Calculation:** `HMAC-SHA256(webhook_secret, raw_json_body)`

### Implementation Example

Here's a complete Node.js/Express example for receiving and verifying webhooks:

```javascript
const express = require("express");
const crypto = require("crypto");

const app = express();
const WEBHOOK_SECRET = process.env.WEBHOOK_SECRET || "your-secret-key";

// Middleware to parse JSON
app.use(
  express.json({
    verify: (req, res, buf) => {
      req.rawBody = buf; // Store raw body for signature verification
    },
  })
);

// Timing-safe string comparison
function timingSafeEqual(a, b) {
  if (a.length !== b.length) {
    return false;
  }
  let result = 0;
  for (let i = 0; i < a.length; i++) {
    result |= a.charCodeAt(i) ^ b.charCodeAt(i);
  }
  return result === 0;
}

// Verify webhook signature
function verifySignature(payload, signature, secret) {
  const expectedSignature = crypto
    .createHmac("sha256", secret)
    .update(payload)
    .digest("hex");

  return timingSafeEqual(signature, expectedSignature);
}

// Webhook endpoint
app.post("/webhook", (req, res) => {
  try {
    const signature = req.headers["x-labelbackup-signature-sha256"];
    const rawBody = req.rawBody;

    // Verify signature
    if (!signature || !verifySignature(rawBody, signature, WEBHOOK_SECRET)) {
      console.error("Invalid webhook signature");
      return res.status(401).json({ error: "Invalid signature" });
    }

    // Parse payload
    const payload = req.body;

    // Process webhook based on success/failure
    if (payload.success) {
      console.log(`✅ Backup successful for ${payload.container_name}`);
      console.log(
        `   Database: ${payload.database_type}/${
          payload.database_name || "default"
        }`
      );
      console.log(
        `   Size: ${(payload.backup_size_bytes / 1024 / 1024).toFixed(2)} MB`
      );
      console.log(`   Duration: ${payload.duration_seconds}s`);
      console.log(`   Destination: ${payload.destination_url}`);
    } else {
      console.error(`❌ Backup failed for ${payload.container_name}`);
      console.error(`   Error: ${payload.error}`);
      console.error(`   Duration: ${payload.duration_seconds}s`);
    }

    res.status(200).json({ received: true });
  } catch (error) {
    console.error("Webhook processing error:", error);
    res.status(500).json({ error: "Internal server error" });
  }
});

const PORT = process.env.PORT || 3000;
app.listen(PORT, () => {
  console.log(`Webhook server listening on port ${PORT}`);
});
```

## Documentation

- **[Troubleshooting Guide](docs/TROUBLESHOOTING.md)** - Common issues and solutions
- **[Restore Guide](docs/RESTORE.md)** - How to restore from backups
- **[Examples](examples/)** - Complete working examples

## Advanced Features

### 🔧 **Production Features**

- Graceful shutdown with context cancellation
- Concurrent backup limiting to prevent resource exhaustion
- Docker client reconnection with exponential backoff
- SIGHUP signal handling for configuration reload
- Structured error logging with context
- Optional GPG encryption support

### 🔔 **Webhook Notifications**

- Sends JSON payloads to your specified URLs on backup success or failure
- HMAC-SHA256 signed payloads for security
- Circuit breaker pattern to prevent webhook spam
- Configurable retry logic with exponential backoff

### 📊 **Backup Metadata**

Each backup creates a `.metadata.json` file containing:

- Timestamp and duration
- Container and database information
- Backup size and checksum
- Success/failure status
- Compression type and version

**Querying Metadata:**

You can query backup metadata via the HTTP API:

```bash
# Get metadata for a specific backup
curl "http://localhost:8080/metadata?object=test-mongo-backup/mongodb-testmongodb-20251024105400.dump.gz"

# Example response:
{
  "timestamp": "2025-10-24T10:54:00.003475093Z",
  "container_id": "dbe737c824731c8f8c34e356d38a183502024c136a180618cf34ae08783ef649",
  "container_name": "test_mongo_db",
  "database_type": "mongodb",
  "database_name": "testmongodb",
  "backup_size_bytes": 116,
  "checksum": "calculated-during-write",
  "compression_type": "gzip",
  "version": "1.0",
  "destination": "http://minio:9000/test-bucket/test-mongo-backup/mongodb-testmongodb-20251024105400.dump.gz",
  "duration_seconds": 0.057319415,
  "success": true
}
```

### 🔄 **Configuration Reload**

Send SIGHUP signal to reload configuration:

```bash
docker kill -s HUP label_backup_agent
```

**Note**: Configuration reload recreates scheduler and webhook components to ensure all settings are applied correctly.

### 🔐 **GPG Encryption**

Optional GPG encryption support:

```yaml
environment:
  GPG_PUBLIC_KEY_PATH: "/keys/backup.pub"
```

### 🛡️ **Circuit Breaker**

Webhook notifications use a circuit breaker pattern to prevent cascading failures:

- Opens after 5 consecutive failures
- Automatically attempts recovery after 30 seconds
- Prevents webhook spam during outages
- State can be monitored via application logs

### 🏥 **Health Check Endpoints**

- `GET /healthz` - Basic health check
- `GET /readyz` - Readiness probe (checks Docker, disk space, S3)
- `GET /metadata?object=<backup-name>` - Query backup metadata

## Testing

### Comprehensive Test Suite

A complete test environment is provided to test all features:

```bash
# Build and start the test environment
docker-compose -f integration-tests/docker-compose-test.yml up --build -d

# Run the comprehensive test suite
./integration-tests/test.sh

# Stop the test environment
docker-compose -f integration-tests/docker-compose-test.yml down -v
```

**Test Environment Includes:**

- All 4 database types (PostgreSQL, MySQL, MongoDB, Redis)
- MinIO S3-compatible storage
- Webhook test server
- Staggered cron schedules (2min, 3min, 4min, 5min intervals)
- All production features enabled

**Test Coverage:**

- Health check endpoints (`/healthz`, `/readyz`)
- Database connection testing
- S3 backup functionality
- Webhook notifications
- SIGHUP configuration reload
- Backup file generation
- Circuit breaker functionality

## Building from Source

1. Clone the repository
2. Ensure you have Go 1.24+ installed
3. Build: `go build -o label-backup .`
4. Build Docker image: `docker build -t resulgg/label-backup .`

## Contributing

Contributions are welcome! Please feel free to open an issue or submit a pull request.

## License

This project is open-source and available under the MIT License.
