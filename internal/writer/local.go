package writer

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"label-backup/internal/logger"
	"label-backup/internal/model"

	"go.uber.org/zap"
)

const LocalWriterType = "local"
// DefaultLocalPath and GlobalConfigKeyLocalPath are defined in writer.go

// LocalWriter implements BackupWriter for saving to the local filesystem.
type LocalWriter struct {
	basePath string
}

func init() {
	RegisterWriterFactory(LocalWriterType, NewLocalWriter)
}

// NewLocalWriter creates a new LocalWriter.
// It expects a base path from globalConfig (e.g., "LOCAL_BACKUP_PATH") or defaults to "/backups".
func NewLocalWriter(spec model.BackupSpec, globalConfig map[string]string) (BackupWriter, error) {
	basePath := DefaultLocalPath
	if configuredPath, ok := globalConfig[GlobalConfigKeyLocalPath]; ok && configuredPath != "" {
		basePath = configuredPath
	}
	if err := os.MkdirAll(basePath, 0755); err != nil {
		logger.Log.Error("Failed to create local backup base path", zap.String("path", basePath), zap.Error(err))
		return nil, fmt.Errorf("failed to create local backup base path %s: %w", basePath, err)
	}
	logger.Log.Info("LocalWriter initialized", zap.String("basePath", basePath))
	return &LocalWriter{basePath: basePath}, nil
}

// Type returns the type of the writer.
func (lw *LocalWriter) Type() string {
	return LocalWriterType
}

// Write saves the backup data from the reader to a local file.
// objectName is used to construct the final file path relative to the basePath.
func (lw *LocalWriter) Write(ctx context.Context, objectName string, reader io.Reader) (destination string, bytesWritten int64, err error) {
	// Clean the objectName to prevent path traversal issues with Join alone.
	// Replace backslashes for consistency and remove leading/trailing slashes and dots.
	// This is a basic sanitization; more robust library might be used for production.
	cleanedObjectName := strings.ReplaceAll(objectName, "\\", "/")
	cleanedObjectName = filepath.Clean(cleanedObjectName) // Resolves "..", multiple slashes etc.
	// Prevent writing to absolute paths or paths starting with "../" after cleaning.
	if filepath.IsAbs(cleanedObjectName) || strings.HasPrefix(cleanedObjectName, "..") {
		logger.Log.Error("LocalWriter: Malformed objectName, potential path traversal",
			zap.String("originalObjectName", objectName),
			zap.String("cleanedObjectName", cleanedObjectName),
		)
		return "", 0, fmt.Errorf("malformed objectName: %s", objectName)
	}

	filePath := filepath.Join(lw.basePath, cleanedObjectName)

	// Security check: Ensure the final resolved path is within the basePath.
	absBasePath, errAbsBase := filepath.Abs(lw.basePath)
	if errAbsBase != nil {
		logger.Log.Error("LocalWriter: Could not get absolute path for basePath", zap.String("basePath", lw.basePath), zap.Error(errAbsBase))
		return "", 0, fmt.Errorf("could not determine absolute path for base: %w", errAbsBase)
	}
	absFilePath, errAbsFile := filepath.Abs(filePath)
	if errAbsFile != nil {
		logger.Log.Error("LocalWriter: Could not get absolute path for filePath", zap.String("filePath", filePath), zap.Error(errAbsFile))
		return "", 0, fmt.Errorf("could not determine absolute path for target: %w", errAbsFile)
	}

	if !strings.HasPrefix(absFilePath, absBasePath) {
		logger.Log.Error("LocalWriter: Target filePath is outside basePath, aborting write",
			zap.String("filePath", filePath),
			zap.String("absFilePath", absFilePath),
			zap.String("basePath", lw.basePath),
			zap.String("absBasePath", absBasePath),
		)
		return "", 0, fmt.Errorf("target filePath %s is outside basePath %s", absFilePath, absBasePath)
	}

	if errMkdir := os.MkdirAll(filepath.Dir(filePath), 0755); errMkdir != nil {
		logger.Log.Error("Failed to create directory for local backup file", zap.String("path", filePath), zap.Error(errMkdir))
		return "", 0, fmt.Errorf("failed to create directory for local backup file %s: %w", filePath, errMkdir)
	}

	file, errCreate := os.Create(filePath)
	if errCreate != nil {
		logger.Log.Error("Failed to create local backup file", zap.String("path", filePath), zap.Error(errCreate))
		return "", 0, fmt.Errorf("failed to create local backup file %s: %w", filePath, errCreate)
	}
	defer file.Close()

	bytesWritten, errCopy := io.Copy(file, reader)
	if errCopy != nil {
		os.Remove(filePath) // Attempt to remove partially written file on error
		logger.Log.Error("Failed to write backup data to local file", zap.String("path", filePath), zap.Error(errCopy))
		return "", 0, fmt.Errorf("failed to write backup data to %s: %w", filePath, errCopy)
	}

	logger.Log.Info("Successfully wrote to local backup", zap.Int64("bytesWritten", bytesWritten), zap.String("path", filePath))
	return filePath, bytesWritten, nil
}

