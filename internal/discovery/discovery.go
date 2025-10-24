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

type Registry map[string]model.BackupSpec

type Watcher struct {
	cli               *client.Client
	registry          Registry
	mu                sync.RWMutex
	reconnectBackoff  time.Duration
	maxBackoff        time.Duration
}

func NewWatcher() (*Watcher, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create docker client: %w", err)
	}

	w := &Watcher{
		cli:              cli,
		registry:         make(Registry),
		reconnectBackoff: 1 * time.Second,
		maxBackoff:       30 * time.Second,
	}
	logger.Log.Info("Docker event watcher initialized")
	return w, nil
}

func (w *Watcher) Start(ctx context.Context) {
	logger.Log.Info("Starting Docker event listener and initial container scan...")

	for {
		select {
		case <-ctx.Done():
			logger.Log.Info("Docker watcher stopping due to context cancellation")
			return
		default:
			if err := w.startEventLoop(ctx); err != nil {
				logger.Log.Error("Docker event loop failed, attempting reconnection", zap.Error(err))
				
				select {
				case <-ctx.Done():
					return
				case <-time.After(w.reconnectBackoff):
					w.reconnectBackoff *= 2
					if w.reconnectBackoff > w.maxBackoff {
						w.reconnectBackoff = w.maxBackoff
					}
					continue
				}
			}
			return
		}
	}
}

func (w *Watcher) startEventLoop(ctx context.Context) error {
	if err := w.TestDockerConnection(ctx); err != nil {
		return fmt.Errorf("docker connection test failed: %w", err)
	}

	w.reconnectBackoff = 1 * time.Second

	containers, err := w.cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return fmt.Errorf("failed to list existing containers: %w", err)
	}
	
		logger.Log.Info("Found existing containers on startup", zap.Int("count", len(containers)))
		for _, cont := range containers {
			if ctx.Err() != nil {
				logger.Log.Info("Context cancelled during initial container scan")
				return ctx.Err()
			}
			inspectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			containerJSON, inspectErr := w.cli.ContainerInspect(inspectCtx, cont.ID)
			cancel()
			if inspectErr != nil {
				logger.Log.Error("Failed to inspect existing container", zap.String("containerID", cont.ID), zap.Error(inspectErr))
				continue
			}
			w.parseAndRegister(containerJSON)
	}

	eventFilters := filters.NewArgs()
	eventFilters.Add("type", string(events.ContainerEventType))

	options := types.EventsOptions{Filters: eventFilters}
	msgs, errs := w.cli.Events(ctx, options)

	logger.Log.Info("Now listening for Docker container events...")
	for {
		select {
		case event := <-msgs:
			w.handleEvent(ctx, event)
		case err := <-errs:
			if err != nil && err != context.Canceled && err.Error() != "context canceled" {
				return fmt.Errorf("docker event stream error: %w", err)
			} else if err != nil {
				return err
			}
			logger.Log.Info("Docker event message channel closed")
			return nil
		case <-ctx.Done():
			logger.Log.Info("Docker event listener stopping due to context cancellation")
			return ctx.Err()
		}
	}
}

func (w *Watcher) TestDockerConnection(ctx context.Context) error {
	_, err := w.cli.Ping(ctx)
	if err != nil {
		return fmt.Errorf("docker ping failed: %w", err)
	}
	logger.Log.Debug("Docker connection test successful")
	return nil
}

