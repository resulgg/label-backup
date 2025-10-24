package scheduler

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"label-backup/internal/discovery"
	"label-backup/internal/dumper"
	"label-backup/internal/logger"
	"label-backup/internal/model"
	"label-backup/internal/webhook"
	"label-backup/internal/writer"

	"github.com/robfig/cron/v3"
	"go.uber.org/zap"
)

type scheduledJob struct {
	spec   model.BackupSpec
	cronID cron.EntryID
}

type Scheduler struct {
	cron             *cron.Cron
	mu               sync.Mutex
	activeJobs       map[string]*scheduledJob
	globalConfig     map[string]string
	webhookSender    webhook.WebhookSender
	discoveryWatcher *discovery.Watcher
	concurrencyLimit chan struct{}
}

func NewScheduler(globalCfg map[string]string, whSender webhook.WebhookSender, dw *discovery.Watcher) *Scheduler {
	c := cron.New(
		cron.WithSeconds(),
		cron.WithChain(
			cron.SkipIfStillRunning(logger.NewCronZapLogger(logger.Log.Named("cron-skip-if-running"))),
		),
		cron.WithLogger(logger.NewCronZapLogger(logger.Log.Named("cron"))),
	)
	
	concurrencyLimit := 20
	if limitStr, ok := globalCfg["CONCURRENT_BACKUP_LIMIT"]; ok && limitStr != "" {
		if limit, err := strconv.Atoi(limitStr); err == nil && limit > 0 {
			concurrencyLimit = limit
		} else {
			logger.Log.Warn("Invalid CONCURRENT_BACKUP_LIMIT value, using default", 
				zap.String("value", limitStr), 
				zap.Int("default", 20),
				zap.Error(err),
			)
		}
	}
	
	s := &Scheduler{
		cron:             c,
		activeJobs:       make(map[string]*scheduledJob),
		globalConfig:     globalCfg,
		webhookSender:    whSender,
		discoveryWatcher: dw,
		concurrencyLimit: make(chan struct{}, concurrencyLimit),
	}
	s.cron.Start()
	logger.Log.Info("Cron scheduler started", zap.Int("concurrencyLimit", concurrencyLimit))
	return s
}

func (s *Scheduler) AddOrUpdateJob(containerID string, spec model.BackupSpec) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	existingJob, exists := s.activeJobs[containerID]
	if exists {
		if existingJob.spec.Cron == spec.Cron {
			logger.Log.Debug("Cron spec unchanged for existing job, updating internal spec details only",
				zap.String("containerID", containerID),
				zap.String("cron", spec.Cron))
			existingJob.spec = spec
			return nil
		}

		logger.Log.Info("Cron spec changed for existing job, re-scheduling",
			zap.String("containerID", containerID),
			zap.String("oldCron", existingJob.spec.Cron),
			zap.String("newCron", spec.Cron))
		s.cron.Remove(existingJob.cronID)
	}

	jobFunction := s.jobFunc(containerID, spec)

	cronSpecToUse := spec.Cron
	trimmedCron := strings.TrimSpace(spec.Cron)
	fields := strings.Fields(trimmedCron)

	if !strings.HasPrefix(trimmedCron, "@") && len(fields) == 5 {
		cronSpecToUse = "0 " + trimmedCron
		logger.Log.Info("Converted 5-field cron expression to 6-field",
			zap.String("containerID", containerID),
			zap.String("originalCron", spec.Cron),
			zap.String("convertedCron", cronSpecToUse),
		)
	}

	newCronID, err := s.cron.AddFunc(cronSpecToUse, jobFunction)
	if err != nil {
		logger.Log.Error("Failed to add cron job",
			zap.String("containerID", containerID),
			zap.String("cronAttempted", cronSpecToUse),
			zap.String("originalCronLabel", spec.Cron),
			zap.Error(err))
		return fmt.Errorf("failed to add cron job for %s (attempted: '%s', original: '%s'): %w", containerID, cronSpecToUse, spec.Cron, err)
	}

	s.activeJobs[containerID] = &scheduledJob{
		spec:   spec,
		cronID: newCronID,
	}
	logAction := "Successfully added new cron job"
	if exists {
	    logAction = "Successfully updated existing cron job"
	}
	logger.Log.Info(logAction,
		zap.String("containerID", containerID),
		zap.String("cron", spec.Cron),
		zap.String("dbType", spec.Type),
		zap.String("destination", spec.Dest),
		zap.Int("cronEntryID", int(newCronID)),
	)
	return nil
}

func (s *Scheduler) RemoveJob(containerID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	jobDetails, exists := s.activeJobs[containerID]
	if exists {
		s.cron.Remove(jobDetails.cronID)
		delete(s.activeJobs, containerID)
		logger.Log.Info("Removed cron job", zap.String("containerID", containerID))
	}
}

