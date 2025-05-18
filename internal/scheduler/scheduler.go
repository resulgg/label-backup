package scheduler

import (
	"context"
	"fmt"
	"io"
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

// scheduledJob holds the details of a job managed by the scheduler.
type scheduledJob struct {
	spec   model.BackupSpec
	cronID cron.EntryID
}

// Scheduler manages cron jobs for backups.
type Scheduler struct {
	cron             *cron.Cron
	mu               sync.Mutex
	activeJobs       map[string]*scheduledJob // Stores containerID -> *scheduledJob
	globalConfig     map[string]string
	webhookSender    webhook.WebhookSender
	discoveryWatcher *discovery.Watcher
}

// NewScheduler creates a new Scheduler.
func NewScheduler(globalCfg map[string]string, whSender webhook.WebhookSender, dw *discovery.Watcher) *Scheduler {
	c := cron.New(
		cron.WithSeconds(),
		cron.WithChain(
			cron.SkipIfStillRunning(logger.NewCronZapLogger(logger.Log.Named("cron-skip-if-running"))),
		),
		cron.WithLogger(logger.NewCronZapLogger(logger.Log.Named("cron"))),
	)
	s := &Scheduler{
		cron:             c,
		activeJobs:       make(map[string]*scheduledJob),
		globalConfig:     globalCfg,
		webhookSender:    whSender,
		discoveryWatcher: dw,
	}
	s.cron.Start()
	logger.Log.Info("Cron scheduler started")
	return s
}

// AddOrUpdateJob adds a new backup job or updates an existing one.
func (s *Scheduler) AddOrUpdateJob(containerID string, spec model.BackupSpec) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	existingJob, exists := s.activeJobs[containerID]
	if exists {
		// If cron spec is the same, just update the spec details internally and avoid rescheduling.
		if existingJob.spec.Cron == spec.Cron {
			logger.Log.Debug("Cron spec unchanged for existing job, updating internal spec details only",
				zap.String("containerID", containerID),
				zap.String("cron", spec.Cron))
			existingJob.spec = spec // Update stored spec
			return nil
		}

		// Cron spec has changed, so remove the old cron entry before adding the new one.
		logger.Log.Info("Cron spec changed for existing job, re-scheduling",
			zap.String("containerID", containerID),
			zap.String("oldCron", existingJob.spec.Cron),
			zap.String("newCron", spec.Cron))
		s.cron.Remove(existingJob.cronID)
	}

	jobFunction := s.jobFunc(containerID, spec)

	// Hem 5 alanlı hem de 6 alanlı cron ifadelerini desteklemek için cronSpec'i ayarla
	cronSpecToUse := spec.Cron
	// spec.Cron'u boşluklara göre bölmeden önce trim yapalım ki baştaki/sondaki boşluklar sorun yaratmasın
	trimmedCron := strings.TrimSpace(spec.Cron)
	fields := strings.Fields(trimmedCron) // trimmedCron'u boşluklara göre böl

	// Eğer ifade "@" ile başlamıyorsa VE 5 alandan oluşuyorsa
	if !strings.HasPrefix(trimmedCron, "@") && len(fields) == 5 {
		// 5 alanlı bir ifadeyse ve bir tanımlayıcı (@daily vb.) değilse,
		// saniyeler için başına "0 " ekle.
		// Bu, kullanıcının ifadenin belirtilen dakikanın 0. saniyesinde çalışmasını istediğini varsayar.
		cronSpecToUse = "0 " + trimmedCron // trimmedCron kullanılıyor
		logger.Log.Info("Converted 5-field cron expression to 6-field",
			zap.String("containerID", containerID),
			zap.String("originalCron", spec.Cron),
			zap.String("convertedCron", cronSpecToUse),
		)
	}

	newCronID, err := s.cron.AddFunc(cronSpecToUse, jobFunction) // cronSpecToUse kullanılır
	if err != nil {
		// Hata durumunda daha net olması için hem denenen hem de orijinal cron ifadesini logla
		logger.Log.Error("Cron job eklenemedi",
			zap.String("containerID", containerID),
			zap.String("cronAttempted", cronSpecToUse),      // Denenen ifade
			zap.String("originalCronLabel", spec.Cron), // Etiketten gelen orijinal ifade
			zap.Error(err))
		return fmt.Errorf("failed to add cron job for %s (denenen: '%s', orijinal: '%s'): %w", containerID, cronSpecToUse, spec.Cron, err)
	}

	s.activeJobs[containerID] = &scheduledJob{
		spec:   spec, // Orijinal spec'i sakla (dönüştürülmemiş cron ile)
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

// RemoveJob removes a backup job.
func (s *Scheduler) RemoveJob(containerID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	jobDetails, exists := s.activeJobs[containerID]
	if exists {
		s.cron.Remove(jobDetails.cronID) // Use cronID from the stored jobDetails
		delete(s.activeJobs, containerID)
		logger.Log.Info("Removed cron job", zap.String("containerID", containerID))
	}
}

// jobFunc returns the function to be executed by the cron job.
// It now includes webhook notification logic.
func (s *Scheduler) jobFunc(containerID string, spec model.BackupSpec) func() {
	return func() {
		startTime := time.Now()
		jobCtx := context.Background() // Context for this job run

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
		var dumpErr error
		var destinationURL string // Stores the actual URL/path from the writer
		var wg sync.WaitGroup
		wg.Add(2)
		
		objectName := writer.GenerateObjectName(spec) // Create objectName once

		go func() {
			defer wg.Done()
			defer func() {
				if errClosePipe := pw.Close(); errClosePipe != nil && errClosePipe != io.ErrClosedPipe {
					logger.Log.Error("Error closing pipe writer in dumper goroutine", zap.Error(errClosePipe), zap.String("containerID", containerID))
				}
			}()
			dumpErr = dbDumper.Dump(jobCtx, spec, pw) // jobCtx, spec, pw
			if dumpErr != nil {
				logger.Log.Error("Dumper failed", zap.Error(dumpErr), zap.String("containerID", containerID))
				_ = pw.CloseWithError(dumpErr)
			} else {
				logger.Log.Info("Dump completed successfully by dumper goroutine", zap.String("containerID", containerID))
			}
		}()

		go func() {
		    defer wg.Done()
		    destinationURL, bytesWritten, writeErr = backupWriter.Write(jobCtx, objectName, pr) // jobCtx, objectName, pr
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

		payload.Error = finalErrorMsg
		payload.Success = jobSuccess
		payload.DurationSeconds = time.Since(startTime).Seconds()
		payload.BackupSize = bytesWritten
		payload.DestinationURL = destinationURL 

		if jobSuccess {
			logger.Log.Info("Backup job write completed successfully",
				zap.String("containerID", containerID),
				zap.String("objectName", objectName), 
				zap.Int64("bytesWritten", bytesWritten),
				zap.String("destination", destinationURL),
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

// Stop the scheduler.
func (s *Scheduler) Stop() {
	s.mu.Lock() // Ensure exclusive access for stopping
	defer s.mu.Unlock()
	if s.cron != nil {
		logger.Log.Info("Stopping cron scheduler...")
		ctx := s.cron.Stop() // Stop gracefully waits for running jobs
		select {
		case <-ctx.Done():
			logger.Log.Info("Cron scheduler stopped gracefully.")
		case <-time.After(10 * time.Second): // Add a timeout for safety
			logger.Log.Warn("Cron scheduler stop timed out after 10s. Some jobs may not have finished.")
		}
	}
}

// GetActiveJobsCount returns the number of active jobs in the scheduler.
func (s *Scheduler) GetActiveJobsCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.activeJobs)
}