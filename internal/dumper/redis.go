package dumper

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"strings"

	"label-backup/internal/logger"
	"label-backup/internal/model"

	"go.uber.org/zap"
)

const RedisDumperType = "redis"

// RedisDumper implements the Dumper interface for Redis.
type RedisDumper struct {
	spec model.BackupSpec
}

// redisConnParams holds parsed connection details for Redis.
type redisConnParams struct {
	Host     string
	Port     string
	Password string
	DBNum    string // Optional, redis-cli uses -n for DB number
}

// parseRedisConn attempts to extract host, port, and password from spec.Conn.
// spec.Conn for Redis could be:
// - "redis://:password@host:port/dbnum"
// - "host:port"
// - "host" (implies default port)
// - ":password@host:port"
// This is a simplified parser.
func parseRedisConn(connStr string) (*redisConnParams, error) {
	params := &redisConnParams{Port: "6379"} // Default Redis port

	// Attempt to parse as URI first
	if strings.HasPrefix(connStr, "redis://") {
		// Example: redis://:mypassword@myredishost:6380/0
		// Go's url.Parse can handle this well.
		u, err := url.Parse(connStr)
		if err != nil {
			return nil, fmt.Errorf("failed to parse Redis connection URI '%s': %w", connStr, err)
		}
		params.Host = u.Hostname()
		if u.Port() != "" {
			params.Port = u.Port()
		}
		if u.User != nil {
			if pass, ok := u.User.Password(); ok {
				params.Password = pass
			}
		}
		if u.Path != "" && u.Path != "/" {
			params.DBNum = strings.TrimPrefix(u.Path, "/")
		}
	} else {
		// Simple host:port or host parsing, potentially with password
		// Example: "mypassword@myredishost:6380" or "myredishost:6380" or "myredishost"
		tempConnStr := connStr
		if strings.Contains(tempConnStr, "@") {
			parts := strings.SplitN(tempConnStr, "@", 2)
			if strings.HasPrefix(parts[0], ":") { // e.g. :password@host:port
			    params.Password = strings.TrimPrefix(parts[0], ":")
			} else { // For user:password form, though redis-cli only takes password
			    // For simplicity, if not starting with ':', assume it is part of host or user (which we ignore for password only)
			    // This case is ambiguous without a full URI spec, best to use redis:// URI
			    // Or rely on spec.Password if that field existed.
			    // For now, we only take password if it's like :password@...
			}
			tempConnStr = parts[1]
		}

		hostPort := strings.SplitN(tempConnStr, ":", 2)
		params.Host = hostPort[0]
		if len(hostPort) > 1 && hostPort[1] != "" {
			params.Port = hostPort[1]
		}
	}

	if params.Host == "" {
		return nil, fmt.Errorf("failed to parse Redis host from connection string: '%s'", connStr)
	}

	return params, nil
}

func init() {
	RegisterDumperFactory(RedisDumperType, NewRedisDumper)
}

// NewRedisDumper creates a new RedisDumper.
func NewRedisDumper(spec model.BackupSpec) (Dumper, error) {
	if spec.Type != RedisDumperType {
		err := fmt.Errorf("invalid dumper type for redis: %s", spec.Type)
		logger.Log.Error("Failed to create new RedisDumper",
			zap.String("expectedType", RedisDumperType),
			zap.String("providedType", spec.Type),
			zap.Error(err),
		)
		return nil, err
	}
	return &RedisDumper{spec: spec}, nil
}

// Dump executes redis-cli --rdb to get an RDB dump for Redis.
func (d *RedisDumper) Dump(ctx context.Context, spec model.BackupSpec, writer io.Writer) error {
	params, err := parseRedisConn(spec.Conn)
	if err != nil {
		logger.Log.Error("Failed to parse Redis connection string",
			zap.String("containerID", spec.ContainerID),
			zap.String("connStr", spec.Conn),
			zap.Error(err),
		)
		return fmt.Errorf("failed to parse Redis connection string '%s': %w", spec.Conn, err)
	}

	args := []string{}
	if params.Host != "" {
		args = append(args, "-h", params.Host)
	}
	if params.Port != "" {
		args = append(args, "-p", params.Port)
	}
	if params.Password != "" {
		args = append(args, "-a", params.Password)
	}
	if spec.Database != "" { 
		args = append(args, "-n", spec.Database)
	} else if params.DBNum != "" {
	    args = append(args, "-n", params.DBNum)
	}

	args = append(args, "--rdb", "-")

	cmd := exec.CommandContext(ctx, "redis-cli", args...)

	// Create a safe version of args for logging, masking the password.
	safeArgsToLog := make([]string, len(args))
	for i, arg := range args {
		if i > 0 && args[i-1] == "-a" { // If the previous arg was -a, this one is the password.
			safeArgsToLog[i] = "<password_hidden>"
		} else {
			safeArgsToLog[i] = arg
		}
	}

	logger.Log.Info("Executing redis-cli",
		zap.String("containerID", spec.ContainerID),
		zap.String("command", "redis-cli"),
		zap.Strings("args", safeArgsToLog),
	)

	return StreamAndGzip(ctx, cmd, writer)
} 