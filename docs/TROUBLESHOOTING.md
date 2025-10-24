# Troubleshooting Guide

This guide helps you diagnose and resolve common issues with the Label Backup application.

## Common Issues

### 1. Backup Jobs Not Starting

**Symptoms:**

- No backup jobs are scheduled
- Containers are discovered but no cron jobs are created

**Diagnosis:**

```bash
# Check if containers have backup labels
docker ps --format "table {{.Names}}\t{{.Labels}}" | grep backup

# Check application logs
docker logs label-backup-container
```

**Solutions:**

- Ensure containers have `backup.enabled=true` label
- Verify `backup.cron` label has valid cron expression
- Check `backup.type` label matches supported types (postgres, mysql, mongodb, redis)
- Verify `backup.conn` label has valid connection string

### 2. Database Connection Failures

**Symptoms:**

- Backup jobs fail with connection errors
- "Database connection test failed" in logs

**Diagnosis:**

```bash
# Test database connection manually
docker exec -it postgres-container psql -h localhost -U user -d database -c "SELECT 1;"
```

**Solutions:**

- Verify connection string format
- Check database credentials
- Ensure database is accessible from backup container
- Test network connectivity between containers

### 3. S3 Upload Failures

**Symptoms:**

- "S3 upload failed" errors
- "S3 bucket does not exist" errors

**Diagnosis:**

```bash
# Check S3 configuration
echo $AWS_ACCESS_KEY_ID
echo $AWS_SECRET_ACCESS_KEY
echo $BUCKET_NAME
echo $AWS_REGION

# Test S3 access
aws s3 ls s3://your-bucket-name
```

**Solutions:**

- Verify AWS credentials are correct
- Check bucket name and region
- Ensure bucket exists and is accessible
- Verify IAM permissions for S3 operations

### 4. Disk Space Issues

**Symptoms:**

- "Insufficient disk space" errors
- Backup operations fail

**Diagnosis:**

```bash
# Check disk space
df -h /backups
du -sh /backups/*
```

**Solutions:**

- Free up disk space
- Increase disk size
- Configure retention policies to delete old backups
- Use S3 storage instead of local storage

### 6. Circuit Breaker Issues

**Symptoms:**

- Webhook notifications stop working
- "Circuit breaker is open" in logs
- No webhook retries attempted

**Diagnosis:**

```bash
# Check circuit breaker state in logs
docker logs label-backup-container 2>&1 | grep -i "circuit breaker"

# Monitor webhook attempts
docker logs label-backup-container 2>&1 | grep -i "webhook"
```

**Solutions:**

- Wait for automatic recovery (30 seconds timeout)
- Check webhook endpoint availability
- Verify webhook URL and credentials
- Restart application to reset circuit breaker state

### 7. Configuration Reload Issues

**Symptoms:**

- SIGHUP signal doesn't reload configuration
- Components not updated after reload
- Configuration changes not applied

**Diagnosis:**

```bash
# Test SIGHUP reload
docker kill -s HUP label-backup-container

# Check reload logs
docker logs label-backup-container 2>&1 | grep -i "reload\|sighup"
```

**Solutions:**

- Verify configuration syntax before reload
- Check for invalid environment variables
- Ensure all required components are properly configured
- Restart application if reload fails

### 5. Webhook Failures

**Symptoms:**

- No webhook notifications received
- "Webhook failed after all retries" in logs

**Diagnosis:**

```bash
# Test webhook endpoint
curl -X POST https://your-webhook-url.com/backup \
  -H "Content-Type: application/json" \
  -d '{"test": "message"}'
```

**Solutions:**

- Verify webhook URL is correct
- Check webhook endpoint is accessible
- Ensure webhook accepts POST requests
- Verify webhook secret if using HMAC signing

## Debugging Commands

### Check Application Status

