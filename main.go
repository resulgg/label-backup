package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"label-backup/internal/discovery"
	"label-backup/internal/gc"
	"label-backup/internal/logger"
	"label-backup/internal/model"
	"label-backup/internal/scheduler"
	"label-backup/internal/webhook"
	"label-backup/internal/writer"

	"github.com/robfig/cron/v3"
	"go.uber.org/zap"
)

const (
	DefaultReconcileIntervalSeconds = 10
	EnvReconcileIntervalSeconds   = "RECONCILE_INTERVAL_SECONDS"
	EnvGlobalRetentionPeriod      = "GLOBAL_RETENTION_PERIOD"
	DefaultGlobalRetentionPeriod  = "7d" 
	EnvGCDryRun                   = "GC_DRY_RUN"
)

var globalRetentionPeriod time.Duration 
var gcDryRun bool                     

func parseRetentionPeriod(retentionStr string, defaultValue string) time.Duration {
	value := strings.TrimSpace(retentionStr)
	if value == "" {
		value = defaultValue
	}

	d, err := time.ParseDuration(value)
	if err == nil {
		if d < 0 {
			logger.Log.Warn("Global retention period is negative, using default",
				zap.String("value", value),
				zap.String("default", defaultValue),
			)
			d, _ = time.ParseDuration(defaultValue)
			return d
		}
		return d
	}

	if strings.HasSuffix(value, "d") {
		daysStr := strings.TrimSuffix(value, "d")
		days, convErr := strconv.Atoi(daysStr)
		if convErr == nil {
			if days < 0 {
				logger.Log.Warn("Global retention days is negative, using default",
					zap.String("value", value),
					zap.String("default", defaultValue),
				)
				d, _ = time.ParseDuration(defaultValue) 
				return d
			}
			return time.Duration(days) * 24 * time.Hour
		}
	}

	days, convErr := strconv.Atoi(value)
	if convErr == nil {
		if days < 0 {
			logger.Log.Warn("Global retention days is negative, using default",
				zap.String("value", value),
				zap.String("default", defaultValue),
			)
			d, _ = time.ParseDuration(defaultValue)
			return d
		}
		return time.Duration(days) * 24 * time.Hour
	}

	logger.Log.Warn("Invalid global retention period format, using default.",
		zap.String("value", value),
		zap.String("default", defaultValue),
		zap.Error(err), 
	)
	d, _ = time.ParseDuration(defaultValue) 
	return d
}

func loadGlobalConfig() map[string]string {
	cfg := make(map[string]string)

	getTrimmedEnv := func(key string) string {
		val := os.Getenv(key)
		return strings.Trim(val, "\\\"")
	}

	if s3Bucket := getTrimmedEnv("BUCKET_NAME"); s3Bucket != "" {
		cfg[writer.GlobalConfigKeyS3Bucket] = s3Bucket
		logger.Log.Info("Using S3 bucket from env", zap.String("bucket", s3Bucket))
	} else {
		logger.Log.Warn("Warning: BUCKET_NAME not set. S3 writer will fail if used without a bucket.")
	}
	if awsRegion := getTrimmedEnv("REGION"); awsRegion != "" {
		cfg[writer.GlobalConfigKeyS3Region] = awsRegion
		logger.Log.Info("Using AWS region from env", zap.String("region", awsRegion))
	}
	if s3Endpoint := getTrimmedEnv("ENDPOINT"); s3Endpoint != "" {
		cfg[writer.GlobalConfigKeyS3Endpoint] = s3Endpoint
		logger.Log.Info("Using S3 endpoint from env", zap.String("endpoint", s3Endpoint))
	}
	if accessKeyID := getTrimmedEnv("ACCESS_KEY_ID"); accessKeyID != "" {
		cfg[writer.GlobalConfigKeyS3AccessKeyID] = accessKeyID
		logger.Log.Info("Using S3 access key ID from env") 
	}
	if secretAccessKey := getTrimmedEnv("SECRET_ACCESS_KEY"); secretAccessKey != "" {
		cfg[writer.GlobalConfigKeyS3SecretAccessKey] = secretAccessKey
		logger.Log.Info("Using S3 secret access key from env") 
	}

	if localPath := getTrimmedEnv("LOCAL_BACKUP_PATH"); localPath != "" {
		cfg[writer.GlobalConfigKeyLocalPath] = localPath
		logger.Log.Info("Using local backup path from env", zap.String("path", localPath))
	} else {
		cfg[writer.GlobalConfigKeyLocalPath] = writer.DefaultLocalPath 
		logger.Log.Info("LOCAL_BACKUP_PATH not set, using default", zap.String("path", writer.DefaultLocalPath))
	}

	retentionPeriodStr := os.Getenv(EnvGlobalRetentionPeriod)
	globalRetentionPeriod = parseRetentionPeriod(retentionPeriodStr, DefaultGlobalRetentionPeriod)
	logger.Log.Info("Using global retention period", zap.Duration("period", globalRetentionPeriod))

	dryRunStr := strings.ToLower(os.Getenv(EnvGCDryRun))
	gcDryRun = (dryRunStr == "true" || dryRunStr == "1")
	logger.Log.Info("GC Dry Run mode", zap.Bool("enabled", gcDryRun))

	return cfg
}

