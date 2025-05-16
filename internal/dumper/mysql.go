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

const MySQLDumperType = "mysql"

// MySQLDumper implements the Dumper interface for MySQL databases.
type MySQLDumper struct {
	spec model.BackupSpec
}

// mysqlConnParams holds parsed URI components for MySQL.
type mysqlConnParams struct {
	User     string
	Password string
	Host     string
	Port     string
	DBName   string
	SSLMode  string
}

// parseMySQLURI parses a MySQL connection URI.
// Expected format: mysql://[user[:password]@]host[:port]/dbname[?param1=value1&...]
func parseMySQLURI(connStr string) (*mysqlConnParams, error) {
	params := &mysqlConnParams{}

	if !strings.HasPrefix(connStr, "mysql://") {
		return nil, fmt.Errorf("invalid MySQL connection URI: must start with mysql://, got %s", connStr)
	}

	parsedURL, err := url.Parse(connStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse MySQL connection URI '%s': %w", connStr, err)
	}

	params.Host = parsedURL.Hostname()
	if parsedURL.Port() != "" {
		params.Port = parsedURL.Port()
	} else {
		params.Port = "3306" // Default MySQL port
	}

	if parsedURL.User != nil {
		params.User = parsedURL.User.Username()
		if pass, ok := parsedURL.User.Password(); ok {
			params.Password = pass
		}
	}

	// Database name is the path component, without the leading slash.
	if parsedURL.Path != "" {
		params.DBName = strings.TrimPrefix(parsedURL.Path, "/")
	}

	queryParams := parsedURL.Query()
	params.SSLMode = queryParams.Get("sslmode") // e.g. "disable"

	if params.Host == "" {
		return nil, fmt.Errorf("host missing in MySQL connection URI: %s", connStr)
	}
	return params, nil
}

func init() {
	RegisterDumperFactory(MySQLDumperType, NewMySQLDumper)
}

// NewMySQLDumper creates a new MySQLDumper.
func NewMySQLDumper(spec model.BackupSpec) (Dumper, error) {
	if spec.Type != MySQLDumperType {
		err := fmt.Errorf("invalid dumper type for mysql: %s", spec.Type)
		logger.Log.Error("Failed to create new MySQLDumper",
			zap.String("expectedType", MySQLDumperType),
			zap.String("providedType", spec.Type),
			zap.Error(err),
		)
		return nil, err
	}
	return &MySQLDumper{spec: spec}, nil
}

// Dump executes mariadb-dump (provided by mysql-client in Alpine) for the MySQL database.
func (d *MySQLDumper) Dump(ctx context.Context, spec model.BackupSpec, writer io.Writer) error {
	params, err := parseMySQLURI(spec.Conn)
	if err != nil {
		logger.Log.Error("MySQL dump failed: could not parse connection URI",
			zap.String("containerID", spec.ContainerID),
			zap.String("connectionURI", spec.Conn),
			zap.Error(err),
		)
		return fmt.Errorf("failed to parse MySQL connection URI '%s': %w", spec.Conn, err)
	}

	args := []string{}
	loggedArgs := []string{} // For logging, with password masked

	// Connection parameters
	if params.Host != "" {
		args = append(args, "--host="+params.Host)
		loggedArgs = append(loggedArgs, "--host="+params.Host)
	}
	if params.Port != "" {
		args = append(args, "--port="+params.Port)
		loggedArgs = append(loggedArgs, "--port="+params.Port)
	}
	if params.User != "" {
		args = append(args, "--user="+params.User)
		loggedArgs = append(loggedArgs, "--user="+params.User)
	}
	if params.Password != "" {
		args = append(args, "--password="+params.Password)
		loggedArgs = append(loggedArgs, "--password=***")
	}

	// Handle SSLMode for mariadb-dump
	if strings.ToLower(params.SSLMode) == "disable" || strings.ToLower(params.SSLMode) == "disabled" {
		args = append(args, "--ssl=0")
		loggedArgs = append(loggedArgs, "--ssl=0")
	} else if params.SSLMode != "" {
		logger.Log.Warn("MySQL/MariaDB dumper received an sslmode that is not 'disabled'. If SSL is required and not implicitly handled by the server/client, this might fail or require specific SSL flags.",
			zap.String("containerID", spec.ContainerID),
			zap.String("sslMode", params.SSLMode),
		)
	}

	// Standard mysqldump/mariadb-dump options
	args = append(args, "--single-transaction")
	args = append(args, "--routines")
	args = append(args, "--triggers")
	args = append(args, "--skip-lock-tables")

	// Determine database to dump
	dbToDump := ""
	if spec.Database != "" { // Explicit label takes precedence
		dbToDump = spec.Database
	} else if params.DBName != "" { // From URI path
		dbToDump = params.DBName
	} else {
		err := fmt.Errorf("no database specified in URI path or backup.database label for MySQL dump")
		logger.Log.Error("MySQL dump configuration error",
			zap.String("containerID", spec.ContainerID),
			zap.String("connectionURI", spec.Conn),
			zap.Error(err),
		)
		return err
	}
	args = append(args, dbToDump)
	loggedArgs = append(loggedArgs, dbToDump)

	// Use mariadb-dump, as that's what mysql-client in Alpine provides
	cmd := exec.CommandContext(ctx, "mariadb-dump", args...)

	logger.Log.Info("Executing mariadb-dump",
		zap.String("containerID", spec.ContainerID),
		zap.String("command", "mariadb-dump"),
		zap.Strings("args", loggedArgs),
		zap.String("targetDatabase", dbToDump),
		zap.String("parsedSSLModeFromURI", params.SSLMode),
	)

	return StreamAndGzip(ctx, cmd, writer)
}