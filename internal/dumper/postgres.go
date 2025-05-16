package dumper

import (
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

// PostgresDumper implements the Dumper interface for PostgreSQL databases.
type PostgresDumper struct {
	spec model.BackupSpec
}

// postgresConnParams holds parsed connection details for PostgreSQL.
type postgresConnParams struct {
	Host     string
	Port     string
	User     string
	Password string
	DBName   string
}

// parsePostgresURI parses a PostgreSQL connection URI.
// Example URI: postgresql://user:password@host:port/dbname
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
		params.Port = "5432" // Default PostgreSQL port
	}

	if u.User != nil {
		params.User = u.User.Username()
		if pass, ok := u.User.Password(); ok {
			params.Password = pass
		}
	}

	// Database name is the path component, without the leading slash.
	if u.Path != "" {
		params.DBName = strings.TrimPrefix(u.Path, "/")
	}

	if params.Host == "" {
		return nil, fmt.Errorf("host missing in PostgreSQL connection URI: %s", connStr)
	}
	if params.DBName == "" {
		// Unlike some DBs, pg_dump typically requires a database name unless listing all DBs (pg_dumpall).
		// For a single DB backup, it's generally required.
		return nil, fmt.Errorf("database name missing in PostgreSQL connection URI path: %s", connStr)
	}

	return params, nil
}

func init() {
	RegisterDumperFactory(PostgresDumperType, NewPostgresDumper)
}

// NewPostgresDumper creates a new PostgresDumper.
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

// Dump executes pg_dump for the PostgreSQL database specified in the BackupSpec.
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
		loggedArgs = append(loggedArgs, "-U", params.User) // Username is not a secret
	}

	// Add other pg_dump arguments
	args = append(args, "-Fc") // Custom format
	loggedArgs = append(loggedArgs, "-Fc")

	if params.DBName != "" {
		args = append(args, params.DBName) // DBName is the last positional argument
		loggedArgs = append(loggedArgs, params.DBName)
	} else {
		// This case should ideally be caught by parsePostgresURI,
		// but as a safeguard:
		err := fmt.Errorf("database name is required for pg_dump")
		logger.Log.Error("PostgreSQL dump failed",
			zap.String("containerID", spec.ContainerID),
			zap.Error(err),
		)
		return err
	}
	
	cmd := exec.CommandContext(ctx, "pg_dump", args...)

	// Set PGPASSWORD environment variable for the command
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