func runGlobalGC(ctx context.Context, discoveryWatcher *discovery.Watcher, writerCfg map[string]string, retentionPeriodForGC time.Duration, isDryRun bool) {
	logger.Log.Info("Starting nightly global Garbage Collection run...")
	activeSpecs := discoveryWatcher.GetRegistry() 

	if len(activeSpecs) == 0 {
		logger.Log.Info("Global GC: No active backup specifications found. Nothing to GC.")
		return
	}

	for containerID, spec := range activeSpecs {
		if !spec.Enabled {
			logger.Log.Info("Global GC: Skipping disabled spec for container", zap.String("containerID", containerID))
			continue
		}
		if ctx.Err() != nil {
		    logger.Log.Info("Global GC run cancelled.")
		    return
		}

		logger.Log.Info("Global GC: Processing spec for container", zap.String("containerID", containerID), zap.String("prefix", spec.Prefix), zap.String("dest", spec.Dest))
		backupWriter, err := writer.GetWriter(spec, writerCfg)
		if err != nil {
			logger.Log.Error("Global GC: Failed to get writer for spec", zap.String("containerID", containerID), zap.String("dest", spec.Dest), zap.Error(err))
			continue
		}

		gcRunner, err := gc.NewRunner(spec, backupWriter, retentionPeriodForGC, isDryRun)
		if err != nil {
			logger.Log.Error("Global GC: Failed to create GC runner for spec", zap.String("containerID", containerID), zap.Error(err))
			continue
		}

		if err := gcRunner.RunGC(ctx); err != nil {
			logger.Log.Error("Global GC: Error during GC run for spec", 
			    zap.String("containerID", containerID), 
			    zap.String("prefix", spec.Prefix), 
			    zap.Error(err),
			)
		}
	}
	logger.Log.Info("Nightly global Garbage Collection run finished.")
}


func checkDiskSpace(path string) error {
	return writer.CheckDiskSpace(path)
}

func validateConfig(globalConfig map[string]string) error {
	var errors []string

	if cronExpr, ok := globalConfig["GLOBAL_CRON"]; ok && cronExpr != "" {
		if _, err := cron.ParseStandard(cronExpr); err != nil {
			errors = append(errors, fmt.Sprintf("Invalid GLOBAL_CRON expression '%s': %v", cronExpr, err))
		}
	}

	if retentionStr, ok := globalConfig["GLOBAL_RETENTION"]; ok && retentionStr != "" {
		if duration := parseRetentionPeriod(retentionStr, ""); duration == 0 && retentionStr != "" {
			errors = append(errors, fmt.Sprintf("Invalid GLOBAL_RETENTION '%s': cannot parse duration", retentionStr))
		}
	}

	if limitStr, ok := globalConfig["CONCURRENT_BACKUP_LIMIT"]; ok && limitStr != "" {
		if limit, err := strconv.Atoi(limitStr); err != nil || limit <= 0 {
			errors = append(errors, fmt.Sprintf("Invalid CONCURRENT_BACKUP_LIMIT '%s': must be a positive integer", limitStr))
		}
	}

	if bucket, ok := globalConfig["BUCKET_NAME"]; ok && bucket != "" {
		logger.Log.Debug("S3 bucket configuration validated", zap.String("bucket", bucket))
	}

	if localPath, ok := globalConfig["LOCAL_BACKUP_PATH"]; ok && localPath != "" {
		logger.Log.Debug("Local backup path configuration validated", zap.String("path", localPath))
	}

	if len(errors) > 0 {
		return fmt.Errorf("configuration validation failed:\n%s", strings.Join(errors, "\n"))
	}

	logger.Log.Info("Configuration validation successful")
	return nil
}

