<p align="center">
  <img src="cover-image.png" alt="Label Backup Cover Image"/>
</p>

# Label Backup

**Label Backup is a Docker-aware backup agent that automatically discovers and backs up your containerized databases based on Docker labels.**

Simply add labels to your database containers, and Label Backup will automatically discover them, schedule backups according to your cron expressions, and manage the entire backup lifecycle. No manual configuration needed - just label your containers and let Label Backup handle the rest.

**Key Benefits:**

- **Zero Configuration**: Just add labels to your database containers
- **Multi-Database Support**: PostgreSQL, MySQL, MongoDB, Redis
- **Flexible Storage**: Local filesystem or S3-compatible services
- **Production Ready**: Health checks, webhooks, retention policies, circuit breakers
- **Docker Native**: Monitors container events and adapts automatically

## What Problem Does It Solve?

Managing database backups in containerized environments can be complex and error-prone. You need to:

- Manually configure backup schedules for each database
- Ensure backup tools are available in each container
- Handle different database types with different backup commands
- Manage backup storage and retention policies
- Monitor backup success/failure

Label Backup solves this by using Docker labels to automatically discover databases and their backup requirements, providing a unified backup solution that works across your entire containerized infrastructure.

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
- Complete backup information including timestamps, container details, size, and success status

### 🔔 **Webhook Notifications**

- Sends JSON payloads to your specified URLs on backup success or failure
- HMAC-SHA256 signed payloads for security
- Circuit breaker pattern to prevent webhook spam
- Configurable retry logic with exponential backoff

### 🗑️ **Retention Policies**

- Automatic pruning of old backups based on configurable retention periods
- Global retention policy with per-container overrides
- Dry-run mode to preview what would be deleted
- Supports days, hours, minutes (e.g., `"7d"`, `"24h"`, `"90m"`)

### 🏥 **Health Monitoring**

- Health check endpoints: `/healthz`, `/readyz`, `/status`
- Database connection pre-validation before backups
- Docker daemon connectivity monitoring
- Disk space monitoring for local backups

### 🔧 **Production Features**

- Graceful shutdown with context cancellation
- Concurrent backup limiting to prevent resource exhaustion
- Docker client reconnection with exponential backoff
- SIGHUP signal handling for configuration reload
- Structured error logging with context
- Optional GPG encryption support

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
```

### 2. Run the Stack

```bash
docker-compose up -d
```

### 3. Check Status

Visit `http://localhost:8080/status` to see active backup jobs and container information.

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

labels:
backup.enabled: "true"
backup.cron: "0 2 * * *" # Daily at 2 AM
backup.type: "postgres"
backup.conn: "postgresql://user:pass@host:5432/db"
backup.dest: "remote"
backup.prefix: "production/postgres"
backup.retention: "14d"
backup.webhook: "https://hooks.slack.com/services/your/webhook"

````

## Examples

Check out the `examples/` directory for complete working examples:

- **[PostgreSQL with Local Storage](examples/docker-compose.postgres-local.yml)** - Simple local backup setup
- **[MySQL with S3 Storage](examples/docker-compose.mysql-s3.yml)** - Cloud backup with S3
- **[MongoDB with MinIO](examples/docker-compose.mongodb-minio.yml)** - S3-compatible storage with MinIO
- **[Multi-Database Setup](examples/docker-compose.multi-database.yml)** - Multiple databases with different schedules
- **[Development Environment](examples/docker-compose.development.yml)** - Debug mode with dry-run

## Documentation

- **[Troubleshooting Guide](docs/TROUBLESHOOTING.md)** - Common issues and solutions
- **[Restore Guide](docs/RESTORE.md)** - How to restore from backups
- **[Examples](examples/)** - Complete working examples

## Advanced Features

### Health Check Endpoints

- `GET /healthz` - Basic health check
- `GET /readyz` - Readiness probe (checks Docker, disk space, S3)
- `GET /status` - Detailed status with active jobs and container information
- `GET /metadata?object=<backup-name>` - Query backup metadata

### Circuit Breaker

Webhook notifications use a circuit breaker pattern to prevent cascading failures:

- Opens after 5 consecutive failures
- Automatically attempts recovery after 30 seconds
- Prevents webhook spam during outages
- State can be monitored via application logs

### Backup Metadata

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

### GPG Encryption

Optional GPG encryption support:

```yaml
        environment:
  GPG_PUBLIC_KEY_PATH: "/keys/backup.pub"
````

### Configuration Reload

Send SIGHUP signal to reload configuration:

```bash
docker kill -s HUP label_backup_agent
```

**Note**: Configuration reload recreates scheduler and webhook components to ensure all settings are applied correctly.

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

- Health check endpoints (`/healthz`, `/readyz`, `/status`)
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