// ListObjects lists backup files from the local filesystem, matching the given prefix.
func (lw *LocalWriter) ListObjects(ctx context.Context, prefix string) ([]BackupObjectMeta, error) {
	var objects []BackupObjectMeta
	scanPath := lw.basePath
	if prefix != "" {
		scanPath = filepath.Join(lw.basePath, prefix)
	}

	// Ensure scanPath is a directory and exists, otherwise return empty list or error
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
			    // This check is tricky due to how Walk works with the starting path.
			    // If scanPath = basePath/prefix, then relKey should naturally be prefix/filename.
			    // This might be redundant if Walk is always inside the prefixed dir.
			    // A simpler way is to check if path is within filepath.Join(lw.basePath, prefix)
			    // The filepath.Walk will only iterate files under scanPath.
			    // So, we just need to make sure we are creating the key correctly.
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

// DeleteObject deletes a file from the local filesystem.
// The key is expected to be relative to the writer's basePath.
func (lw *LocalWriter) DeleteObject(ctx context.Context, key string) error {
	filePath := filepath.Join(lw.basePath, filepath.FromSlash(key))

	logger.Log.Info("LocalWriter: Attempting to delete local file", zap.String("filePath", filePath), zap.String("originalKey", key))

	absBasePath, _ := filepath.Abs(lw.basePath)
	absFilePath, _ := filepath.Abs(filePath)
	// Re-check absolute path conversion as it might fail and return empty strings
	if absBasePath == "" || absFilePath == "" {
	    logger.Log.Error("Failed to get absolute paths for deletion check", zap.String("filePath", filePath), zap.String("basePath", lw.basePath))
	    return fmt.Errorf("failed to get absolute paths for %s or %s", filePath, lw.basePath)
	}

	if !strings.HasPrefix(absFilePath, absBasePath) {
		currentAbsFilePath, errPath := filepath.Abs(filePath)
		if errPath != nil {
		    logger.Log.Error("Could not determine absolute path for deletion target", zap.String("filePath", filePath), zap.Error(errPath))
		    return fmt.Errorf("could not determine absolute path for %s: %w", filePath, errPath)
		}
		if !strings.HasPrefix(currentAbsFilePath, absBasePath) {
		    logger.Log.Error("Delete path is outside base path, aborting", 
		        zap.String("filePath", filePath), 
		        zap.String("absFilePath", currentAbsFilePath), 
		        zap.String("basePath", lw.basePath), 
		        zap.String("absBasePath", absBasePath),
		    )
		    return fmt.Errorf("delete path %s (abs: %s) is outside base path %s (abs: %s), aborting", filePath, currentAbsFilePath, lw.basePath, absBasePath)
		}
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