func main() {
	logger.Log.Info("Label Backup Agent starting...")
	defer logger.Close() 

	globalCfgForWriterAndOthers := loadGlobalConfig()

	if err := validateConfig(globalCfgForWriterAndOthers); err != nil {
		logger.Log.Fatal("Configuration validation failed", zap.Error(err))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	discoveryWatcher, err := discovery.NewWatcher() 
	if err != nil {
		logger.Log.Fatal("Failed to initialize discovery watcher", zap.Error(err))
	}
	defer discoveryWatcher.Close() 

	webhookSender := webhook.NewSender(globalCfgForWriterAndOthers) 

	sched := scheduler.NewScheduler(globalCfgForWriterAndOthers, webhookSender, discoveryWatcher) 

	gcCron := cron.New(cron.WithLogger(logger.NewCronZapLogger(logger.Log.Named("gc-cron")))) 
	_, err = gcCron.AddFunc("0 4 * * *", func() { 
		gcCtx, gcCancel := context.WithTimeout(context.Background(), 1*time.Hour) 
		defer gcCancel()
		runGlobalGC(gcCtx, discoveryWatcher, globalCfgForWriterAndOthers, globalRetentionPeriod, gcDryRun)
	})
	if err != nil {
		logger.Log.Fatal("Failed to schedule nightly GC job", zap.Error(err))
	}
	gcCron.Start()
	logger.Log.Info("Nightly GC job scheduled for 04:00 daily.")
	defer func() {
	    logger.Log.Info("Stopping GC cron scheduler...")
	    gcCronCtx := gcCron.Stop()
	    <-gcCronCtx.Done()
	    logger.Log.Info("GC cron scheduler stopped.")
	}()

	go discoveryWatcher.Start(ctx) 

	hmux := http.NewServeMux()
	hmux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "ok") 
		logger.Log.Debug("Health check successful", zap.String("path", r.URL.Path))
	})

	hmux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		var checks []string
		var allHealthy bool = true

		if err := discoveryWatcher.TestDockerConnection(ctx); err != nil {
			checks = append(checks, fmt.Sprintf("Docker: %v", err))
			allHealthy = false
		} else {
			checks = append(checks, "Docker: OK")
		}

		if err := checkDiskSpace("/backups"); err != nil {
			checks = append(checks, fmt.Sprintf("Disk: %v", err))
			allHealthy = false
		} else {
			checks = append(checks, "Disk: OK")
		}

		if bucket, ok := globalCfgForWriterAndOthers["BUCKET_NAME"]; ok && bucket != "" {
			checks = append(checks, "S3: OK")
		}

		if allHealthy {
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, "ready\n%s", strings.Join(checks, "\n"))
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, "not ready\n%s", strings.Join(checks, "\n"))
		}
	})

	hmux.HandleFunc("/metadata", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		// Get query parameters
		objectName := r.URL.Query().Get("object")
		if objectName == "" {
			http.Error(w, "object parameter is required", http.StatusBadRequest)
			return
		}

		// Get writer for the first available container (for testing)
		registry := discoveryWatcher.GetRegistry()
		if len(registry) == 0 {
			http.Error(w, "no containers found", http.StatusNotFound)
			return
		}

		// Use the first container's spec to get writer
		var firstSpec model.BackupSpec
		for _, spec := range registry {
			firstSpec = spec
			break
		}

		backupWriter, err := writer.GetWriter(firstSpec, globalCfgForWriterAndOthers)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to get writer: %v", err), http.StatusInternalServerError)
			return
		}

		// Read metadata
		metadata, err := writer.ReadMetadata(ctx, backupWriter, objectName)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to read metadata: %v", err), http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(metadata)
	})

	hmux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		_, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		registry := discoveryWatcher.GetRegistry()
		
		status := map[string]interface{}{
			"timestamp": time.Now().UTC().Format(time.RFC3339),
			"active_jobs": len(registry),
			"containers": make([]map[string]interface{}, 0),
		}

		for containerID, spec := range registry {
			containerInfo := map[string]interface{}{
				"container_id":   containerID,
				"container_name": spec.ContainerName,
				"database_type":  spec.Type,
				"database_name":  spec.Database,
				"cron_schedule":  spec.Cron,
				"destination":    spec.Dest,
				"retention":      spec.Retention.String(),
			}
			status["containers"] = append(status["containers"].([]map[string]interface{}), containerInfo)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(status)
	})

	server := &http.Server{
		Addr:    ":8080",
		Handler: hmux,
	}

	go func() {
		logger.Log.Info("Serving HTTP endpoints on :8080", zap.String("addr", ":8080"))
		if err := server.ListenAndServe(); err != nil {
			if err != http.ErrServerClosed {
				logger.Log.Error("HTTP server failed", zap.Error(err))
			} else {
				logger.Log.Info("HTTP server closed gracefully.")
			}
		}
	}()

	defer func() {
		logger.Log.Info("Shutting down HTTP server...")
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Log.Error("HTTP server shutdown failed", zap.Error(err))
		} else {
			logger.Log.Info("HTTP server shutdown completed")
		}
	}()

	logger.Log.Info("Discovery watcher and scheduler started. Monitoring containers...")

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	reconcileInterval := DefaultReconcileIntervalSeconds
	if intervalStr := os.Getenv(EnvReconcileIntervalSeconds); intervalStr != "" {
		if intervalSec, errConv := strconv.Atoi(intervalStr); errConv == nil && intervalSec > 0 {
			reconcileInterval = intervalSec
			logger.Log.Info("Using custom reconcile interval from env", zap.Int("seconds", reconcileInterval))
		} else {
			logger.Log.Warn("Invalid RECONCILE_INTERVAL_SECONDS value, using default",
				zap.String("value", intervalStr),
				zap.Int("defaultSeconds", DefaultReconcileIntervalSeconds),
				zap.Error(errConv),
			)
		}
	}
	reconcileTicker := time.NewTicker(time.Duration(reconcileInterval) * time.Second)
	defer reconcileTicker.Stop()

