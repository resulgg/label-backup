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
	"label-backup/internal/model"

	"go.uber.org/zap"
)

const GlobalConfigKeyWebhookURL = "WEBHOOK_URL"
const GlobalConfigKeyWebhookSecret = "WEBHOOK_SECRET"
const GlobalConfigKeyWebhookTimeout = "WEBHOOK_TIMEOUT_SECONDS"
const GlobalConfigKeyWebhookMaxRetries = "WEBHOOK_MAX_RETRIES"
const DefaultWebhookTimeoutSeconds = 10
const DefaultWebhookMaxRetries = 3
const HMACHeaderName = "X-LabelBackup-Signature-SHA256"

type CircuitBreakerState int

const (
	CircuitClosed CircuitBreakerState = iota
	CircuitOpen
	CircuitHalfOpen
)

type CircuitBreaker struct {
	mu                sync.RWMutex
	state             CircuitBreakerState
	failureCount      int
	lastFailureTime   time.Time
	failureThreshold  int
	recoveryTimeout   time.Duration
}

func NewCircuitBreaker(failureThreshold int, recoveryTimeout time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		state:            CircuitClosed,
		failureThreshold: failureThreshold,
		recoveryTimeout:  recoveryTimeout,
	}
}

func (cb *CircuitBreaker) Call(fn func() error) error {
	cb.mu.RLock()
	state := cb.state
	cb.mu.RUnlock()

	switch state {
	case CircuitOpen:
		cb.mu.Lock()
		if time.Since(cb.lastFailureTime) >= cb.recoveryTimeout {
			cb.state = CircuitHalfOpen
			cb.mu.Unlock()
			logger.Log.Info("Circuit breaker transitioning to half-open state")
		} else {
			cb.mu.Unlock()
			return fmt.Errorf("circuit breaker is open")
		}
		fallthrough
	case CircuitHalfOpen:
		err := fn()
		cb.mu.Lock()
		if err != nil {
			cb.failureCount++
			cb.lastFailureTime = time.Now()
			cb.state = CircuitOpen
			cb.mu.Unlock()
			logger.Log.Warn("Circuit breaker call failed, transitioning to open state", zap.Error(err))
			return err
		}
		cb.state = CircuitClosed
		cb.failureCount = 0
		cb.mu.Unlock()
		logger.Log.Info("Circuit breaker call succeeded, transitioning to closed state")
		return nil
	case CircuitClosed:
		err := fn()
		cb.mu.Lock()
		if err != nil {
			cb.failureCount++
			cb.lastFailureTime = time.Now()
			if cb.failureCount >= cb.failureThreshold {
				cb.state = CircuitOpen
				logger.Log.Warn("Circuit breaker failure threshold reached, transitioning to open state", 
					zap.Int("failureCount", cb.failureCount),
					zap.Int("threshold", cb.failureThreshold),
				)
			}
		} else {
			cb.failureCount = 0
		}
		cb.mu.Unlock()
		return err
	}
	return nil
}

type WebhookSender interface {
	Enqueue(payload NotificationPayload, backupSpec model.BackupSpec)
	Stop()
}

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

type workItem struct {
	payload     NotificationPayload
	targetURL   string
	secret      string
	containerID string 
	dbType      string
}

type Sender struct {
	httpClient     *http.Client
	globalURL      string
	globalSecret   string
	maxRetries     int
	queue          chan workItem
	stopChan       chan struct{}
	wg             sync.WaitGroup
	circuitBreaker *CircuitBreaker
}

var _ WebhookSender = (*Sender)(nil)

func extractHost(urlString string) string {
	if urlString == "" {
		return "unknown_host"
	}
	u, err := url.Parse(urlString)
	if err != nil || u.Hostname() == "" {
		logger.Log.Debug("Failed to parse hostname from URL for metrics label", zap.String("url", urlString), zap.Error(err))
		return "unknown_host"
	}
	return u.Hostname()
}

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
		globalURL:      globalConfig[GlobalConfigKeyWebhookURL],
		globalSecret:   globalConfig[GlobalConfigKeyWebhookSecret],
		maxRetries:     maxRetries,
		queue:          make(chan workItem, 100),
		stopChan:       make(chan struct{}),
		circuitBreaker: NewCircuitBreaker(5, 5*time.Minute), 
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

		actualSecret := s.globalSecret

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

