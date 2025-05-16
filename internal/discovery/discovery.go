package discovery

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"label-backup/internal/logger"
	"label-backup/internal/model"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"go.uber.org/zap"
)

// Registry stores the backup specifications for discovered containers.
type Registry map[string]model.BackupSpec

// Watcher continuously monitors Docker events and updates the registry.
type Watcher struct {
	cli      *client.Client
	registry Registry
	mu       sync.Mutex // Added mutex
}

// NewWatcher creates a new Docker event watcher.
func NewWatcher() (*Watcher, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		// If logger.Log is not yet initialized (e.g. fatal error before logger init),
		// this direct fmt.Errorf is okay. Otherwise, prefer logger.
		return nil, fmt.Errorf("failed to create docker client: %w", err)
	}

	w := &Watcher{
		cli:      cli,
		registry: make(Registry),
	}
	logger.Log.Info("Docker event watcher initialized")
	return w, nil
}

// Start watching Docker events.
func (w *Watcher) Start(ctx context.Context) {
	logger.Log.Info("Starting Docker event listener and initial container scan...")

	// Initial scan of existing containers
	containers, err := w.cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		logger.Log.Error("Failed to list existing containers on startup", zap.Error(err))
	} else {
		logger.Log.Info("Found existing containers on startup", zap.Int("count", len(containers)))
		for _, cont := range containers {
			// Check context before long operations or many iterations
			if ctx.Err() != nil {
				logger.Log.Info("Context cancelled during initial container scan")
				return
			}
			containerJSON, inspectErr := w.cli.ContainerInspect(ctx, cont.ID)
			if inspectErr != nil {
				logger.Log.Error("Failed to inspect existing container", zap.String("containerID", cont.ID), zap.Error(inspectErr))
				continue
			}
			w.parseAndRegister(containerJSON)
		}
	}

	// Define event filters
	// Listening for container start, die, and update events to catch label changes.
	eventFilters := filters.NewArgs()
	eventFilters.Add("type", string(events.ContainerEventType))
	// Specific actions can be filtered too, but PRD implies general discovery.
	// e.g., eventFilters.Add("event", "start")
	// eventFilters.Add("event", "die")
	// eventFilters.Add("event", "update") // For label changes

	options := types.EventsOptions{Filters: eventFilters}
	msgs, errs := w.cli.Events(ctx, options)

	logger.Log.Info("Now listening for Docker container events...")
	for {
		select {
		case event := <-msgs:
			w.handleEvent(ctx, event)
		case err := <-errs:
			if err != nil && err != context.Canceled && err.Error() != "context canceled" {
				logger.Log.Error("Error receiving Docker event stream", zap.Error(err))
				// Consider if this is fatal or if we should try to re-establish.
				// For now, if the error stream sends a significant error, we might exit the loop.
				// If it's io.EOF, it might mean Docker daemon stopped, which is also a reason to stop.
				logger.Log.Info("Exiting event listener loop due to error from Docker event stream.")
				return
			} else if err != nil { // context.Canceled or similar
			    logger.Log.Info("Docker event stream error due to context cancellation or closure.", zap.Error(err))
			    return
			}
			// If err is nil, it means the channel was closed, often part of graceful shutdown.
			logger.Log.Info("Docker event message channel closed.")
			return 
		case <-ctx.Done():
			logger.Log.Info("Docker event listener stopping due to context cancellation.")
			return
		}
	}
}

