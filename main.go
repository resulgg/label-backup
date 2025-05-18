package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"net/http"
	"os"
	"os/signal" // Added for sync.Once
	"syscall"
	"time"

	"label-backup/internal/discovery"
	"label-backup/internal/gc"
	"label-backup/internal/logger" // Import custom logger
	"label-backup/internal/scheduler"
	"label-backup/internal/webhook"
	"label-backup/internal/writer"

	"github.com/robfig/cron/v3"
	"go.uber.org/zap" // For direct use for zap.String etc.
)

const (
	DefaultReconcileIntervalSeconds = 10
	EnvReconcileIntervalSeconds   = "RECONCILE_INTERVAL_SECONDS"
	EnvGlobalRetentionPeriod      = "GLOBAL_RETENTION_PERIOD"
	DefaultGlobalRetentionPeriod  = "7d" // Default global retention: 7 days
	EnvGCDryRun                   = "GC_DRY_RUN"
)

var globalRetentionPeriod time.Duration // Parsed global retention period
var gcDryRun bool                     // Parsed GC dry run flag

// parseRetentionPeriod parses a string like "7d", "24h", "30m", or a plain number (for days)
// into a time.Duration. Uses a default if the string is empty or invalid.
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
			// Fallback to parse default again
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
				d, _ = time.ParseDuration(defaultValue) // Assumes default is always valid
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
		zap.Error(err), // Original time.ParseDuration error
	)
	d, _ = time.ParseDuration(defaultValue) // Assumes default is always valid
	return d
}

// loadGlobalConfig loads configuration from environment variables.
func loadGlobalConfig() map[string]string {
	cfg := make(map[string]string)

	// Helper function to get and trim quotes from env var
	getTrimmedEnv := func(key string) string {
		val := os.Getenv(key)
		return strings.Trim(val, "\\\"")
	}

	// S3 Configuration
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
	// AWS SDK v2 uses specific environment variables for credentials by default (AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY).
	// However, to allow overriding them with potentially different names if the user desires (as per this request),
	// we'll read them explicitly and store them under our defined keys.
	// The S3 writer will then need to be adapted to use these from the config map if present.
	if accessKeyID := getTrimmedEnv("ACCESS_KEY_ID"); accessKeyID != "" {
		cfg[writer.GlobalConfigKeyS3AccessKeyID] = accessKeyID
		logger.Log.Info("Using S3 access key ID from env") // Don't log the key itself
	}
	if secretAccessKey := getTrimmedEnv("SECRET_ACCESS_KEY"); secretAccessKey != "" {
		cfg[writer.GlobalConfigKeyS3SecretAccessKey] = secretAccessKey
		logger.Log.Info("Using S3 secret access key from env") // Don't log the secret
	}

	// Local Writer Configuration
	if localPath := getTrimmedEnv("LOCAL_BACKUP_PATH"); localPath != "" {
		cfg[writer.GlobalConfigKeyLocalPath] = localPath
		logger.Log.Info("Using local backup path from env", zap.String("path", localPath))
	} else {
		cfg[writer.GlobalConfigKeyLocalPath] = writer.DefaultLocalPath // Use default from writer pkg
		logger.Log.Info("LOCAL_BACKUP_PATH not set, using default", zap.String("path", writer.DefaultLocalPath))
	}

	// Global Retention Period
	retentionPeriodStr := os.Getenv(EnvGlobalRetentionPeriod)
	globalRetentionPeriod = parseRetentionPeriod(retentionPeriodStr, DefaultGlobalRetentionPeriod)
	logger.Log.Info("Using global retention period", zap.Duration("period", globalRetentionPeriod))

	// GC Dry Run
	dryRunStr := strings.ToLower(os.Getenv(EnvGCDryRun))
	gcDryRun = (dryRunStr == "true" || dryRunStr == "1")
	logger.Log.Info("GC Dry Run mode", zap.Bool("enabled", gcDryRun))

	// Add other global configurations here (e.g., webhook URLs, retention days if global)
	return cfg
}

// runGlobalGC iterates through all known backup specs and runs GC for each.
func runGlobalGC(ctx context.Context, discoveryWatcher *discovery.Watcher, writerCfg map[string]string, retentionPeriodForGC time.Duration, isDryRun bool) {
	logger.Log.Info("Starting nightly global Garbage Collection run...")
	activeSpecs := discoveryWatcher.GetRegistry() // Get a snapshot of current specs

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
			// Errors from RunGC itself are already logged within it, but we can log a summary error here.
			logger.Log.Error("Global GC: Error during GC run for spec", 
			    zap.String("containerID", containerID), 
			    zap.String("prefix", spec.Prefix), 
			    zap.Error(err),
			)
		}
	}
	logger.Log.Info("Nightly global Garbage Collection run finished.")
}

func main() {
	logger.Log.Info("Label Backup Agent starting...")

	globalCfgForWriterAndOthers := loadGlobalConfig()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	discoveryWatcher, err := discovery.NewWatcher() 
	if err != nil {
		logger.Log.Fatal("Failed to initialize discovery watcher", zap.Error(err))
	}

	webhookSender := webhook.NewSender(globalCfgForWriterAndOthers) 
	defer webhookSender.Stop()

	sched := scheduler.NewScheduler(globalCfgForWriterAndOthers, webhookSender, discoveryWatcher) 
	defer sched.Stop()

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

	go func() {
		logger.Log.Info("Serving HTTP endpoints on :8080", zap.String("addr", ":8080"))
		if err := http.ListenAndServe(":8080", hmux); err != nil {
			if err != http.ErrServerClosed {
				logger.Log.Error("HTTP server failed", zap.Error(err))
			} else {
				logger.Log.Info("HTTP server closed gracefully.")
			}
		}
	}()

	logger.Log.Info("Discovery watcher and scheduler started. Monitoring containers...")

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Configure reconcile interval
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
					sched.RemoveJob(id) // removeJob should also log with Zap
				}
			}

		case <-sigChan:
			logger.Log.Info("Shutdown signal received, stopping agent...")
			cancel()
			break Loop
		case <-ctx.Done():
			logger.Log.Info("Context cancelled, stopping agent...")
			break Loop
		}
	}

	logger.Log.Info("Label Backup Agent stopped.")
} 