Loop:
	for {
		select {
		case <-reconcileTicker.C:
			logger.Log.Debug("Reconciling scheduler jobs with discovered containers...")
			currentRegistry := discoveryWatcher.GetRegistry()
			activeScheduledJobs := sched.GetActiveJobsCount()
			logger.Log.Debug("Reconciliation check", zap.Int("discoveredSpecs", len(currentRegistry)), zap.Int("activeJobs", activeScheduledJobs))

			for id, spec := range currentRegistry {
				if spec.Enabled {
					if err := sched.AddOrUpdateJob(id, spec); err != nil {
						logger.Log.Error("Error scheduling job for container", zap.String("containerID", id), zap.Error(err))
					}
				} else {
					sched.RemoveJob(id) 
				}
			}

		case sig := <-sigChan:
			switch sig {
			case syscall.SIGHUP:
				logger.Log.Info("Received SIGHUP, reloading configuration...")
				newConfig := loadGlobalConfig()
				if err := validateConfig(newConfig); err != nil {
					logger.Log.Error("Configuration validation failed during reload", zap.Error(err))
				} else {
					logger.Log.Info("Configuration reloaded successfully")
					
					globalCfgForWriterAndOthers = newConfig
					
					retentionPeriodStr := os.Getenv(EnvGlobalRetentionPeriod)
					globalRetentionPeriod = parseRetentionPeriod(retentionPeriodStr, DefaultGlobalRetentionPeriod)
					
					dryRunStr := strings.ToLower(os.Getenv(EnvGCDryRun))
					gcDryRun = (dryRunStr == "true" || dryRunStr == "1")
					
					logger.Log.Info("Global configuration updated",
						zap.Duration("retentionPeriod", globalRetentionPeriod),
						zap.Bool("gcDryRun", gcDryRun),
					)
					
					logger.Log.Info("Updating components with new configuration...")
					
					webhookSender.Stop()
					
					sched.Stop()
					
					webhookSender = webhook.NewSender(newConfig)
					
					sched = scheduler.NewScheduler(newConfig, webhookSender, discoveryWatcher)
					
					logger.Log.Info("Components updated successfully with new configuration")
				}
				continue
			case syscall.SIGINT, syscall.SIGTERM:
			logger.Log.Info("Shutdown signal received, stopping agent...")
			cancel()
			break Loop
			}
		case <-ctx.Done():
			logger.Log.Info("Context cancelled, stopping agent...")
			break Loop
		}
	}

	logger.Log.Info("Cleaning up components...")
	webhookSender.Stop()
	sched.Stop()
	logger.Log.Info("Label Backup Agent stopped.")
} 