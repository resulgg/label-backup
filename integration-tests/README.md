# Integration Tests

This directory contains comprehensive integration tests for the Label Backup application.

## Files

- `docker-compose-test.yml` - Complete test environment with MinIO S3 server
- `docker-compose-r2-test.yml` - Cloudflare R2 S3-compatible storage test environment
- `test-webhook.conf` - Nginx configuration for webhook testing
- `test.sh` - Comprehensive test script

## Quick Start

### Option 1: MinIO S3 Test (Recommended for Development)

```bash
# Navigate to integration-tests directory
cd integration-tests

# Build and start the test environment with MinIO
docker-compose -f docker-compose-test.yml up --build -d

# Wait for services to start (about 30 seconds)
sleep 30

# Run the comprehensive test suite
./test.sh

# Stop the test environment
docker-compose -f docker-compose-test.yml down -v
```

### Option 2: Cloudflare R2 Test (Production-like S3)

```bash
# Navigate to integration-tests directory
cd integration-tests

# Configure your R2 credentials in docker-compose-r2-test.yml
# Update: ENDPOINT, BUCKET_NAME, REGION, ACCESS_KEY_ID, SECRET_ACCESS_KEY

# Build and start the R2 test environment
docker-compose -f docker-compose-r2-test.yml up --build -d

# Wait for services to start
sleep 30

# Monitor backup logs
docker logs -f label_backup_r2

# Stop the test environment
docker-compose -f docker-compose-r2-test.yml down -v
```

**Note**: Both test environments build the Label Backup image locally from the source code in the parent directory.

## Test Environments

### MinIO S3 Test Environment

The MinIO test environment includes:

- **Label Backup Agent** - Main application with debug logging
- **MinIO S3 Server** - S3-compatible storage for backups
- **Webhook Test Server** - Nginx-based webhook receiver
- **PostgreSQL** - Test database with 1-minute backup schedule
- **MongoDB** - Test database with 1-minute backup schedule
- **MySQL** - Test database with 1-minute backup schedule
- **Redis** - Test database with 1-minute backup schedule

### Cloudflare R2 Test Environment

The R2 test environment includes:

- **Label Backup Agent** - Main application with debug logging
- **Webhook Test Server** - Nginx-based webhook receiver
- **PostgreSQL** - Test database with 1-minute backup schedule
- **MongoDB** - Test database with 1-minute backup schedule
- **MySQL** - Test database with 1-minute backup schedule
- **Redis** - Test database with 1-minute backup schedule

**Note**: R2 test uses external Cloudflare R2 storage (no local MinIO server).

## Test Coverage

The test suite validates:

- Health check endpoints (`/healthz`, `/readyz`, `/status`, `/metadata`)
- Database connection testing
- S3 backup functionality (MinIO and R2)
- Webhook notifications
- SIGHUP configuration reload
- Backup file generation
- Circuit breaker functionality
- Metadata and checksum validation
- Real SHA256 checksum calculation

## Access URLs

### MinIO Test Environment

- **Label Backup Status**: http://localhost:8080/status
- **MinIO Console**: http://localhost:9001 (minioadmin/minioadmin)
- **Webhook Test Server**: http://localhost:8081/health

### R2 Test Environment

- **Label Backup Status**: http://localhost:8080/status
- **Webhook Test Server**: http://localhost:8081/health

## Monitoring

View logs for different components:

```bash
# MinIO Test Environment
docker logs -f label_backup
docker logs -f webhook_test_server
docker logs -f minio_s3_server
docker logs -f test_postgres_db
docker logs -f test_mongo_db
docker logs -f test_mysql_db
docker logs -f test_redis_db

# R2 Test Environment
docker logs -f label_backup_r2
docker logs -f webhook_test_server_r2
docker logs -f test_postgres_db_r2
docker logs -f test_mongo_db_r2
docker logs -f test_mysql_db_r2
docker logs -f test_redis_db_r2
```

## Monitoring Tests

### Health Checks

```bash
# Basic health check
curl http://localhost:8080/healthz

# Readiness check
curl http://localhost:8080/readyz

# Detailed status
curl http://localhost:8080/status

# Metadata endpoint
curl "http://localhost:8080/metadata?object=test-mongo-backup/mongodb-testmongodb-20251024115200.dump.gz"
```

### Metadata API Tests