func (w *Watcher) handleEvent(ctx context.Context, event events.Message) {
	// The event.Type check is already done by the filter, but doesn't hurt if we remove filter.
	if event.Type != events.ContainerEventType {
		logger.Log.Debug("Ignoring non-container event", zap.String("eventType", string(event.Type)))
		return
	}

	logger.Log.Debug("Received container event",
		zap.String("action", string(event.Action)),
		zap.String("containerID", event.Actor.ID),
		zap.String("containerName", event.Actor.Attributes["name"]),
	)

	switch string(event.Action) { // event.Action is type events.Action, convert to string for switch
	case "start", "create", "update":
		// For "update", PRD implies this is how label changes are detected.
		// For "create", container might not be fully ready. "start" is more reliable.
		// Debouncing or slight delay for "create" could be an improvement if races occur.
		containerJSON, err := w.cli.ContainerInspect(ctx, event.Actor.ID)
		if err != nil {
			logger.Log.Error("Failed to inspect container on event",
				zap.String("containerID", event.Actor.ID),
				zap.String("eventAction", string(event.Action)),
				zap.Error(err),
			)
			return
		}
		w.parseAndRegister(containerJSON)

	case "destroy", "die", "kill", "stop": 
		w.mu.Lock()
		if _, ok := w.registry[event.Actor.ID]; ok {
			delete(w.registry, event.Actor.ID)
			logger.Log.Info("Unregistered backup spec due to container event",
				zap.String("containerID", event.Actor.ID),
				zap.String("eventAction", string(event.Action)),
			)
		} else {
			logger.Log.Debug("Container event for non-registered container",
				zap.String("containerID", event.Actor.ID),
				zap.String("eventAction", string(event.Action)),
			)
		}
		w.mu.Unlock()
	// Other events like "pause", "unpause" are ignored for now as they don't change backup necessity.
	}
}

func (w *Watcher) parseAndRegister(container types.ContainerJSON) {
	w.mu.Lock()
	defer w.mu.Unlock()

	logger.Log.Debug("Parsing labels for container", zap.String("containerName", container.Name), zap.String("containerID", container.ID))

	spec, ok := parseLabels(container.Config.Labels, container.ID, container.Name)
	if !ok {
		if enabledStr, enabledLabelExists := container.Config.Labels["backup.enabled"]; enabledLabelExists {
			if enabledStr == "false" {
				// If explicitly disabled, ensure it's removed from registry.
				if _, exists := w.registry[container.ID]; exists {
					logger.Log.Info("Container has backup.enabled=false. Unregistering.",
						zap.String("containerID", container.ID),
					)
					delete(w.registry, container.ID)
				}
			} else if enabledStr != "true" {
				// Invalid value for backup.enabled
				if _, exists := w.registry[container.ID]; exists {
					logger.Log.Warn("Container has backup.enabled set to an invalid value. Unregistering.",
						zap.String("containerID", container.ID),
						zap.String("value", enabledStr),
					)
					delete(w.registry, container.ID)
				}
			}
		} else {
			// No backup.enabled label, or it's not "true". If previously registered, remove.
			if _, exists := w.registry[container.ID]; exists {
				logger.Log.Info("Backup labels removed or backup.enabled not 'true'. Unregistering.", zap.String("containerID", container.ID))
				delete(w.registry, container.ID)
			}
		}
		return
	}

	w.registry[container.ID] = spec
	logger.Log.Info("Registered/Updated backup spec for container",
		zap.String("containerID", container.ID),
		// zap.Any("spec", spec) can be verbose, consider logging key fields
		zap.String("dbType", spec.Type),
		zap.String("cron", spec.Cron),
		zap.String("dest", spec.Dest),
		zap.Duration("retention", spec.Retention),
	)
}