func (s *Sender) worker() {
	defer s.wg.Done()
	logger.Log.Info("Webhook worker started.")
	for {
		select {
			case item, ok := <-s.queue:
				if !ok {
					logger.Log.Info("Webhook queue closed, draining remaining items...")
					for remainingItem := range s.queue {
						logFields := []zap.Field{
							zap.String("containerID", remainingItem.containerID),
							zap.String("dbType", remainingItem.dbType),
							zap.String("targetURL", remainingItem.targetURL),
						}
						logger.Log.Debug("Worker processing remaining webhook", logFields...)
						s.sendWithRetries(remainingItem.payload, remainingItem.targetURL, remainingItem.secret, logFields)
					}
					logger.Log.Info("Webhook worker stopped after draining queue.")
					return
				}
			logFields := []zap.Field{
				zap.String("containerID", item.containerID),
				zap.String("dbType", item.dbType),
				zap.String("targetURL", item.targetURL),
			}
			logger.Log.Debug("Worker picked up webhook for processing", logFields...)
				s.sendWithRetries(item.payload, item.targetURL, item.secret, logFields)
		case <-s.stopChan:
			logger.Log.Info("Webhook worker stopping.")
			return
		}
	}
}

func (s *Sender) sendWithRetries(payload NotificationPayload, targetURL, secretKey string, baseLogFields []zap.Field) {
	if targetURL == "" {
	    logger.Log.Warn("Webhook send attempt skipped: no target URL.", baseLogFields...)
	    return
	}

	targetHost := extractHost(targetURL)
	
	err := s.circuitBreaker.Call(func() error {
	var lastErr error
	for attempt := 0; attempt <= s.maxRetries; attempt++ {
		currentAttemptFields := append(baseLogFields, zap.Int("attempt", attempt+1), zap.Int("maxAttempts", s.maxRetries+1), zap.String("targetHost", targetHost))
			lastErr = s.sendAttempt(payload, targetURL, secretKey, targetHost)
		if lastErr == nil {
			logger.Log.Info("Webhook sent successfully", currentAttemptFields...)
				return nil
		}
		logger.Log.Warn("Webhook send attempt failed", append(currentAttemptFields, zap.Error(lastErr))...)
		if attempt < s.maxRetries {
				backoffDuration := time.Duration(2<<attempt) * time.Second
				if backoffDuration > 10*time.Second {
					backoffDuration = 10 * time.Second
				}
			logger.Log.Info("Retrying webhook...", append(currentAttemptFields, zap.Duration("backoff", backoffDuration))...)
			time.Sleep(backoffDuration)
		}
	}
		return lastErr
	})
	
	if err != nil {
		logger.Log.Error("Webhook failed after circuit breaker protection", append(baseLogFields, zap.String("targetHost", targetHost), zap.Error(err))...)
}
}

func (s *Sender) sendAttempt(payload NotificationPayload, targetURL string, secretKey string, _ string) error {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal webhook payload: %w", err)
	}

		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, targetURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create webhook request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "LabelBackupAgent/1.0")

	if secretKey != "" {
		hmacHash := hmac.New(sha256.New, []byte(secretKey))
			hmacHash.Write(jsonData) 
		req.Header.Set(HMACHeaderName, hex.EncodeToString(hmacHash.Sum(nil)))
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP request failed for webhook to %s: %w", targetURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			bodyBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, 1024*64)) 
		if readErr != nil {
			logger.Log.Warn("Failed to read error response body from webhook", zap.String("targetURL", targetURL), zap.String("status", resp.Status), zap.Error(readErr))
		}
		return fmt.Errorf("webhook request to %s returned non-2xx status: %s. Body: %s", targetURL, resp.Status, string(bodyBytes))
	}

	
		_, _ = io.Copy(io.Discard, resp.Body)
	logger.Log.Debug("Webhook response successful", zap.String("status", resp.Status))
	return nil
}

func (s *Sender) Stop() {
	logger.Log.Info("Stopping webhook sender...")
		close(s.queue) 
	s.wg.Wait()       
	logger.Log.Info("Webhook sender stopped.")
} 