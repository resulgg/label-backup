package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"label-backup/internal/logger"
	"label-backup/internal/model" // For BackupSpec to get webhook URL and container info

	"go.uber.org/zap"
)

const GlobalConfigKeyWebhookURL = "WEBHOOK_URL"             // Global default webhook URL
const GlobalConfigKeyWebhookSecret = "WEBHOOK_SECRET"         // Global secret for HMAC signing
const GlobalConfigKeyWebhookTimeout = "WEBHOOK_TIMEOUT_SECONDS" // Global timeout for webhook HTTP request
const GlobalConfigKeyWebhookMaxRetries = "WEBHOOK_MAX_RETRIES" // Global max retries for webhook
const DefaultWebhookTimeoutSeconds = 10
const DefaultWebhookMaxRetries = 3
const HMACHeaderName = "X-LabelBackup-Signature-SHA256"

// WebhookSender defines the interface for sending webhooks.
// This allows for easier testing and potential alternative implementations.
type WebhookSender interface {
	Enqueue(payload NotificationPayload, backupSpec model.BackupSpec)
	Stop()
}

// NotificationPayload defines the JSON structure for webhook notifications.
type NotificationPayload struct {
	ContainerID     string  `json:"container_id"`
	ContainerName   string  `json:"container_name"`
	DatabaseType    string  `json:"database_type"`
	DatabaseName    string  `json:"database_name,omitempty"`
	DestinationURL  string  `json:"destination_url"`
	Success         bool    `json:"success"`
	Error           string  `json:"error,omitempty"`
	BackupSize      int64   `json:"backup_size_bytes,omitempty"`
	DurationSeconds float64 `json:"duration_seconds"`
	Timestamp       string  `json:"timestamp_utc"`
	CronSchedule    string  `json:"cron_schedule,omitempty"`
	BackupPrefix    string  `json:"backup_prefix,omitempty"`
	DestinationType string  `json:"destination_type,omitempty"`
}

// workItem is what's actually placed on the queue
type workItem struct {
	payload     NotificationPayload
	targetURL   string
	secret      string // Use the specific secret for this targetURL
	containerID string // For logging in worker, if payload doesn't have it yet
	dbType      string // For logging
}

// Sender handles sending webhook notifications asynchronously with retries.
// It now implements the WebhookSender interface.
type Sender struct {
	httpClient  *http.Client
	globalURL   string
	globalSecret string
	maxRetries  int
	queue       chan workItem
	stopChan    chan struct{}
	wg          sync.WaitGroup
}

// Ensure Sender implements WebhookSender
var _ WebhookSender = (*Sender)(nil)

// extractHost parses a URL string and returns its hostname.
// Returns "unknown_host" if parsing fails or hostname is empty.
func extractHost(urlString string) string {
	if urlString == "" {
		return "unknown_host"
	}
	u, err := url.Parse(urlString)
	if err != nil || u.Hostname() == "" {
		// Log parsing error for diagnostics, but return a safe label
		logger.Log.Debug("Failed to parse hostname from URL for metrics label", zap.String("url", urlString), zap.Error(err))
		return "unknown_host"
	}
	return u.Hostname()
}

// NewSender creates a new webhook Sender.
func NewSender(globalConfig map[string]string) *Sender {
	timeoutSeconds := DefaultWebhookTimeoutSeconds
	if timeoutStr, ok := globalConfig[GlobalConfigKeyWebhookTimeout]; ok {
		if val, err := strconv.Atoi(timeoutStr); err == nil && val > 0 {
			timeoutSeconds = val
		} else if err != nil {
			logger.Log.Warn("Invalid webhook timeout value", zap.String("value", timeoutStr), zap.Error(err), zap.Int("default", DefaultWebhookTimeoutSeconds))
		}
	}

	maxRetries := DefaultWebhookMaxRetries
	if retriesStr, ok := globalConfig[GlobalConfigKeyWebhookMaxRetries]; ok {
		if val, err := strconv.Atoi(retriesStr); err == nil && val >= 0 {
			maxRetries = val
		} else if err != nil {
			logger.Log.Warn("Invalid webhook max_retries value", zap.String("value", retriesStr), zap.Error(err), zap.Int("default", DefaultWebhookMaxRetries))
		}
	}

	s := &Sender{
		httpClient: &http.Client{
			Timeout: time.Duration(timeoutSeconds) * time.Second,
		},
		globalURL:   globalConfig[GlobalConfigKeyWebhookURL],
		globalSecret: globalConfig[GlobalConfigKeyWebhookSecret],
		maxRetries:  maxRetries,
		queue:       make(chan workItem, 100),
		stopChan:    make(chan struct{}),
	}

	s.wg.Add(1)
	go s.worker()

	logger.Log.Info("Webhook Sender initialized.",
		zap.String("globalURL", s.globalURL),
		zap.Int("maxRetries", s.maxRetries),
		zap.Int("timeoutSeconds", timeoutSeconds),
		zap.Bool("hmacSecretConfigured", s.globalSecret != ""),
	)
	return s
}

