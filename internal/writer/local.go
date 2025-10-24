package writer

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"label-backup/internal/logger"
	"label-backup/internal/model"

	"go.uber.org/zap"
)

func CheckDiskSpace(path string) error {
	return checkDiskSpaceImpl(path)
}

const LocalWriterType = "local"

type LocalWriter struct {
	basePath string
}

func init() {
	RegisterWriterFactory(LocalWriterType, NewLocalWriter)
}

func NewLocalWriter(spec model.BackupSpec, globalConfig map[string]string) (BackupWriter, error) {
	basePath := DefaultLocalPath
	if configuredPath, ok := globalConfig[GlobalConfigKeyLocalPath]; ok && configuredPath != "" {
		basePath = configuredPath
	}
	
	if err := CheckDiskSpace(basePath); err != nil {
		logger.Log.Error("Insufficient disk space for local backups", zap.String("path", basePath), zap.Error(err))
		return nil, fmt.Errorf("disk space check failed: %w", err)
	}
	
	if err := os.MkdirAll(basePath, 0755); err != nil {
		logger.Log.Error("Failed to create local backup base path", zap.String("path", basePath), zap.Error(err))
		return nil, fmt.Errorf("failed to create local backup base path %s: %w", basePath, err)
	}
	logger.Log.Info("LocalWriter initialized", zap.String("basePath", basePath))
	return &LocalWriter{basePath: basePath}, nil
}

func (lw *LocalWriter) Type() string {
	return LocalWriterType
}

func (lw *LocalWriter) Write(ctx context.Context, objectName string, reader io.Reader) (destination string, bytesWritten int64, checksum string, err error) {
	if err := CheckDiskSpace(lw.basePath); err != nil {
		return "", 0, "", fmt.Errorf("disk space check failed before write: %w", err)
	}

	cleanedObjectName := strings.ReplaceAll(objectName, "\\", "/")
	cleanedObjectName = filepath.Clean(cleanedObjectName)
	if filepath.IsAbs(cleanedObjectName) || strings.HasPrefix(cleanedObjectName, "..") {
		logger.Log.Error("LocalWriter: Malformed objectName, potential path traversal",
			zap.String("originalObjectName", objectName),
			zap.String("cleanedObjectName", cleanedObjectName),
		)
		return "", 0, "", fmt.Errorf("malformed objectName: %s", objectName)
	}

	filePath := filepath.Join(lw.basePath, cleanedObjectName)

	absBasePath, errAbsBase := filepath.Abs(lw.basePath)
	if errAbsBase != nil {
		logger.Log.Error("LocalWriter: Could not get absolute path for basePath", zap.String("basePath", lw.basePath), zap.Error(errAbsBase))
		return "", 0, "", fmt.Errorf("could not determine absolute path for base: %w", errAbsBase)
	}
	absFilePath, errAbsFile := filepath.Abs(filePath)
	if errAbsFile != nil {
		logger.Log.Error("LocalWriter: Could not get absolute path for filePath", zap.String("filePath", filePath), zap.Error(errAbsFile))
		return "", 0, "", fmt.Errorf("could not determine absolute path for target: %w", errAbsFile)
	}

	if !strings.HasPrefix(absFilePath, absBasePath) {
		logger.Log.Error("LocalWriter: Target filePath is outside basePath, aborting write",
			zap.String("filePath", filePath),
			zap.String("absFilePath", absFilePath),
			zap.String("basePath", lw.basePath),
			zap.String("absBasePath", absBasePath),
		)
		return "", 0, "", fmt.Errorf("target filePath %s is outside basePath %s", absFilePath, absBasePath)
	}

	if errMkdir := os.MkdirAll(filepath.Dir(filePath), 0755); errMkdir != nil {
		logger.Log.Error("Failed to create directory for local backup file", zap.String("path", filePath), zap.Error(errMkdir))
		return "", 0, "", fmt.Errorf("failed to create directory for local backup file %s: %w", filePath, errMkdir)
	}

	file, errCreate := os.Create(filePath)
	if errCreate != nil {
		logger.Log.Error("Failed to create local backup file", zap.String("path", filePath), zap.Error(errCreate))
		return "", 0, "", fmt.Errorf("failed to create local backup file %s: %w", filePath, errCreate)
	}
	defer file.Close()

	// Calculate checksum while writing
	hash := sha256.New()
	teeReader := io.TeeReader(reader, hash)
	
	bytesWritten, errCopy := io.Copy(file, teeReader)
	if errCopy != nil {
		if removeErr := os.Remove(filePath); removeErr != nil {
			logger.Log.Error("Failed to remove partial backup file", zap.String("path", filePath), zap.Error(removeErr))
		}
		logger.Log.Error("Failed to write backup data to local file", zap.String("path", filePath), zap.Error(errCopy))
		return "", 0, "", fmt.Errorf("failed to write backup data to %s: %w", filePath, errCopy)
	}

	checksum = fmt.Sprintf("%x", hash.Sum(nil))
	logger.Log.Info("Successfully wrote to local backup", 
		zap.Int64("bytesWritten", bytesWritten), 
		zap.String("path", filePath),
		zap.String("checksum", checksum),
	)
	return filePath, bytesWritten, checksum, nil
}

