package writer

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"strings"
	"time"

	"label-backup/internal/logger"
	"label-backup/internal/model"

	"go.uber.org/zap"
)

const (
	GlobalConfigKeyS3Bucket = "BUCKET_NAME"
	GlobalConfigKeyS3Region = "REGION"
	GlobalConfigKeyS3Endpoint = "ENDPOINT"
	GlobalConfigKeyS3AccessKeyID = "ACCESS_KEY_ID"
	GlobalConfigKeyS3SecretAccessKey = "SECRET_ACCESS_KEY"
	GlobalConfigKeyLocalPath = "LOCAL_BACKUP_PATH"
	DefaultLocalPath = "/backups"
)

type BackupObjectMeta struct {
	Key          string
	LastModified time.Time
	Size         int64
	Checksum     string
}

type BackupWriter interface {
	Write(ctx context.Context, objectName string, reader io.Reader) (destination string, bytesWritten int64, checksum string, err error)
	Type() string

	ListObjects(ctx context.Context, prefix string) ([]BackupObjectMeta, error)
	ReadObject(ctx context.Context, objectName string) (io.ReadCloser, error)

	DeleteObject(ctx context.Context, key string) error
}

type NewWriterFunc func(spec model.BackupSpec, globalConfig map[string]string) (BackupWriter, error)

var writerFactories = make(map[string]NewWriterFunc)

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

func GetWriter(spec model.BackupSpec, globalConfig map[string]string) (BackupWriter, error) {
	destType := strings.ToLower(spec.Dest)
	if destType == "" {
		destType = "local"
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

func GenerateObjectName(spec model.BackupSpec) string {
	timestamp := time.Now().UTC().Format("20060102150405")
	var dbNamePart string
	if spec.Database != "" {
		dbNamePart = spec.Database
	} else {
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
	dbNamePart = strings.ReplaceAll(dbNamePart, ":", "_") 
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

func ValidateBackup(ctx context.Context, reader io.Reader) (string, error) {
	// Read and validate gzip header
	header := make([]byte, 3)
	n, err := reader.Read(header)
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("failed to read backup header: %w", err)
	}
	
	if n < 2 || header[0] != 0x1f || header[1] != 0x8b {
		return "", fmt.Errorf("invalid gzip header: expected magic bytes 0x1f8b, got %x", header[:n])
	}
	
	// Calculate SHA256 checksum of the entire backup
	hash := sha256.New()
	
	// Write the header bytes we already read
	if n > 0 {
		hash.Write(header[:n])
	}
	
	// Read and hash the rest of the stream
	_, err = io.Copy(hash, reader)
	if err != nil {
		return "", fmt.Errorf("failed to read backup data for checksum: %w", err)
	}
	
	checksum := fmt.Sprintf("%x", hash.Sum(nil))
	logger.Log.Debug("Backup validation successful", zap.String("checksum", checksum))
	return checksum, nil
} 