// Enqueue adds a notification payload to the send queue.
func (s *Sender) Enqueue(payload NotificationPayload, backupSpec model.BackupSpec) {
	targetURL := s.globalURL
	if backupSpec.Webhook != "" {
		targetURL = backupSpec.Webhook
	}

	logFields := []zap.Field{
		zap.String("containerID", payload.ContainerID),
		zap.String("dbType", payload.DatabaseType),
		zap.String("effectiveWebhookURL", targetURL),
	}

	if targetURL == "" {
		logger.Log.Info("Webhook skipped: No target URL configured (global or spec).", logFields...)
		return
	}

	actualSecret := s.globalSecret // Assuming global secret for all for now

	item := workItem{
		payload:     payload,
		targetURL:   targetURL,
		secret:      actualSecret,
		containerID: payload.ContainerID, 
		dbType:      payload.DatabaseType,      
	}

	select {
	case s.queue <- item:
		logger.Log.Info("Enqueued webhook notification", logFields...)
	default:
		logger.Log.Warn("Webhook queue full. Dropping notification.", logFields...)
	}
}

// worker processes notifications from the queue.
func (s *Sender) worker() {
	defer s.wg.Done()
	logger.Log.Info("Webhook worker started.")
	for {
		select {
		case item := <-s.queue:
			logFields := []zap.Field{
				zap.String("containerID", item.containerID),
				zap.String("dbType", item.dbType),
				zap.String("targetURL", item.targetURL),
			}
			logger.Log.Debug("Worker picked up webhook for processing", logFields...)
			s.sendWithRetries(item.payload, item.targetURL, item.secret, logFields) // Pass logFields for context
		case <-s.stopChan:
			logger.Log.Info("Webhook worker stopping.")
			return
		}
	}
}

// sendWithRetries attempts to send the payload, retrying on failure.
func (s *Sender) sendWithRetries(payload NotificationPayload, targetURL, secretKey string, baseLogFields []zap.Field) {
	if targetURL == "" {
	    logger.Log.Warn("Webhook send attempt skipped: no target URL.", baseLogFields...)
	    return
	}

	targetHost := extractHost(targetURL)
	var lastErr error

	for attempt := 0; attempt <= s.maxRetries; attempt++ {
		currentAttemptFields := append(baseLogFields, zap.Int("attempt", attempt+1), zap.Int("maxAttempts", s.maxRetries+1), zap.String("targetHost", targetHost))
		lastErr = s.sendAttempt(payload, targetURL, secretKey, targetHost) // Pass targetHost
		if lastErr == nil {
			logger.Log.Info("Webhook sent successfully", currentAttemptFields...)
			return // Success
		}
		logger.Log.Warn("Webhook send attempt failed", append(currentAttemptFields, zap.Error(lastErr))...)
		if attempt < s.maxRetries {
			backoffDuration := time.Duration(2<<attempt) * time.Second // Exponential backoff (2s, 4s, 8s...)
			logger.Log.Info("Retrying webhook...", append(currentAttemptFields, zap.Duration("backoff", backoffDuration))...)
			time.Sleep(backoffDuration)
		}
	}
	logger.Log.Error("Webhook failed after all retries.", append(baseLogFields, zap.String("targetHost", targetHost), zap.Error(lastErr))...)
}

// sendAttempt performs a single attempt to send the webhook.
func (s *Sender) sendAttempt(payload NotificationPayload, targetURL string, secretKey string, targetHostLabel string) error {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal webhook payload: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(context.Background(), s.httpClient.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, targetURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create webhook request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "LabelBackupAgent/1.0")

	if secretKey != "" {
		hmacHash := hmac.New(sha256.New, []byte(secretKey))
		_, err = hmacHash.Write(jsonData)
		if err != nil {
			logger.Log.Error("HMAC generation failed internally (should not happen)", zap.Error(err))
			return fmt.Errorf("failed to compute HMAC for webhook: %w", err)
		}
		req.Header.Set(HMACHeaderName, hex.EncodeToString(hmacHash.Sum(nil)))
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP request failed for webhook to %s: %w", targetURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			logger.Log.Warn("Failed to read error response body from webhook", zap.String("targetURL", targetURL), zap.String("status", resp.Status), zap.Error(readErr))
		}
		return fmt.Errorf("webhook request to %s returned non-2xx status: %s. Body: %s", targetURL, resp.Status, string(bodyBytes))
	}

	logger.Log.Debug("Webhook response successful", zap.String("status", resp.Status))
	return nil
}

// Stop gracefully shuts down the webhook sender and its worker.
func (s *Sender) Stop() {
	logger.Log.Info("Stopping webhook sender...")
	close(s.stopChan) 
	s.wg.Wait()       
	logger.Log.Info("Webhook sender stopped.")
} 