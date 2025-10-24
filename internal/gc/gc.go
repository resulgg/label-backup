package gc

import (
	"context"
	"fmt"
	"time"

	"label-backup/internal/logger"
	"label-backup/internal/model"
	"label-backup/internal/writer"

	"go.uber.org/zap"
)

type Runner struct {
	spec              model.BackupSpec
	backupWriter      writer.BackupWriter
	effectiveRetention time.Duration
	dryRun            bool
}

func NewRunner(spec model.BackupSpec, bw writer.BackupWriter, globalRetentionPeriod time.Duration, dryRun bool) (*Runner, error) {
	retentionToUse := globalRetentionPeriod
	if spec.Retention > 0 {
		retentionToUse = spec.Retention
		logger.Log.Info("GC: Using spec-defined retention period",
			zap.String("containerID", spec.ContainerID),
			zap.Duration("specRetention", spec.Retention),
		)
	} else {
		logger.Log.Info("GC: Using global retention period",
			zap.String("containerID", spec.ContainerID),
			zap.Duration("globalRetention", globalRetentionPeriod),
		)
	}

	if retentionToUse <= 0 {
		logger.Log.Warn("GC: Effective retention period is zero or negative. No garbage collection will be performed for this spec.",
			zap.String("containerID", spec.ContainerID),
			zap.Duration("effectiveRetention", retentionToUse),
		)
	}

	logger.Log.Info("GC Runner configured",
		zap.String("containerID", spec.ContainerID),
		zap.String("prefix", spec.Prefix),
		zap.Duration("effectiveRetention", retentionToUse),
		zap.Bool("dryRun", dryRun),
		zap.String("writerType", bw.Type()),
	)

	return &Runner{
		spec:              spec,
		backupWriter:      bw,
		effectiveRetention: retentionToUse,
		dryRun:            dryRun,
	}, nil
}

func (r *Runner) RunGC(ctx context.Context) error {
	if r.effectiveRetention <= 0 {
		logger.Log.Info("GC: Skipping run as effective retention period is not positive.",
			zap.String("containerID", r.spec.ContainerID),
			zap.Duration("effectiveRetention", r.effectiveRetention),
		)
		return nil
	}

	logger.Log.Info("Starting GC run",
		zap.String("containerID", r.spec.ContainerID),
		zap.String("prefix", r.spec.Prefix),
		zap.String("writerType", r.backupWriter.Type()),
		zap.Duration("retention", r.effectiveRetention),
		zap.Bool("dryRun", r.dryRun),
	)

	listCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	objects, err := r.backupWriter.ListObjects(listCtx, r.spec.Prefix)
	if err != nil {
		logger.Log.Error("GC failed to list objects",
			zap.String("containerID", r.spec.ContainerID),
			zap.String("prefix", r.spec.Prefix),
			zap.Error(err),
		)
		return fmt.Errorf("GC failed to list objects for prefix '%s': %w", r.spec.Prefix, err)
	}

	if len(objects) == 0 {
		logger.Log.Info("GC: No objects found for prefix. Nothing to do.",
			zap.String("containerID", r.spec.ContainerID),
			zap.String("prefix", r.spec.Prefix),
		)
		return nil
	}

	deleteCount := 0
	var failedDeletes []string
	var totalSizeFreed int64
	now := time.Now().UTC()
	cutoffDate := now.Add(-r.effectiveRetention)

	logger.Log.Info("GC: Object scan details",
		zap.String("containerID", r.spec.ContainerID),
		zap.Int("objectCount", len(objects)),
		zap.String("prefix", r.spec.Prefix),
		zap.String("cutoffDate", cutoffDate.Format(time.RFC3339)),
	)

	for _, obj := range objects {
		if ctx.Err() != nil {
			logger.Log.Warn("GC run cancelled during object iteration",
				zap.String("containerID", r.spec.ContainerID),
				zap.String("prefix", r.spec.Prefix),
				zap.Int("processedCount", deleteCount),
				zap.Int("totalCount", len(objects)),
				zap.Error(ctx.Err()),
			)
			return ctx.Err()
		}
		
		if obj.LastModified.Before(cutoffDate) {
			logger.Log.Info("GC: Object qualifies for deletion",
				zap.String("containerID", r.spec.ContainerID),
				zap.String("key", obj.Key),
				zap.Time("lastModified", obj.LastModified),
				zap.Int64("size", obj.Size),
			)
			if r.dryRun {
				logger.Log.Info("[DryRun] GC: Would delete object",
					zap.String("containerID", r.spec.ContainerID),
					zap.String("key", obj.Key),
					zap.Int64("size", obj.Size),
				)
				deleteCount++
				totalSizeFreed += obj.Size
			} else {
				deleteCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				err := r.backupWriter.DeleteObject(deleteCtx, obj.Key)
				cancel()
				if err != nil {
					logger.Log.Error("GC: Failed to delete object",
						zap.String("containerID", r.spec.ContainerID),
						zap.String("key", obj.Key),
						zap.Error(err),
					)
					failedDeletes = append(failedDeletes, obj.Key)
					continue
				}
				logger.Log.Info("GC: Successfully deleted object",
					zap.String("containerID", r.spec.ContainerID),
					zap.String("key", obj.Key),
					zap.Int64("size", obj.Size),
				)
				deleteCount++
				totalSizeFreed += obj.Size
				
				// Rate limiting - small delay between deletes
				time.Sleep(100 * time.Millisecond)
			}
		} else {
			logger.Log.Debug("GC: Object is within retention period. Keeping.",
				zap.String("containerID", r.spec.ContainerID),
				zap.String("key", obj.Key),
				zap.Time("lastModified", obj.LastModified),
			)
		}
	}

	statusMsg := "deleted"
	if r.dryRun {
		statusMsg = "that would be deleted (dry run)"
	}
	
	logger.Log.Info("GC run completed",
		zap.String("containerID", r.spec.ContainerID),
		zap.String("prefix", r.spec.Prefix),
		zap.Int("objectsConsidered", len(objects)),
		zap.String("status", statusMsg),
		zap.Int("objectsAffected", deleteCount),
		zap.Int64("totalSizeFreed", totalSizeFreed),
		zap.Int("failedDeletes", len(failedDeletes)),
	)
	
	if len(failedDeletes) > 0 {
		return fmt.Errorf("GC completed with %d failures: %v", len(failedDeletes), failedDeletes)
	}
	
	return nil
} 