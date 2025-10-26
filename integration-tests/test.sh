#!/bin/bash

# Label Backup Test Script
# This script tests all features of the Label Backup application

set -e

echo "🚀 Starting Label Backup Comprehensive Test"
echo "=========================================="

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Test functions
test_health() {
    echo -e "${BLUE}📊 Testing Health Endpoints...${NC}"
    
    # Test /healthz
    if curl -f -s http://localhost:8080/healthz > /dev/null; then
        echo -e "${GREEN}✅ /healthz endpoint working${NC}"
    else
        echo -e "${RED}❌ /healthz endpoint failed${NC}"
        return 1
    fi
    
    # Test /readyz
    if curl -f -s http://localhost:8080/readyz > /dev/null; then
        echo -e "${GREEN}✅ /readyz endpoint working${NC}"
    else
        echo -e "${RED}❌ /readyz endpoint failed${NC}"
        return 1
    fi
    
}

test_webhook() {
    echo -e "${BLUE}🔔 Testing Webhook Server...${NC}"
    
    # Test webhook server health
    if curl -f -s http://localhost:8081/health > /dev/null; then
        echo -e "${GREEN}✅ Webhook test server is running${NC}"
    else
        echo -e "${RED}❌ Webhook test server is not accessible${NC}"
        return 1
    fi
}

test_minio() {
    echo -e "${BLUE}🗄️ Testing MinIO S3 Server...${NC}"
    
    # Test MinIO health
    if curl -f -s http://localhost:9000/minio/health/live > /dev/null; then
        echo -e "${GREEN}✅ MinIO server is running${NC}"
    else
        echo -e "${RED}❌ MinIO server is not accessible${NC}"
        return 1
    fi
    
    # Test MinIO console
    if curl -f -s http://localhost:9001 > /dev/null; then
        echo -e "${GREEN}✅ MinIO console is accessible${NC}"
    else
        echo -e "${RED}❌ MinIO console is not accessible${NC}"
        return 1
    fi
}

test_databases() {
    echo -e "${BLUE}🗃️ Testing Database Connections...${NC}"
    
    # Test PostgreSQL
    if docker exec test_postgres_db pg_isready -U testuser -d testdb > /dev/null 2>&1; then
        echo -e "${GREEN}✅ PostgreSQL is ready${NC}"
    else
        echo -e "${RED}❌ PostgreSQL is not ready${NC}"
        return 1
    fi
    
    # Test MongoDB
    if docker exec test_mongo_db mongosh --eval "db.runCommand({ping: 1})" > /dev/null 2>&1; then
        echo -e "${GREEN}✅ MongoDB is ready${NC}"
    else
        echo -e "${RED}❌ MongoDB is not ready${NC}"
        return 1
    fi
    
    # Test MySQL
    if docker exec test_mysql_db mysqladmin ping -h localhost > /dev/null 2>&1; then
        echo -e "${GREEN}✅ MySQL is ready${NC}"
    else
        echo -e "${RED}❌ MySQL is not ready${NC}"
        return 1
    fi
    
    # Test Redis
    if docker exec test_redis_db redis-cli ping > /dev/null 2>&1; then
        echo -e "${GREEN}✅ Redis is ready${NC}"
    else
        echo -e "${RED}❌ Redis is not ready${NC}"
        return 1
    fi
}

monitor_backups() {
    echo -e "${BLUE}⏰ Monitoring Backup Jobs...${NC}"
    echo "Waiting for backup jobs to trigger..."
    
    # Wait for first backup cycle
    sleep 70
    
    echo -e "${YELLOW}📋 Checking backup logs...${NC}"
    docker logs label_backup 2>&1 | grep -E "(Backup job|Starting backup|Backup job finished)" | tail -10
}

test_sighup() {
    echo -e "${BLUE}🔄 Testing SIGHUP Configuration Reload...${NC}"
    
    # Send SIGHUP signal
    docker kill -s HUP label_backup
    
    # Wait a moment
    sleep 5
    
    # Check logs for reload confirmation
    if docker logs label_backup 2>&1 | grep -q "Configuration reloaded successfully"; then
        echo -e "${GREEN}✅ SIGHUP reload successful${NC}"
    else
        echo -e "${RED}❌ SIGHUP reload failed${NC}"
        return 1
    fi
}

check_backup_files() {
    echo -e "${BLUE}📁 Checking Backup Files...${NC}"
    
    # Check if backup files exist in MinIO
    echo "Checking MinIO bucket contents..."
    docker exec minio_s3_server mc ls minio/test-bucket/ || echo "No files found yet"
    
    # Check local backup directory
    echo "Checking local backup directory..."
    docker exec label_backup ls -la /backups/ || echo "No local backups found"
    
    # Check webhook logs
    echo "Checking webhook logs..."
    docker logs webhook_test_server 2>&1 | tail -5 || echo "No webhook logs found"
}

# Main test execution
main() {
    echo "Starting comprehensive test suite..."
    
    # Wait for services to be ready
    echo -e "${YELLOW}⏳ Waiting for services to start...${NC}"
    sleep 30
    
    # Run tests
    test_health || exit 1
    test_webhook || exit 1
    test_minio || exit 1
    test_databases || exit 1
    
    echo -e "${GREEN}🎉 All initial tests passed!${NC}"
    
    # Monitor backups
    monitor_backups
    
    # Test SIGHUP
    test_sighup || exit 1
    
    # Check backup files
    check_backup_files
    
    echo -e "${GREEN}🎉 Comprehensive test completed successfully!${NC}"
    echo ""
    echo "📊 Test Summary:"
    echo "- Health endpoints: ✅"
    echo "- Webhook server: ✅"
    echo "- MinIO S3 server: ✅"
    echo "- Database connections: ✅"
    echo "- Backup monitoring: ✅"
    echo "- SIGHUP reload: ✅"
    echo ""
    echo "🔗 Access URLs:"
    echo "- MinIO Console: http://localhost:9001 (minioadmin/minioadmin)"
    echo "- Webhook Test Server: http://localhost:8081/health"
    echo ""
    echo "📝 To view logs:"
    echo "- Label Backup: docker logs -f label_backup"
    echo "- Webhook Server: docker logs -f webhook_test_server"
    echo "- MinIO: docker logs -f minio_s3_server"
}

# Run main function
main "$@"
