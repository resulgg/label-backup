package logger

import (
	"fmt"
	"os"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var Log *zap.Logger

func getLogLevelFromEnv() zapcore.Level {
	levelStr := strings.ToLower(os.Getenv("LOG_LEVEL"))
	switch levelStr {
	case "debug":
		return zapcore.DebugLevel
	case "info":
		return zapcore.InfoLevel
	case "warn", "warning":
		return zapcore.WarnLevel
	case "error":
		return zapcore.ErrorLevel
	case "dpanic":
		return zapcore.DPanicLevel
	case "panic":
		return zapcore.PanicLevel
	case "fatal":
		return zapcore.FatalLevel
	default:
		// Default to InfoLevel if an invalid or empty string is provided
		return zapcore.InfoLevel
	}
}

func init() {
	// Default to production logger, can be made configurable (e.g., via env var for dev logging)
	config := zap.NewProductionEncoderConfig()
	config.EncodeTime = zapcore.ISO8601TimeEncoder
	// config.TimeKey = "timestamp" // Default is "ts"
	// config.LevelKey = "level"
	// config.NameKey = "logger"
	// config.CallerKey = "caller"
	// config.MessageKey = "msg"
	// config.StacktraceKey = "stacktrace"

	consoleEncoder := zapcore.NewConsoleEncoder(config) // Human-readable for console
	// jsonEncoder := zapcore.NewJSONEncoder(config) // JSON for structured logging systems

	// Determine log level from environment variable
	logLevel := getLogLevelFromEnv()

	// For now, log to stdout. Could be file, or multi-writer.
	core := zapcore.NewCore(consoleEncoder, zapcore.Lock(os.Stdout), logLevel)
	
	// Add caller info. Can be expensive, consider for prod.
	Log = zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))

	// Replace global log for packages that use it (e.g. cron library if it uses global log)
	// zap.ReplaceGlobals(Log)

	Log.Info("Zap logger initialized.", zap.String("configuredLogLevel", logLevel.String()))
}

// For convenience, if some parts of the code prefer a sugared logger.
func Sugared() *zap.SugaredLogger {
	return Log.Sugar()
}

// CronZapLogger adapts a zap.Logger to cron.Logger interface.
// cron.Logger interface expects: Info(msg string, keysAndValues ...interface{}) and Error(err error, msg string, keysAndValues ...interface{})
type CronZapLogger struct {
	logger *zap.Logger
}

// NewCronZapLogger creates a new adapter for cron logging.
func NewCronZapLogger(logger *zap.Logger) *CronZapLogger {
	return &CronZapLogger{logger: logger}
}

// Info logs an informational message.
func (czl *CronZapLogger) Info(msg string, keysAndValues ...interface{}) {
	fields := czl.formatKeysAndValues(keysAndValues...)
	czl.logger.Info(msg, fields...)
}

// Error logs an error message.
func (czl *CronZapLogger) Error(err error, msg string, keysAndValues ...interface{}) {
	fields := czl.formatKeysAndValues(keysAndValues...)
	fields = append(fields, zap.Error(err))
	czl.logger.Error(msg, fields...)
}

// formatKeysAndValues converts a list of key-value pairs to Zap fields.
// It expects keys to be strings.
func (czl *CronZapLogger) formatKeysAndValues(keysAndValues ...interface{}) []zap.Field {
	var fields []zap.Field
	for i := 0; i < len(keysAndValues); i += 2 {
		key, ok := keysAndValues[i].(string)
		if !ok {
			// Handle error or skip: if key is not string, Zap will panic
			// For simplicity, convert to string or skip
			key = fmt.Sprintf("unknown_key_%d", i/2)
		}
		if i+1 < len(keysAndValues) {
			fields = append(fields, zap.Any(key, keysAndValues[i+1]))
		} else {
			// Handle odd number of KVs, Zap would also handle this with a specific field
			fields = append(fields, zap.Any(key, "<missing_value>"))
		}
	}
	return fields
} 