// parseRetentionDuration parses a string like "7d", "24h", "30m", or a plain number (for days)
// into a time.Duration. Returns 0 if the string is empty or invalid.
// A negative duration is not allowed and will result in 0.
func parseRetentionDuration(retentionStr string, containerID string) time.Duration {
	value := strings.TrimSpace(retentionStr)
	if value == "" {
		return 0 // No retention override specified, use global
	}

	// Try direct parsing with time.ParseDuration for "h", "m", "s" etc.
	// and combined forms like "1h30m"
	d, err := time.ParseDuration(value)
	if err == nil {
		if d < 0 {
			logger.Log.Warn("Negative retention duration specified, defaulting to 0 (use global)",
				zap.String("containerID", containerID),
				zap.String("value", value),
			)
			return 0
		}
		return d
	}

	// If it failed, it might be a number (days) or a "d" suffixed string
	if strings.HasSuffix(value, "d") {
		daysStr := strings.TrimSuffix(value, "d")
		days, convErr := strconv.Atoi(daysStr)
		if convErr == nil {
			if days < 0 {
				logger.Log.Warn("Negative retention days specified, defaulting to 0 (use global)",
					zap.String("containerID", containerID),
					zap.String("value", value),
				)
				return 0
			}
			return time.Duration(days) * 24 * time.Hour
		}
		// If Atoi failed, it's an invalid format like "xd" where x is not number
		logger.Log.Warn("Invalid retention format with 'd' suffix, defaulting to 0 (use global)",
			zap.String("containerID", containerID),
			zap.String("value", value),
			zap.Error(convErr), // log the original Atoi error
		)
		return 0
	}

	// Try to parse as plain number (interpreting as days)
	days, convErr := strconv.Atoi(value)
	if convErr == nil {
		if days < 0 {
			logger.Log.Warn("Negative retention days specified, defaulting to 0 (use global)",
				zap.String("containerID", containerID),
				zap.String("value", value),
			)
			return 0
		}
		return time.Duration(days) * 24 * time.Hour
	}

	// If all attempts fail, log warning and return 0
	logger.Log.Warn("Invalid retention format, defaulting to 0 (use global). Supported formats: '10h', '30m', '7d', or a number for days.",
		zap.String("containerID", containerID),
		zap.String("value", value),
		zap.Error(err), // log the original time.ParseDuration error
	)
	return 0
}

// parseLabels extracts backup configuration from container labels.
// Returns the BackupSpec and a boolean indicating if a valid (and enabled) backup configuration was found.
func parseLabels(labels map[string]string, containerID, containerName string) (model.BackupSpec, bool) {
	getLabel := func(key, defaultValue string) string {
		if val, ok := labels[key]; ok {
			return strings.TrimSpace(val)
		}
		return defaultValue
	}

	enabledStr := getLabel("backup.enabled", "")
	if enabledStr != "true" {
		if enabledStr != "" { // Log only if label exists and is not "true"
			logger.Log.Debug("Backup not enabled for container or label has non-'true' value",
				zap.String("containerID", containerID),
				zap.String("backup.enabled", enabledStr),
			)
		}
		return model.BackupSpec{}, false
	}

	cron := getLabel("backup.cron", "")
	if cron == "" {
		logger.Log.Warn("backup.cron label is missing or empty, cannot schedule backup for enabled container", zap.String("containerID", containerID))
		return model.BackupSpec{}, false
	}

	typeVal := getLabel("backup.type", "")
	if typeVal == "" {
		logger.Log.Warn("backup.type label is missing or empty, cannot determine dumper for enabled container", zap.String("containerID", containerID))
		return model.BackupSpec{}, false
	}
	typeVal = strings.ToLower(typeVal)

	conn := getLabel("backup.conn", "")
	// Redis might not require a conn string if it connects to localhost default, dumper can handle this.
	if conn == "" && typeVal != "redis" {
		logger.Log.Warn("backup.conn label is missing or empty for enabled container", 
		    zap.String("containerID", containerID), 
		    zap.String("dbType", typeVal),
		)
		// For non-redis, conn is essential.
		return model.BackupSpec{}, false
	}

	retentionStr := getLabel("backup.retention", "")
	retentionDuration := parseRetentionDuration(retentionStr, containerID)

	spec := model.BackupSpec{
		Enabled:       true,
		Type:          typeVal,
		Conn:          conn,
		Database:      getLabel("backup.database", ""),
		Cron:          cron,
		Dest:          strings.ToLower(getLabel("backup.dest", "local")),
		Prefix:        getLabel("backup.prefix", ""),
		Webhook:       getLabel("backup.webhook", ""),
		Retention:     retentionDuration,
		ContainerID:   containerID,
		ContainerName: strings.TrimPrefix(containerName, "/"),
	}
	return spec, true
}

// GetRegistry returns a copy of the current registry.
func (w *Watcher) GetRegistry() Registry {
	w.mu.Lock()
	defer w.mu.Unlock()
	newReg := make(Registry)
	for k, v := range w.registry {
		newReg[k] = v
	}
	logger.Log.Debug("Registry copy accessed", zap.Int("size", len(newReg)))
	return newReg
} 