func (lw *LocalWriter) ListObjects(ctx context.Context, prefix string) ([]BackupObjectMeta, error) {
	var objects []BackupObjectMeta
	scanPath := lw.basePath
	if prefix != "" {
		scanPath = filepath.Join(lw.basePath, prefix)
	}

	info, err := os.Stat(scanPath)
	if err != nil {
		if os.IsNotExist(err) {
			logger.Log.Debug("LocalWriter:ListObjects: Path does not exist, returning empty list.", zap.String("path", scanPath))
			return objects, nil
		}
		logger.Log.Error("Failed to stat path for listing", zap.String("path", scanPath), zap.Error(err))
		return nil, fmt.Errorf("failed to stat path %s for listing: %w", scanPath, err)
	}
	if !info.IsDir() {
		logger.Log.Debug("LocalWriter:ListObjects: Path is a file, not a directory. Returning empty list for prefix scan.", zap.String("path", scanPath))
		return objects, nil
	}

	err = filepath.Walk(scanPath, func(path string, info os.FileInfo, errWalk error) error {
		if errWalk != nil {
			logger.Log.Error("Error during filepath.Walk for ListObjects", zap.String("path", path), zap.Error(errWalk))
			return errWalk
		}
		if ctx.Err() != nil { 
		    logger.Log.Warn("Context cancelled during ListObjects walk", zap.Error(ctx.Err()))
		    return ctx.Err()
		}
		if !info.IsDir() {
			relKey, errRel := filepath.Rel(lw.basePath, path)
			if errRel != nil {
				logger.Log.Error("Error creating relative path for local object", zap.String("path", path), zap.String("basePath", lw.basePath), zap.Error(errRel))
				return errRel
			}
			relKey = filepath.ToSlash(relKey)

			if prefix != "" && !strings.HasPrefix(relKey, strings.Trim(filepath.ToSlash(prefix), "/")+"/") {
				return nil
			}

			objects = append(objects, BackupObjectMeta{
				Key:          relKey,
				LastModified: info.ModTime(),
				Size:         info.Size(),
			})
		}
		return nil
	})

	if err != nil {
	    if err == context.Canceled || err == context.DeadlineExceeded {
	        logger.Log.Warn("Local listing cancelled or timed out", zap.String("prefix", prefix), zap.Error(err))
	        return nil, err
	    }
		logger.Log.Error("Failed to walk local path for ListObjects", zap.String("scanPath", scanPath), zap.Error(err))
		return nil, fmt.Errorf("failed to walk local path %s: %w", scanPath, err)
	}
	return objects, nil
}

func (lw *LocalWriter) ReadObject(ctx context.Context, objectName string) (io.ReadCloser, error) {
	filePath := filepath.Join(lw.basePath, filepath.FromSlash(objectName))
	
	logger.Log.Debug("LocalWriter: Reading file", 
		zap.String("filePath", filePath), 
		zap.String("objectName", objectName),
	)

	// Security check: ensure file is within base path
	absBasePath, errAbsBase := filepath.Abs(lw.basePath)
	if errAbsBase != nil {
		return nil, fmt.Errorf("failed to get absolute path for base path %s: %w", lw.basePath, errAbsBase)
	}
	
	absFilePath, errAbsFile := filepath.Abs(filePath)
	if errAbsFile != nil {
		return nil, fmt.Errorf("failed to get absolute path for target file %s: %w", filePath, errAbsFile)
	}

	if !strings.HasPrefix(absFilePath, absBasePath) {
		return nil, fmt.Errorf("read path %s (abs: %s) is outside base path %s (abs: %s), aborting", filePath, absFilePath, lw.basePath, absBasePath)
	}

	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file %s: %w", filePath, err)
	}

	return file, nil
}

func (lw *LocalWriter) DeleteObject(ctx context.Context, key string) error {
	filePath := filepath.Join(lw.basePath, filepath.FromSlash(key))

	logger.Log.Info("LocalWriter: Attempting to delete local file", zap.String("filePath", filePath), zap.String("originalKey", key))

	absBasePath, errAbsBase := filepath.Abs(lw.basePath)
	if errAbsBase != nil {
		logger.Log.Error("Failed to get absolute path for base path", zap.String("basePath", lw.basePath), zap.Error(errAbsBase))
		return fmt.Errorf("failed to get absolute path for base path %s: %w", lw.basePath, errAbsBase)
	}
	
	absFilePath, errAbsFile := filepath.Abs(filePath)
	if errAbsFile != nil {
		logger.Log.Error("Failed to get absolute path for target file", zap.String("filePath", filePath), zap.Error(errAbsFile))
		return fmt.Errorf("failed to get absolute path for target file %s: %w", filePath, errAbsFile)
	}

	if !strings.HasPrefix(absFilePath, absBasePath) {
		    logger.Log.Error("Delete path is outside base path, aborting", 
		        zap.String("filePath", filePath), 
			zap.String("absFilePath", absFilePath), 
		        zap.String("basePath", lw.basePath), 
		        zap.String("absBasePath", absBasePath),
		    )
		return fmt.Errorf("delete path %s (abs: %s) is outside base path %s (abs: %s), aborting", filePath, absFilePath, lw.basePath, absBasePath)
	}

	errDel := os.Remove(filePath)
	if errDel != nil {
		if os.IsNotExist(errDel) {
			logger.Log.Info("Local file not found for deletion, considering as success.", zap.String("filePath", filePath))
			return nil
		}
		logger.Log.Error("Failed to delete local file", zap.String("filePath", filePath), zap.Error(errDel))
		return fmt.Errorf("failed to delete local file %s: %w", filePath, errDel)
	}
	logger.Log.Info("Successfully deleted local file", zap.String("filePath", filePath))
	return nil
} 