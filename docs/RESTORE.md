# Backup Restore Procedures

This guide explains how to restore backups created by the Label Backup application.

## Overview

The Label Backup application creates compressed backup files in the following formats:

- PostgreSQL: `.sql.gz` files
- MySQL: `.sql.gz` files
- MongoDB: `.bson.gz` files
- Redis: `.rdb.gz` files

## Backup Metadata

Each backup includes a `.metadata.json` file with detailed information:

```json
{
  "timestamp": "2024-01-01T02:00:00Z",
  "container_id": "abc123def456",
  "container_name": "postgres",
  "database_type": "postgres",
  "database_name": "myapp",
  "backup_size": 1048576,
  "checksum": "sha256:abc123...",
  "compression_type": "gzip",
  "version": "1.0",
  "destination": "/backups/postgres-mydb-20240101-020000.sql.gz",
  "duration_seconds": 45.2,
  "success": true,
  "error": ""
}
```

### Reading Metadata

**Via API (Recommended):**

```bash
# Query metadata for a specific backup via HTTP API
curl "http://localhost:8080/metadata?object=postgres-mydb-20240101-020000.sql.gz"

# Example response:
{
  "timestamp": "2024-01-01T02:00:00Z",
  "container_id": "abc123def456",
  "container_name": "postgres",
  "database_type": "postgres",
  "database_name": "myapp",
  "backup_size_bytes": 1048576,
  "checksum": "calculated-during-write",
  "compression_type": "gzip",
  "version": "1.0",
  "destination": "/backups/postgres-mydb-20240101-020000.sql.gz",
  "duration_seconds": 45.2,
  "success": true
}
```

**Via Local Files:**

```bash
# View metadata for a specific backup
cat postgres-mydb-20240101-020000.metadata.json | jq '.'

# Extract specific information
cat postgres-mydb-20240101-020000.metadata.json | jq '.backup_size_bytes, .duration_seconds, .success'

# List all metadata files
ls -la *.metadata.json
```

### Checksum Verification

```bash
# Verify backup integrity using SHA256 checksum
sha256sum postgres-mydb-20240101-020000.sql.gz

# Compare with metadata checksum
METADATA_CHECKSUM=$(cat postgres-mydb-20240101-020000.metadata.json | jq -r '.checksum')
CALCULATED_CHECKSUM=$(sha256sum postgres-mydb-20240101-020000.sql.gz | cut -d' ' -f1)

if [ "$METADATA_CHECKSUM" = "sha256:$CALCULATED_CHECKSUM" ]; then
  echo "Backup integrity verified"
else
  echo "Backup integrity check failed"
fi
```

## Prerequisites

Before restoring a backup, ensure you have:

- Access to the backup files
- Appropriate database client tools installed
- Sufficient disk space for the restore operation
- Database credentials and connection information

## PostgreSQL Restore

### 1. Locate Backup Files

```bash
# List available PostgreSQL backups
ls -la /backups/postgres-*.sql.gz
ls -la /backups/postgres-*.metadata.json
```

### 2. Extract and Restore

```bash
# Extract the backup
gunzip -c postgres-mydb-20240101-020000.sql.gz > postgres-mydb-20240101-020000.sql

# Restore to database
psql -h localhost -U username -d database_name < postgres-mydb-20240101-020000.sql
```

### 3. Verify Restore

```bash
# Check database contents
psql -h localhost -U username -d database_name -c "SELECT COUNT(*) FROM your_table;"
```

## MySQL Restore

### 1. Locate Backup Files

```bash
# List available MySQL backups
ls -la /backups/mysql-*.sql.gz
ls -la /backups/mysql-*.metadata.json
```

### 2. Extract and Restore

```bash
# Extract the backup
gunzip -c mysql-mydb-20240101-020000.sql.gz > mysql-mydb-20240101-020000.sql

# Restore to database
mysql -h localhost -u username -p database_name < mysql-mydb-20240101-020000.sql
```

### 3. Verify Restore

```bash
# Check database contents
mysql -h localhost -u username -p database_name -e "SELECT COUNT(*) FROM your_table;"
```

## MongoDB Restore

### 1. Locate Backup Files

```bash
# List available MongoDB backups
ls -la /backups/mongodb-*.bson.gz
ls -la /backups/mongodb-*.metadata.json
```

### 2. Extract and Restore

```bash
# Extract the backup
gunzip -c mongodb-mydb-20240101-020000.bson.gz > mongodb-mydb-20240101-020000.bson

# Restore to database
mongorestore --host localhost:27017 --db database_name --collection collection_name mongodb-mydb-20240101-020000.bson
```

### 3. Verify Restore

```bash
# Check database contents
mongo localhost:27017/database_name --eval "db.collection_name.count()"
```

## Redis Restore

### 1. Locate Backup Files

```bash
# List available Redis backups
ls -la /backups/redis-*.rdb.gz
ls -la /backups/redis-*.metadata.json
```

### 2. Extract and Restore