```bash
# Query backup metadata (replace with actual backup name)
curl "http://localhost:8080/metadata?object=test-mongo-backup/mongodb-testmongodb-20251024115200.dump.gz"

# Example response with real checksum:
{
  "timestamp": "2025-10-24T11:52:00.000931019Z",
  "container_id": "e618393030750cf5653113132d931a314068db38e0f4e1044ec01abf890c6465",
  "container_name": "test_mongo_db",
  "database_type": "mongodb",
  "database_name": "testmongodb",
  "backup_size_bytes": 116,
  "checksum": "cfc2f1c0d85b1d287b2ecf6e6d3e2a5a7804f5aa5ee02cf89878a7ca172384d7",
  "compression_type": "gzip",
  "version": "1.0",
  "destination": "http://minio:9000/test-bucket/test-mongo-backup/mongodb-testmongodb-20251024115200.dump.gz",
  "duration_seconds": 0.052040647,
  "success": true
}
```

### Backup File Verification

#### MinIO Test Environment

```bash
# List backup files in MinIO
docker exec minio_s3_server mc ls myminio/test-bucket/

# Check specific backup directory
docker exec minio_s3_server mc ls myminio/test-bucket/test-mongo-backup/

# Download and verify backup file
docker exec minio_s3_server mc cp myminio/test-bucket/test-mongo-backup/mongodb-testmongodb-20251024115200.dump.gz /tmp/
docker exec minio_s3_server gunzip -t /tmp/mongodb-testmongodb-20251024115200.dump.gz

# Verify checksum
docker exec minio_s3_server sha256sum /tmp/mongodb-testmongodb-20251024115200.dump.gz
```

#### R2 Test Environment

```bash
# Check backup logs for successful uploads
docker logs label_backup_r2 | grep "Successfully uploaded backup to S3"

# Example log entry:
# Successfully uploaded backup to S3 {"location": "https://...r2.cloudflarestorage.com/test-bucket/test-mongo-backup/mongodb-testmongodb-20251024115200.dump.gz", "bytesWritten": 116, "checksum": "cfc2f1c0d85b1d287b2ecf6e6d3e2a5a7804f5aa5ee02cf89878a7ca172384d7"}
```

## Manual Testing Steps

### 1. Start Test Environment

```bash
# MinIO test
docker-compose -f docker-compose-test.yml up -d

# R2 test
docker-compose -f docker-compose-r2-test.yml up -d
```

### 2. Wait for Services

```bash
# Check service status
docker-compose -f docker-compose-test.yml ps

# Wait for health checks
sleep 30
```

### 3. Test Health Endpoints

```bash
# Test all health endpoints
curl http://localhost:8080/healthz
curl http://localhost:8080/readyz
curl http://localhost:8080/status
```

### 4. Monitor Backup Process

```bash
# Watch backup logs
docker logs -f label_backup

# Look for these log entries:
# - "Starting backup job"
# - "Successfully uploaded backup to S3"
# - "Backup job finished processing"
```

### 5. Verify Backup Files

```bash
# MinIO: List files
docker exec minio_s3_server mc ls myminio/test-bucket/

# R2: Check logs for upload success
docker logs label_backup_r2 | grep "Successfully uploaded"
```

### 6. Test Metadata API

```bash
# Get backup metadata
curl "http://localhost:8080/metadata?object=test-mongo-backup/mongodb-testmongodb-20251024115200.dump.gz"
```

## Troubleshooting

If tests fail:

1. **Check service status**: `docker-compose -f docker-compose-test.yml ps`
2. **Verify Docker socket access**: `ls -la /var/run/docker.sock`
3. **Check for port conflicts** on 8080, 8081, 9000, 9001
4. **Review application logs** for specific errors
5. **Ensure sufficient disk space** for backups
6. **For R2 tests**: Verify credentials and bucket access
7. **Check network connectivity** between containers

### Common Issues

- **Docker socket permission denied**: Ensure running as root or add user to docker group
- **Port conflicts**: Stop other services using ports 8080, 8081, 9000, 9001
- **R2 connection failed**: Verify endpoint, credentials, and bucket configuration
- **Backup timeout**: Increase `BACKUP_TIMEOUT_MINUTES` if databases are slow

## Customization

To modify test parameters:

- **Environment variables**: Edit `docker-compose-test.yml` or `docker-compose-r2-test.yml`
- **Webhook configuration**: Modify `test-webhook.conf`
- **Test logic**: Update `test.sh` for test logic and timing
- **Backup schedules**: Change cron expressions in container labels
- **Retention policies**: Modify `GLOBAL_RETENTION_PERIOD` and container labels