```bash
# Health check
curl http://localhost:8080/healthz

# Readiness check
curl http://localhost:8080/readyz

# Detailed status
curl http://localhost:8080/status

# Query backup metadata
curl "http://localhost:8080/metadata?object=postgres-mydb-20240101-020000.sql.gz"
```

### View Logs

```bash
# Application logs
docker logs -f label-backup-container

# Filter for errors
docker logs label-backup-container 2>&1 | grep ERROR

# Filter for backup jobs
docker logs label-backup-container 2>&1 | grep "Backup job"
```

### Test Database Connections

```bash
# Test PostgreSQL connection
docker exec label-backup-container sh -c 'psql -h postgres -U user -d database -c "SELECT 1;"'

# Test MySQL connection
docker exec label-backup-container sh -c 'mysql -h mysql -u user -p database -e "SELECT 1;"'

# Test MongoDB connection
docker exec label-backup-container sh -c 'mongosh mongodb://user:pass@mongodb:27017/database --eval "db.runCommand({ping: 1})"'

# Test Redis connection
docker exec label-backup-container sh -c 'redis-cli -h redis ping'
```

### Monitor Circuit Breaker

```bash
# Check circuit breaker state
docker logs label-backup-container 2>&1 | grep -E "(circuit breaker|Circuit breaker)"

# Monitor webhook attempts
docker logs label-backup-container 2>&1 | grep -E "(webhook|Webhook)"
```

### Test Configuration Reload

```bash
# Send SIGHUP signal
docker kill -s HUP label-backup-container

# Verify reload success
docker logs label-backup-container 2>&1 | grep -E "(reload|SIGHUP|Configuration)"
```

## Performance Issues

### Slow Backups

- Check database performance
- Monitor disk I/O during backups
- Consider increasing `CONCURRENT_BACKUP_LIMIT`
- Use faster storage for backup destination

### High Memory Usage

- Monitor memory usage during backups
- Check for memory leaks in logs
- Consider reducing concurrent backups
- Restart application if memory usage grows continuously

## Recovery Procedures

### Restore from Backup

1. Stop the application
2. Restore backup files to appropriate location
3. Verify backup integrity
4. Start the application

### Reset Configuration

1. Stop the application
2. Remove environment variables
3. Restart with default configuration
4. Reconfigure as needed

### Emergency Shutdown

```bash
# Graceful shutdown
docker stop label-backup-container

# Force shutdown if needed
docker kill label-backup-container
```

## Getting Help

If you continue to experience issues:

1. Check the application logs for detailed error messages
2. Verify your configuration against the documentation
3. Test individual components (database, S3, webhook) separately
4. Check system resources (CPU, memory, disk space)
5. Review network connectivity between containers

## Metadata Issues

### Metadata Not Found

**Symptoms:**

- `404 Not Found` when querying metadata endpoint
- Metadata files missing from backup storage

**Diagnosis:**

```bash
# Check if metadata file exists
curl "http://localhost:8080/metadata?object=backup-name.dump.gz"

# List backup files in storage
# For S3/MinIO:
docker exec minio-container mc ls myminio/bucket-name/

# For local storage:
ls -la /backups/
```

**Solutions:**

- Verify backup object name is correct
- Check if backup was successful (metadata only created for successful backups)
- Ensure backup storage is accessible
- Check application logs for backup errors

### Metadata API Errors

**Symptoms:**

- `500 Internal Server Error` from metadata endpoint
- Invalid JSON responses

**Diagnosis:**

```bash
# Check application logs
docker logs label-backup-container 2>&1 | grep metadata

# Test with verbose curl
curl -v "http://localhost:8080/metadata?object=backup-name.dump.gz"
```

**Solutions:**

- Check writer configuration (S3 credentials, local path permissions)
- Verify backup storage connectivity
- Ensure metadata files are not corrupted
- Check application has read permissions to backup storage

For additional support, please provide:

- Application logs
- Configuration details (without sensitive information)
- System information
- Steps to reproduce the issue