```bash
# Extract the backup
gunzip -c redis-default-20240101-020000.rdb.gz > redis-default-20240101-020000.rdb

# Stop Redis service
sudo systemctl stop redis

# Replace RDB file
sudo cp redis-default-20240101-020000.rdb /var/lib/redis/dump.rdb

# Start Redis service
sudo systemctl start redis
```

### 3. Verify Restore

```bash
# Check Redis contents
redis-cli -h localhost -p 6379 INFO keyspace
```

## S3 Backups Restore

### 1. Download Backup Files

```bash
# List S3 backups
aws s3 ls s3://your-bucket-name/backups/

# Download backup files
aws s3 cp s3://your-bucket-name/backups/postgres-mydb-20240101-020000.sql.gz ./
aws s3 cp s3://your-bucket-name/backups/postgres-mydb-20240101-020000.metadata.json ./
```

### 2. Follow Database-Specific Restore Steps

Use the same restore procedures as above after downloading the files.

## Metadata Files

Each backup includes a `.metadata.json` file with information about the backup:

```json
{
  "timestamp": "2024-01-01T02:00:00Z",
  "container_id": "abc123def456",
  "container_name": "postgres-container",
  "database_type": "postgres",
  "database_name": "mydb",
  "backup_size_bytes": 1048576,
  "checksum": "sha256:abc123...",
  "compression_type": "gzip",
  "version": "1.0",
  "destination": "/backups/postgres-mydb-20240101-020000.sql.gz",
  "duration_seconds": 30.5,
  "success": true,
  "error": ""
}
```

## Best Practices

### Before Restore

1. **Backup Current Data**: Always backup current data before restoring
2. **Test Restore**: Test restore procedures in a non-production environment
3. **Check Compatibility**: Ensure backup is compatible with target database version
4. **Verify Integrity**: Check backup file integrity using checksums

### During Restore

1. **Stop Applications**: Stop applications that might interfere with the restore
2. **Monitor Resources**: Monitor disk space and system resources
3. **Check Logs**: Monitor database logs for errors during restore
4. **Validate Data**: Verify data integrity after restore completion

### After Restore

1. **Update Applications**: Update application configurations if needed
2. **Test Functionality**: Test application functionality with restored data
3. **Monitor Performance**: Monitor system performance after restore
4. **Document Changes**: Document any changes made during restore

## Best Practices

### Pre-Restore Checklist

1. **Verify Backup Integrity**

   ```bash
   # Check metadata for success status
   cat backup-file.metadata.json | jq '.success'

   # Verify checksum
   sha256sum backup-file.sql.gz
   ```

2. **Test Restore Process**

   - Always test restore on a non-production environment first
   - Verify data integrity after restore
   - Check application functionality

3. **Backup Current State**
   - Create a backup of current database before restore
   - Document current configuration and data

### Restore Strategies

#### Point-in-Time Recovery

- Use the most recent successful backup before the issue
- Check metadata timestamps to find the right backup
- Verify backup was created before data loss occurred

#### Selective Restore

- Restore only specific tables or collections
- Use database-specific tools for partial restores
- Document what was restored for audit purposes

#### Full System Restore

- Restore entire database from backup
- Update application configuration if needed
- Verify all services are working correctly

## Troubleshooting

### Common Issues

**Permission Denied**

```bash
# Fix file permissions
chmod 644 backup-file.sql.gz
chown postgres:postgres backup-file.sql.gz
```

**Insufficient Disk Space**

```bash
# Check available space
df -h
# Free up space or use temporary location
```

**Database Connection Failed**

```bash
# Check database service
sudo systemctl status postgresql
# Verify connection parameters
psql -h localhost -U username -d database_name -c "SELECT 1;"
```

**Corrupted Backup File**

```bash
# Check file integrity
gunzip -t backup-file.sql.gz
# Try to extract and check for errors
gunzip -c backup-file.sql.gz | head -10
```

## Automation

### Restore Script Example

```bash
#!/bin/bash
# restore-backup.sh

BACKUP_FILE="$1"
DATABASE_NAME="$2"
DB_TYPE="$3"

if [ -z "$BACKUP_FILE" ] || [ -z "$DATABASE_NAME" ] || [ -z "$DB_TYPE" ]; then
    echo "Usage: $0 <backup_file> <database_name> <db_type>"
    exit 1
fi

case $DB_TYPE in
    postgres)
        gunzip -c "$BACKUP_FILE" | psql -h localhost -U postgres -d "$DATABASE_NAME"
        ;;
    mysql)
        gunzip -c "$BACKUP_FILE" | mysql -h localhost -u root -p "$DATABASE_NAME"
        ;;
    *)
        echo "Unsupported database type: $DB_TYPE"
        exit 1
        ;;
esac

echo "Restore completed successfully"
```

### Usage

```bash
chmod +x restore-backup.sh
./restore-backup.sh postgres-mydb-20240101-020000.sql.gz mydb postgres
```
