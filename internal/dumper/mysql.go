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

const MySQLDumperType = "mysql"

type MySQLDumper struct {
	spec model.BackupSpec
}

type mysqlConnParams struct {
	User     string
	Password string
	Host     string
	Port     string
	DBName   string
	SSLMode  string
}

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
		params.Port = "3306"
	}

	if parsedURL.User != nil {
		params.User = parsedURL.User.Username()
		if pass, ok := parsedURL.User.Password(); ok {
			params.Password = pass
		}
	}

	if parsedURL.Path != "" {
		params.DBName = strings.TrimPrefix(parsedURL.Path, "/")
	}

	queryParams := parsedURL.Query()
	params.SSLMode = queryParams.Get("sslmode")

	if params.Host == "" {
		return nil, fmt.Errorf("host missing in MySQL connection URI: %s", connStr)
	}
	return params, nil
}

func init() {
	RegisterDumperFactory(MySQLDumperType, NewMySQLDumper)
}

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
	loggedArgs := []string{} 

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

	cmd := exec.CommandContext(ctx, "mariadb-dump", args...)

	if params.Password != "" {
		cmd.Env = append(os.Environ(), "MYSQL_PWD="+params.Password)
	}

	if strings.ToLower(params.SSLMode) == "disable" || strings.ToLower(params.SSLMode) == "disabled" {
		args = append(args, "--ssl=0")
		loggedArgs = append(loggedArgs, "--ssl=0")
	} else if params.SSLMode != "" {
		logger.Log.Warn("MySQL/MariaDB dumper received an sslmode that is not 'disabled'. If SSL is required and not implicitly handled by the server/client, this might fail or require specific SSL flags.",
			zap.String("containerID", spec.ContainerID),
			zap.String("sslMode", params.SSLMode),
		)
	}

	args = append(args, "--single-transaction")
	args = append(args, "--routines")
	args = append(args, "--triggers")
	args = append(args, "--skip-lock-tables")

	dbToDump := ""
	if spec.Database != "" { 
		dbToDump = spec.Database
	} else if params.DBName != "" { 
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

	cmd = exec.CommandContext(ctx, "mariadb-dump", args...)
	
	// Set password via environment variable for security
	if params.Password != "" {
		cmd.Env = append(os.Environ(), "MYSQL_PWD="+params.Password)
	}

	logger.Log.Info("Executing mariadb-dump",
		zap.String("containerID", spec.ContainerID),
		zap.String("command", "mariadb-dump"),
		zap.Strings("args", loggedArgs),
		zap.String("targetDatabase", dbToDump),
		zap.String("parsedSSLModeFromURI", params.SSLMode),
	)

	return StreamAndGzip(ctx, cmd, writer)
}

func (d *MySQLDumper) TestConnection(ctx context.Context, spec model.BackupSpec) error {
	params, err := parseMySQLURI(spec.Conn)
	if err != nil {
		return fmt.Errorf("failed to parse connection URI: %w", err)
	}

	args := []string{}
	if params.Host != "" {
		args = append(args, "-h", params.Host)
	}
	if params.Port != "" {
		args = append(args, "-P", params.Port)
	}
	if params.User != "" {
		args = append(args, "-u", params.User)
	}
	
	dbToTest := ""
	if spec.Database != "" {
		dbToTest = spec.Database
	} else if params.DBName != "" {
		dbToTest = params.DBName
	}
	
	if dbToTest != "" {
		args = append(args, dbToTest)
	}
	
	args = append(args, "-e", "SELECT 1;")

	cmd := exec.CommandContext(ctx, "mysql", args...)

	if params.Password != "" {
		cmd.Env = append(os.Environ(), "MYSQL_PWD="+params.Password)
	}

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	logger.Log.Debug("Testing MySQL connection",
		zap.String("containerID", spec.ContainerID),
		zap.String("host", params.Host),
		zap.String("port", params.Port),
		zap.String("user", params.User),
		zap.String("database", dbToTest),
	)

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("connection test failed for MySQL: %w (stderr: %s)", err, stderrBuf.String())
	}

	logger.Log.Debug("MySQL connection test successful", zap.String("containerID", spec.ContainerID))
	return nil
}