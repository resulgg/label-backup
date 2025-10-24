package writer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"label-backup/internal/logger"

	"go.uber.org/zap"
)

type BackupMetadata struct {
	Timestamp       time.Time `json:"timestamp"`
	ContainerID     string    `json:"container_id"`
	ContainerName   string    `json:"container_name"`
	DatabaseType    string    `json:"database_type"`
	DatabaseName    string    `json:"database_name,omitempty"`
	BackupSize      int64     `json:"backup_size_bytes"`
	Checksum        string    `json:"checksum,omitempty"`
	CompressionType string    `json:"compression_type"`
	Version         string    `json:"version"`
	Destination     string    `json:"destination"`
	DurationSeconds float64   `json:"duration_seconds"`
	Success         bool      `json:"success"`
	Error           string    `json:"error,omitempty"`
}

func WriteMetadata(ctx context.Context, writer BackupWriter, metadata BackupMetadata, objectName string) error {
	metadataName := objectName + ".metadata.json"
	
	jsonData, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal metadata: %w", err)
	}
	
	reader := io.NopCloser(strings.NewReader(string(jsonData)))
	
	_, _, _, err = writer.Write(ctx, metadataName, reader)
	if err != nil {
		return fmt.Errorf("failed to write metadata file: %w", err)
	}
	
	logger.Log.Debug("Backup metadata written successfully",
		zap.String("metadataFile", metadataName),
		zap.String("containerID", metadata.ContainerID),
	)
	
	return nil
}

func ReadMetadata(ctx context.Context, writer BackupWriter, objectName string) (*BackupMetadata, error) {
	metadataName := objectName + ".metadata.json"
	
	logger.Log.Debug("Reading backup metadata",
		zap.String("metadataFile", metadataName),
	)

	// Read metadata file content from writer
	reader, err := writer.ReadObject(ctx, metadataName)
	if err != nil {
		return nil, fmt.Errorf("failed to read metadata file: %w", err)
	}
	defer reader.Close()

	// Parse JSON metadata
	var metadata BackupMetadata
	if err := json.NewDecoder(reader).Decode(&metadata); err != nil {
		return nil, fmt.Errorf("failed to decode metadata JSON: %w", err)
	}

	logger.Log.Debug("Backup metadata read successfully",
		zap.String("metadataFile", metadataName),
		zap.String("containerID", metadata.ContainerID),
		zap.String("databaseType", metadata.DatabaseType),
		zap.Bool("success", metadata.Success),
	)

	return &metadata, nil
}
