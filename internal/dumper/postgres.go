package dumper

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"label-backup/internal/logger"
	"label-backup/internal/model"

	"go.uber.org/zap"
)

const PostgresDumperType = "postgres"

type PostgresDumper struct {
	spec model.BackupSpec
}

type postgresConnParams struct {
	Host     string
	Port     string
	User     string
	Password string
	DBName   string
}

func parsePostgresURI(connStr string) (*postgresConnParams, error) {
	params := &postgresConnParams{}

	if !strings.HasPrefix(connStr, "postgresql://") && !strings.HasPrefix(connStr, "postgres://") {
		return nil, fmt.Errorf("invalid PostgreSQL connection URI: must start with postgresql:// or postgres://, got %s", connStr)
	}

	u, err := url.Parse(connStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse PostgreSQL connection URI '%s': %w", connStr, err)
	}

	params.Host = u.Hostname()
	if u.Port() != "" {
		params.Port = u.Port()
	} else {
		params.Port = "5432"
	}

	if u.User != nil {
		params.User = u.User.Username()
		if pass, ok := u.User.Password(); ok {
			params.Password = pass
		}
	}

	if u.Path != "" {
		params.DBName = strings.TrimPrefix(u.Path, "/")
	}

	if params.Host == "" {
		return nil, fmt.Errorf("host missing in PostgreSQL connection URI: %s", connStr)
	}
	if params.DBName == "" {
		return nil, fmt.Errorf("database name missing in PostgreSQL connection URI path: %s", connStr)
	}

	return params, nil
}

func init() {
	RegisterDumperFactory(PostgresDumperType, NewPostgresDumper)
}

func NewPostgresDumper(spec model.BackupSpec) (Dumper, error) {
	if spec.Type != PostgresDumperType {
		err := fmt.Errorf("invalid dumper type for postgres: %s", spec.Type)
		logger.Log.Error("Failed to create new PostgresDumper",
			zap.String("expectedType", PostgresDumperType),
			zap.String("providedType", spec.Type),
			zap.Error(err),
		)
		return nil, err
	}
	return &PostgresDumper{spec: spec}, nil
}

func (d *PostgresDumper) Dump(ctx context.Context, spec model.BackupSpec, writer io.Writer) error {
	params, err := parsePostgresURI(spec.Conn)
	if err != nil {
		logger.Log.Error("PostgreSQL dump failed: could not parse connection string",
			zap.String("containerID", spec.ContainerID),
			zap.String("connectionString", spec.Conn),
			zap.Error(err),
		)
		return err
	}

	args := []string{}
	loggedArgs := []string{}

	if params.Host != "" {
		args = append(args, "-h", params.Host)
		loggedArgs = append(loggedArgs, "-h", params.Host)
	}
	if params.Port != "" {
		args = append(args, "-p", params.Port)
		loggedArgs = append(loggedArgs, "-p", params.Port)
	}
	if params.User != "" {
		args = append(args, "-U", params.User)
		loggedArgs = append(loggedArgs, "-U", params.User)
	}

	args = append(args, "-Fc")
	loggedArgs = append(loggedArgs, "-Fc")

	if params.DBName != "" {
		args = append(args, params.DBName)
		loggedArgs = append(loggedArgs, params.DBName)
	} else {
		err := fmt.Errorf("database name is required for pg_dump")
		logger.Log.Error("PostgreSQL dump failed",
			zap.String("containerID", spec.ContainerID),
			zap.Error(err),
		)
		return err
	}
	
	cmd := exec.CommandContext(ctx, "pg_dump", args...)

	if params.Password != "" {
		cmd.Env = append(os.Environ(), "PGPASSWORD="+params.Password)
	}

	logger.Log.Info("Executing pg_dump",
		zap.String("containerID", spec.ContainerID),
		zap.String("command", "pg_dump"),
		zap.Strings("args", loggedArgs),
		zap.String("targetDatabase", params.DBName),
		zap.Bool("pgpassword_set", params.Password != ""),
	)

	return StreamAndGzip(ctx, cmd, writer)
} 

func (d *PostgresDumper) TestConnection(ctx context.Context, spec model.BackupSpec) error {
	params, err := parsePostgresURI(spec.Conn)
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
	if params.User != "" {
		args = append(args, "-U", params.User)
	}
	args = append(args, "-d", params.DBName)
	args = append(args, "-c", "SELECT 1;")

	cmd := exec.CommandContext(ctx, "psql", args...)
	
	if params.Password != "" {
		cmd.Env = append(os.Environ(), "PGPASSWORD="+params.Password)
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	logger.Log.Debug("Testing PostgreSQL connection",
		zap.String("containerID", spec.ContainerID),
		zap.String("host", params.Host),
		zap.String("port", params.Port),
		zap.String("user", params.User),
		zap.String("database", params.DBName),
	)

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("connection test failed for PostgreSQL: %w (stderr: %s)", err, stderrBuf.String())
	}

	logger.Log.Debug("PostgreSQL connection test successful", zap.String("containerID", spec.ContainerID))
	return nil
} 