package logger

import (
	"fmt"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type LogLevel string

const (
	DebugLevel LogLevel = "debug"
	InfoLevel  LogLevel = "info"
	WarnLevel  LogLevel = "warn"
	ErrorLevel LogLevel = "error"
)

type StructuredError struct {
	Type        string            `json:"type"`
	Message     string            `json:"message"`
	Context     map[string]string `json:"context,omitempty"`
	ContainerID string            `json:"container_id,omitempty"`
	Operation   string            `json:"operation,omitempty"`
	Timestamp   time.Time         `json:"timestamp"`
}

func (e *StructuredError) Error() string {
	return e.Message
}

func NewStructuredError(errorType, message string) *StructuredError {
	return &StructuredError{
		Type:      errorType,
		Message:   message,
		Context:   make(map[string]string),
		Timestamp: time.Now(),
	}
}

func (e *StructuredError) WithContext(key, value string) *StructuredError {
	e.Context[key] = value
	return e
}

func (e *StructuredError) WithContainerID(containerID string) *StructuredError {
	e.ContainerID = containerID
	return e
}

func (e *StructuredError) WithOperation(operation string) *StructuredError {
	e.Operation = operation
	return e
}

func LogStructuredError(err *StructuredError) {
	fields := []zap.Field{
		zap.String("error_type", err.Type),
		zap.String("error_message", err.Message),
		zap.String("operation", err.Operation),
		zap.Time("timestamp", err.Timestamp),
	}
	
	if err.ContainerID != "" {
		fields = append(fields, zap.String("container_id", err.ContainerID))
	}
	
	if len(err.Context) > 0 {
		fields = append(fields, zap.Any("context", err.Context))
	}
	
	if err.Type == "critical" || err.Type == "fatal" {
		fields = append(fields, zap.Stack("stack"))
	}
	
	Log.Error("Structured error occurred", fields...)
}

var Log *zap.Logger

func getLogLevelFromEnv() zapcore.Level {
	levelStr := strings.ToLower(os.Getenv("LOG_LEVEL"))
	var level zapcore.Level
	
	switch levelStr {
	case "debug":
		level = zapcore.DebugLevel
	case "info":
		level = zapcore.InfoLevel
	case "warn", "warning":
		level = zapcore.WarnLevel
	case "error":
		level = zapcore.ErrorLevel
	case "dpanic":
		level = zapcore.DPanicLevel
	case "panic":
		level = zapcore.PanicLevel
	case "fatal":
		level = zapcore.FatalLevel
	default:
		level = zapcore.InfoLevel
		if levelStr != "" && levelStr != "info" {
			fmt.Fprintf(os.Stderr, "Warning: Invalid LOG_LEVEL '%s', using INFO\n", levelStr)
	}
	}
	
	return level
}

func init() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", r)
			Log = zap.NewNop()
		}
	}()

	config := zap.NewProductionEncoderConfig()
	config.EncodeTime = zapcore.ISO8601TimeEncoder

	consoleEncoder := zapcore.NewConsoleEncoder(config)

	logLevel := getLogLevelFromEnv()

	core := zapcore.NewCore(consoleEncoder, zapcore.Lock(os.Stdout), logLevel)
	
	Log = zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))

	Log.Info("Zap logger initialized.", zap.String("configuredLogLevel", logLevel.String()))
}

func Sugared() *zap.SugaredLogger {
	return Log.Sugar()
}

func Close() error {
	if Log != nil {
		return Log.Sync()
	}
	return nil
}

type CronZapLogger struct {
	logger *zap.Logger
}

func NewCronZapLogger(logger *zap.Logger) *CronZapLogger {
	return &CronZapLogger{logger: logger}
}

func (czl *CronZapLogger) Info(msg string, keysAndValues ...interface{}) {
	fields := czl.formatKeysAndValues(keysAndValues...)
	czl.logger.Info(msg, fields...)
}

func (czl *CronZapLogger) Debug(msg string, keysAndValues ...interface{}) {
	fields := czl.formatKeysAndValues(keysAndValues...)
	czl.logger.Debug(msg, fields...)
}

func (czl *CronZapLogger) Warn(msg string, keysAndValues ...interface{}) {
	fields := czl.formatKeysAndValues(keysAndValues...)
	czl.logger.Warn(msg, fields...)
}

func (czl *CronZapLogger) Error(err error, msg string, keysAndValues ...interface{}) {
	fields := czl.formatKeysAndValues(keysAndValues...)
	fields = append(fields, zap.Error(err))
	czl.logger.Error(msg, fields...)
}

func (czl *CronZapLogger) formatKeysAndValues(keysAndValues ...interface{}) []zap.Field {
	var fields []zap.Field
	
	if len(keysAndValues)%2 != 0 {
		czl.logger.Warn("Odd number of arguments passed to logger", 
			zap.Int("count", len(keysAndValues)),
			zap.Any("args", keysAndValues),
		)
	}
	
	for i := 0; i < len(keysAndValues); i += 2 {
		key, ok := keysAndValues[i].(string)
		if !ok {
			key = fmt.Sprintf("unknown_key_%d", i/2)
		}
		if i+1 < len(keysAndValues) {
			fields = append(fields, zap.Any(key, keysAndValues[i+1]))
		} else {
			fields = append(fields, zap.Any(key, "<missing_value>"))
		}
	}
	return fields
} 