func (s *Scheduler) jobFunc(containerID string, spec model.BackupSpec) func() {
	return func() {
		select {
		case s.concurrencyLimit <- struct{}{}:
		default:
			logger.Log.Warn("Skipping backup due to concurrency limit reached",
				zap.String("containerID", containerID),
				zap.String("containerName", spec.ContainerName),
			)
			return
		}
		
		defer func() {
			<-s.concurrencyLimit
		}()

		startTime := time.Now()
		// Use configurable timeout for backup operations (default 30 minutes)
		backupTimeout := 30 * time.Minute
		if timeoutStr, ok := s.globalConfig["BACKUP_TIMEOUT_MINUTES"]; ok && timeoutStr != "" {
			if timeout, err := strconv.Atoi(timeoutStr); err == nil && timeout > 0 {
				backupTimeout = time.Duration(timeout) * time.Minute
			} else {
				logger.Log.Warn("Invalid BACKUP_TIMEOUT_MINUTES value, using default",
					zap.String("value", timeoutStr),
					zap.Duration("default", backupTimeout),
					zap.Error(err),
				)
			}
		}
		jobCtx, cancel := context.WithTimeout(context.Background(), backupTimeout)
		defer cancel()

		logger.Log.Info("Starting backup job",
			zap.String("containerID", containerID),
			zap.String("dbType", spec.Type),
			zap.String("containerName", spec.ContainerName),
		)

		payload := webhook.NotificationPayload{
			Timestamp:       startTime.UTC().Format(time.RFC3339),
			ContainerID:     containerID,
			ContainerName:   spec.ContainerName,
			DatabaseType:    spec.Type,
			DatabaseName:    spec.Database,
			CronSchedule:    spec.Cron,
			BackupPrefix:    spec.Prefix,
		}

		dbDumper, err := dumper.GetDumper(spec)
		if err != nil {
			errMsg := fmt.Sprintf("Failed to get dumper for %s: %v", spec.Type, err)
			logger.Log.Error(errMsg, zap.String("containerID", containerID))
			payload.Success = false
			payload.Error = errMsg
			payload.DurationSeconds = time.Since(startTime).Seconds()
			if s.webhookSender != nil {
				s.webhookSender.Enqueue(payload, spec)
			}
			return
		}
		logger.Log.Debug("Dumper obtained", zap.String("containerID", containerID), zap.String("type", spec.Type))

		// Test database connection before proceeding with backup
		if err := dbDumper.TestConnection(jobCtx, spec); err != nil {
			errMsg := fmt.Sprintf("Database connection test failed for %s: %v", spec.Type, err)
			logger.Log.Error(errMsg, zap.String("containerID", containerID))
			payload.Success = false
			payload.Error = errMsg
			payload.DurationSeconds = time.Since(startTime).Seconds()
			if s.webhookSender != nil {
				s.webhookSender.Enqueue(payload, spec)
			}
			return
		}
		logger.Log.Debug("Database connection test successful", zap.String("containerID", containerID), zap.String("type", spec.Type))

		backupWriter, err := writer.GetWriter(spec, s.globalConfig)
		if err != nil {
			errMsg := fmt.Sprintf("Failed to get writer for %s: %v", spec.Dest, err)
			logger.Log.Error(errMsg, zap.String("containerID", containerID))
			payload.Success = false
			payload.Error = errMsg
			payload.DurationSeconds = time.Since(startTime).Seconds()
			if s.webhookSender != nil {
				s.webhookSender.Enqueue(payload, spec)
			}
			return
		}
		payload.DestinationType = backupWriter.Type()
		logger.Log.Debug("Writer obtained", zap.String("containerID", containerID), zap.String("type", backupWriter.Type()))

		pr, pw := io.Pipe()

		var bytesWritten int64
		var writeErr error
		var backupChecksum string
		var dumpErr error
		var destinationURL string
		var wg sync.WaitGroup
		wg.Add(2)
		
		objectName := writer.GenerateObjectName(spec)

		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					logger.Log.Error("Panic in dumper goroutine", zap.Any("panic", r), zap.String("containerID", containerID))
					dumpErr = fmt.Errorf("panic: %v", r)
				}
				if errClosePipe := pw.Close(); errClosePipe != nil && errClosePipe != io.ErrClosedPipe {
					logger.Log.Error("Error closing pipe writer in dumper goroutine", zap.Error(errClosePipe), zap.String("containerID", containerID))
				}
			}()
			
			// Monitor context cancellation
			select {
			case <-jobCtx.Done():
				dumpErr = fmt.Errorf("backup cancelled: %w", jobCtx.Err())
				logger.Log.Warn("Backup cancelled during dump", zap.String("containerID", containerID), zap.Error(jobCtx.Err()))
				return
			default:
			}
			
			dumpErr = dbDumper.Dump(jobCtx, spec, pw)
			if dumpErr != nil {
				logger.Log.Error("Dumper failed", zap.Error(dumpErr), zap.String("containerID", containerID))
				_ = pw.CloseWithError(dumpErr)
			} else {
				logger.Log.Info("Dump completed successfully by dumper goroutine", zap.String("containerID", containerID))
			}
		}()

		go func() {
		    defer wg.Done()
		    defer func() {
			    if r := recover(); r != nil {
				    logger.Log.Error("Panic in writer goroutine", zap.Any("panic", r), zap.String("containerID", containerID))
				    writeErr = fmt.Errorf("panic: %v", r)
			    }
		    }()
		    
		    // Monitor context cancellation
		    select {
		    case <-jobCtx.Done():
			    writeErr = fmt.Errorf("backup cancelled: %w", jobCtx.Err())
			    logger.Log.Warn("Backup cancelled during write", zap.String("containerID", containerID), zap.Error(jobCtx.Err()))
			    return
		    default:
		    }
		    
		    destinationURL, bytesWritten, backupChecksum, writeErr = backupWriter.Write(jobCtx, objectName, pr)
		}()

		wg.Wait() 

		finalErrorMsg := ""
		jobSuccess := true

		if dumpErr != nil {
			finalErrorMsg = fmt.Sprintf("dump error: %v", dumpErr)
			jobSuccess = false
		}
		if writeErr != nil {
			if finalErrorMsg != "" {
				finalErrorMsg += "; "
			}
			finalErrorMsg += fmt.Sprintf("write error: %v", writeErr)
			jobSuccess = false
			logger.Log.Error("Writer failed", zap.Error(writeErr), zap.String("containerID", containerID), zap.String("objectName", objectName))
		}

		// Update payload with final results
		payload.Success = jobSuccess
		payload.DurationSeconds = time.Since(startTime).Seconds()
		payload.BackupSize = bytesWritten
		payload.DestinationURL = destinationURL 
		if !jobSuccess {
			payload.Error = finalErrorMsg
		}

		// Only write metadata for successful backups
		if jobSuccess && bytesWritten > 0 {
			metadata := writer.BackupMetadata{
				Timestamp:       startTime,
				ContainerID:     containerID,
				ContainerName:   spec.ContainerName,
				DatabaseType:    spec.Type,
				DatabaseName:    spec.Database,
				BackupSize:      bytesWritten,
				Checksum:        backupChecksum,
				CompressionType: "gzip",
				Version:         "1.0",
				Destination:     destinationURL,
				DurationSeconds: payload.DurationSeconds,
				Success:         jobSuccess,
				Error:           payload.Error,
			}
			
			if err := writer.WriteMetadata(jobCtx, backupWriter, metadata, objectName); err != nil {
				logger.Log.Warn("Failed to write backup metadata", 
					zap.String("containerID", containerID),
					zap.String("objectName", objectName),
					zap.Error(err),
				)
			}
		} else if !jobSuccess && bytesWritten > 0 {
			// Cleanup partial backup on failure
			if err := backupWriter.DeleteObject(jobCtx, objectName); err != nil {
				logger.Log.Warn("Failed to cleanup partial backup",
					zap.String("containerID", containerID),
					zap.String("objectName", objectName),
					zap.Error(err),
				)
			} else {
				logger.Log.Info("Cleaned up partial backup",
					zap.String("containerID", containerID),
					zap.String("objectName", objectName),
				)
			}
		} 

		if jobSuccess {
			logger.Log.Info("Backup job write completed successfully",
				zap.String("containerID", containerID),
				zap.String("objectName", objectName), 
				zap.Int64("bytesWritten", bytesWritten),
				zap.String("destination", destinationURL),
				zap.String("checksum", backupChecksum),
			)
		} else {
			logger.Log.Error("Backup job failed overall",
				zap.String("containerID", containerID),
				zap.String("finalErrorSummary", finalErrorMsg), 
			)
		}

		logger.Log.Info("Backup job finished processing",
			zap.String("containerID", containerID),
			zap.Bool("success", payload.Success),
			zap.Float64("durationSeconds", payload.DurationSeconds),
			zap.Int64("sizeBytes", payload.BackupSize), 
			zap.String("destinationURL", payload.DestinationURL),
			zap.String("error", payload.Error),
		)

		if s.webhookSender != nil {
			s.webhookSender.Enqueue(payload, spec)
		} else {
			logger.Log.Warn("Webhook sender is not initialized, cannot send notification", zap.String("containerID", containerID))
		}
	}
}

func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cron != nil {
		logger.Log.Info("Stopping cron scheduler...")
		ctx := s.cron.Stop()
		select {
		case <-ctx.Done():
			logger.Log.Info("Cron scheduler stopped gracefully.")
		case <-time.After(10 * time.Second):
			logger.Log.Warn("Cron scheduler stop timed out after 10s. Some jobs may not have finished.")
		}
	}
}

func (s *Scheduler) GetActiveJobsCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.activeJobs)
}