func (w *Watcher) handleEvent(ctx context.Context, event events.Message) {
	if event.Type != events.ContainerEventType {
		logger.Log.Debug("Ignoring non-container event", zap.String("eventType", string(event.Type)))
		return
	}

	logger.Log.Debug("Received container event",
		zap.String("action", string(event.Action)),
		zap.String("containerID", event.Actor.ID),
		zap.String("containerName", event.Actor.Attributes["name"]),
	)

	switch string(event.Action) {
	case "start", "create", "update":
		inspectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		containerJSON, err := w.cli.ContainerInspect(inspectCtx, event.Actor.ID)
		cancel()
		if err != nil {
			logger.Log.Error("Failed to inspect container on event",
				zap.String("containerID", event.Actor.ID),
				zap.String("containerName", event.Actor.Attributes["name"]),
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
				if _, exists := w.registry[container.ID]; exists {
					logger.Log.Info("Container has backup.enabled=false. Unregistering.",
						zap.String("containerID", container.ID),
					)
					delete(w.registry, container.ID)
				}
			} else if enabledStr != "true" {
				if _, exists := w.registry[container.ID]; exists {
					logger.Log.Warn("Container has backup.enabled set to an invalid value. Unregistering.",
						zap.String("containerID", container.ID),
						zap.String("value", enabledStr),
					)
					delete(w.registry, container.ID)
				}
			}
		} else {
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
		zap.String("dbType", spec.Type),
		zap.String("cron", spec.Cron),
		zap.String("dest", spec.Dest),
		zap.Duration("retention", spec.Retention),
	)
}

func parseRetentionDuration(retentionStr string, containerID string) time.Duration {
	value := strings.TrimSpace(retentionStr)
	if value == "" {
		return 0
	}

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
		logger.Log.Warn("Invalid retention format with 'd' suffix, defaulting to 0 (use global)",
			zap.String("containerID", containerID),
			zap.String("value", value),
			zap.Error(convErr),
		)
		return 0
	}

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

	logger.Log.Warn("Invalid retention format, defaulting to 0 (use global). Supported formats: '10h', '30m', '7d', or a number for days.",
		zap.String("containerID", containerID),
		zap.String("value", value),
		zap.Error(err),
	)
	return 0
}

func validateLabelValues(spec *model.BackupSpec, containerID string) error {
	// Validate dest
	if spec.Dest != "" && spec.Dest != "local" && spec.Dest != "remote" {
		return fmt.Errorf("invalid backup.dest value '%s': must be 'local' or 'remote'", spec.Dest)
	}

	// Validate type
	validTypes := map[string]bool{
		"postgres": true,
		"mysql":    true,
		"mongodb":  true,
		"redis":    true,
	}
	if !validTypes[spec.Type] {
		return fmt.Errorf("invalid backup.type value '%s': must be one of postgres, mysql, mongodb, redis", spec.Type)
	}

	// Basic cron validation (at least 5 fields)
	cronFields := strings.Fields(spec.Cron)
	if len(cronFields) < 5 {
		return fmt.Errorf("invalid backup.cron value '%s': must have at least 5 fields", spec.Cron)
	}

	return nil
}

func parseLabels(labels map[string]string, containerID, containerName string) (model.BackupSpec, bool) {
	getLabel := func(key, defaultValue string) string {
		if val, ok := labels[key]; ok {
			return strings.TrimSpace(val)
		}
		return defaultValue
	}

	enabledStr := getLabel("backup.enabled", "")
	if enabledStr != "true" {
		if enabledStr != "" {
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
	if conn == "" && typeVal != "redis" {
		logger.Log.Warn("backup.conn label is missing or empty for enabled container", 
		    zap.String("containerID", containerID), 
		    zap.String("dbType", typeVal),
		)
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

	// Validate label values
	if err := validateLabelValues(&spec, containerID); err != nil {
		logger.Log.Warn("Invalid label values for container",
			zap.String("containerID", containerID),
			zap.Error(err),
		)
		return model.BackupSpec{}, false
	}

	return spec, true
}

func (w *Watcher) GetRegistry() Registry {
	w.mu.RLock()
	defer w.mu.RUnlock()
	
	// Return a copy to prevent external modification
	registryCopy := make(Registry)
	for k, v := range w.registry {
		registryCopy[k] = v
	}
	return registryCopy
}

func (w *Watcher) Close() error {
	if w.cli != nil {
		return w.cli.Close()
	}
	return nil
} 