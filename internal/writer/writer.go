package writer

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"label-backup/internal/logger"
	"label-backup/internal/model"

	"go.uber.org/zap"
)

const (
	// GlobalConfigKeyS3Bucket is the key for the S3 bucket name in global config.
	GlobalConfigKeyS3Bucket = "BUCKET_NAME"
	// GlobalConfigKeyS3Region is the key for the AWS S3 region in global config.
	GlobalConfigKeyS3Region = "REGION"
	// GlobalConfigKeyS3Endpoint is the key for the S3-compatible endpoint in global config.
	GlobalConfigKeyS3Endpoint = "ENDPOINT"
	// GlobalConfigKeyS3AccessKeyID is the key for the S3 access key ID in global config.
	GlobalConfigKeyS3AccessKeyID = "ACCESS_KEY_ID"
	// GlobalConfigKeyS3SecretAccessKey is the key for the S3 secret access key in global config.
	GlobalConfigKeyS3SecretAccessKey = "SECRET_ACCESS_KEY"
	// GlobalConfigKeyLocalPath is the key for the local backup path in global config.
	GlobalConfigKeyLocalPath = "LOCAL_BACKUP_PATH"
	// DefaultLocalPath is the default path for local backups if not overridden.
	DefaultLocalPath = "/backups"
)

// BackupObjectMeta holds metadata about a stored backup object.
type BackupObjectMeta struct {
	Key          string    // Full path/key of the object
	LastModified time.Time // Last modified timestamp
	Size         int64     // Size in bytes
}

// BackupWriter defines the interface for writing backup data to a destination.
type BackupWriter interface {
	// Write takes the backup data from the reader and writes it to the destination.
	// objectName is the suggested name for the backup object (e.g., prefix/dbname-timestamp.sql.gz).
	// reader provides the gzipped backup data.
	// Returns final path/URL, number of bytes written, and an error if any.
	Write(ctx context.Context, objectName string, reader io.Reader) (destination string, bytesWritten int64, err error)
	// Type returns the type of the writer (e.g., "local", "s3")
	Type() string

	// ListObjects lists backup objects, optionally filtered by a prefix.
	// The prefix corresponds to spec.Prefix.
	ListObjects(ctx context.Context, prefix string) ([]BackupObjectMeta, error)

	// DeleteObject deletes a backup object by its key.
	DeleteObject(ctx context.Context, key string) error
}

// NewWriterFunc is a function type that creates a new BackupWriter.
type NewWriterFunc func(spec model.BackupSpec, globalConfig map[string]string) (BackupWriter, error)

var writerFactories = make(map[string]NewWriterFunc)

// RegisterWriterFactory allows different writer implementations to register themselves.
func RegisterWriterFactory(destType string, factory NewWriterFunc) {
	if factory == nil {
		logger.Log.Fatal("Writer factory is nil", zap.String("destType", destType))
	}
	if _, ok := writerFactories[destType]; ok {
		logger.Log.Fatal("Writer factory already registered", zap.String("destType", destType))
	}
	writerFactories[destType] = factory
	logger.Log.Info("Registered writer factory", zap.String("destType", destType))
}

// GetWriter returns a BackupWriter for the given destination type from the BackupSpec.
// globalConfig can provide S3 bucket names, local paths, etc.
func GetWriter(spec model.BackupSpec, globalConfig map[string]string) (BackupWriter, error) {
	destType := strings.ToLower(spec.Dest)
	if destType == "" {
		destType = "local" // Default to local if not specified
		logger.Log.Debug("Destination type not specified, defaulting to local",
			zap.String("containerID", spec.ContainerID),
		)
	}

	factory, ok := writerFactories[destType]
	if !ok {
		err := fmt.Errorf("no writer registered for destination type: %s", destType)
		logger.Log.Error("Failed to get writer: no factory registered",
			zap.String("destType", destType),
			zap.String("containerID", spec.ContainerID),
			zap.Error(err),
		)
		return nil, err
	}
	return factory(spec, globalConfig)
}

// GenerateObjectName creates a standardized object name for the backup.
// Example: my-prefix/postgres-mydb-20230101150405.sql.gz
func GenerateObjectName(spec model.BackupSpec) string {
	timestamp := time.Now().UTC().Format("20060102150405")
	var dbNamePart string
	if spec.Database != "" {
		dbNamePart = spec.Database
	} else {
		// Try to derive from Conn string as a fallback (very basic)
		// This is not robust. A better way would be to have the dumper return the effective DB name.
		lastSlash := strings.LastIndex(spec.Conn, "/")
		lastQ := strings.LastIndex(spec.Conn, "?")
		if lastSlash != -1 {
			if lastQ != -1 && lastQ > lastSlash {
				dbNamePart = spec.Conn[lastSlash+1 : lastQ]
			} else {
				dbNamePart = spec.Conn[lastSlash+1:]
			}
		} else {
			dbNamePart = "database"
		}
	}
	// Sanitize dbNamePart (replace non-alphanum with underscore)
	dbNamePart = strings.ReplaceAll(dbNamePart, ":", "_") // common in host:port/db
	dbNamePart = strings.Map(func(r rune) rune {
        if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '.' {
            return r
        }
        return '_'
    }, dbNamePart)

	fileName := fmt.Sprintf("%s-%s-%s.dump.gz", spec.Type, dbNamePart, timestamp)

	if spec.Prefix != "" {
		return fmt.Sprintf("%s/%s", strings.Trim(spec.Prefix, "/"), fileName)
	}
	return fileName
} 