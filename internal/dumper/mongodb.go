package dumper

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"label-backup/internal/logger"
	"label-backup/internal/model"

	"go.uber.org/zap"
)

const MongoDBDumperType = "mongodb"

type MongoDBDumper struct {
	spec model.BackupSpec
}

func init() {
	RegisterDumperFactory(MongoDBDumperType, NewMongoDBDumper)
}

func NewMongoDBDumper(spec model.BackupSpec) (Dumper, error) {
	if spec.Type != MongoDBDumperType {
		err := fmt.Errorf("invalid dumper type for mongodb: %s", spec.Type)
		logger.Log.Error("Failed to create new MongoDBDumper",
			zap.String("expectedType", MongoDBDumperType),
			zap.String("providedType", spec.Type),
			zap.Error(err),
		)
		return nil, err
	}
	return &MongoDBDumper{spec: spec}, nil
}

func (d *MongoDBDumper) Dump(ctx context.Context, spec model.BackupSpec, writer io.Writer) error {
	

	args := []string{}
	loggedArgs := []string{}

	if spec.Conn != "" {
		args = append(args, fmt.Sprintf("--uri=%s", spec.Conn))
		maskedURI := spec.Conn
		if strings.Contains(maskedURI, "@") && (strings.HasPrefix(maskedURI, "mongodb://") || strings.HasPrefix(maskedURI, "mongodb+srv://")) {
			schemaEnd := strings.Index(maskedURI, "://")
			credEnd := strings.Index(maskedURI, "@")
			if schemaEnd != -1 && credEnd != -1 && credEnd > schemaEnd+3 {
				maskedURI = maskedURI[:schemaEnd+3] + "<credentials>" + maskedURI[credEnd:]
			}
		}
		loggedArgs = append(loggedArgs, fmt.Sprintf("--uri=%s", maskedURI))
	} else {
		logger.Log.Error("MongoDB connection string (spec.Conn) is empty",
			zap.String("containerID", spec.ContainerID),
		)
		return fmt.Errorf("mongodb connection string (spec.Conn) is empty for container %s", spec.ContainerID)
	}

	dbToDump := ""
	if spec.Database != "" {
		if !strings.Contains(spec.Conn, "/"+spec.Database+"?") && !strings.HasSuffix(spec.Conn, "/"+spec.Database) {
			args = append(args, "--db="+spec.Database)
			loggedArgs = append(loggedArgs, "--db="+spec.Database)
		}
		dbToDump = spec.Database
	} else {
		uriParts := strings.Split(spec.Conn, "/")
		if len(uriParts) > 1 {
			lastPart := uriParts[len(uriParts)-1]
			qmarkIdx := strings.Index(lastPart, "?")
			if qmarkIdx != -1 {
				dbToDump = lastPart[:qmarkIdx]
			} else {
				dbToDump = lastPart
			}
		}
		if dbToDump == "" {
			logger.Log.Warn("MongoDB database not specified in spec.Database and not clearly parsable from the end of spec.Conn. mongodump might backup all DBs or fail if a DB is required by the URI.",
				zap.String("containerID", spec.ContainerID),
				zap.String("connectionString", spec.Conn),
			)
		}
	}

	args = append(args, "--archive")
	loggedArgs = append(loggedArgs, "--archive")

	cmd := exec.CommandContext(ctx, "mongodump", args...)

	logger.Log.Info("Executing mongodump",
		zap.String("containerID", spec.ContainerID),
		zap.String("command", "mongodump"),
		zap.Strings("args", loggedArgs),
		zap.String("targetDatabase", dbToDump),
	)

	return StreamAndGzip(ctx, cmd, writer)
}

func (d *MongoDBDumper) TestConnection(ctx context.Context, spec model.BackupSpec) error {
	if spec.Conn == "" {
		return fmt.Errorf("mongodb connection string is empty")
	}

	dbToTest := ""
	if spec.Database != "" {
		dbToTest = spec.Database
	} else {
		uriParts := strings.Split(spec.Conn, "/")
		if len(uriParts) > 1 {
			lastPart := uriParts[len(uriParts)-1]
			qmarkIdx := strings.Index(lastPart, "?")
			if qmarkIdx != -1 {
				dbToTest = lastPart[:qmarkIdx]
			} else {
				dbToTest = lastPart
			}
		}
	}

	// Use mongodump for connection testing - just check if we can connect
	cmd := exec.CommandContext(ctx, "mongodump", "--uri", spec.Conn, "--out", "/tmp", "--quiet")

	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	logger.Log.Debug("Testing MongoDB connection",
		zap.String("containerID", spec.ContainerID),
		zap.String("database", dbToTest),
	)

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("connection test failed for MongoDB: %w (stderr: %s)", err, stderrBuf.String())
	}

	logger.Log.Debug("MongoDB connection test successful", zap.String("containerID", spec.ContainerID))
	return nil
}