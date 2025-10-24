package dumper

import (
	"bytes"
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

type RedisDumper struct {
	spec model.BackupSpec
}

type redisConnParams struct {
	Host     string
	Port     string
	Password string
	DBNum    string
}

func parseRedisConn(connStr string) (*redisConnParams, error) {
	params := &redisConnParams{Port: "6379"}

	if strings.HasPrefix(connStr, "redis://") {
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
		tempConnStr := connStr
		if strings.Contains(tempConnStr, "@") {
			parts := strings.SplitN(tempConnStr, "@", 2)
		if strings.HasPrefix(parts[0], ":") {
			    params.Password = strings.TrimPrefix(parts[0], ":")
		} else {
			params.Password = parts[0]
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

	safeArgsToLog := make([]string, len(args))
	for i, arg := range args {
		if i > 0 && args[i-1] == "-a" {
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

func (d *RedisDumper) TestConnection(ctx context.Context, spec model.BackupSpec) error {
	params, err := parseRedisConn(spec.Conn)
	if err != nil {
		return fmt.Errorf("failed to parse connection string: %w", err)
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
	args = append(args, "ping")

	cmd := exec.CommandContext(ctx, "redis-cli", args...)

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	logger.Log.Debug("Testing Redis connection",
		zap.String("containerID", spec.ContainerID),
		zap.String("host", params.Host),
		zap.String("port", params.Port),
		zap.String("database", spec.Database),
	)

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("redis connection test failed: %w (stderr: %s)", err, stderrBuf.String())
	}

	logger.Log.Debug("Redis connection test successful", zap.String("containerID", spec.ContainerID))
	return nil
} 