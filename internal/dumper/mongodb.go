package dumper

import (
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

// MongoDBDumper implements the Dumper interface for MongoDB databases.
type MongoDBDumper struct {
	spec model.BackupSpec
}

func init() {
	RegisterDumperFactory(MongoDBDumperType, NewMongoDBDumper)
}

// NewMongoDBDumper creates a new MongoDBDumper.
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

// Dump executes mongodump for the MongoDB database specified in the BackupSpec.
func (d *MongoDBDumper) Dump(ctx context.Context, spec model.BackupSpec, writer io.Writer) error {
	// mongodump --uri="mongodb://user:password@host:port/dbname" --archive
	// --archive writes to standard output. Add --gzip if mongodump supports it directly,
	// otherwise our StreamAndGzip will handle it.
	// mongodump itself can output gzip, so we can use that if available and skip our own gzip.
	// For consistency and to ensure our StreamAndGzip handles all cases (e.g. if native gzip fails or is not available),
	// we will let StreamAndGzip handle the compression.

	args := []string{}
	loggedArgs := []string{}

	// URI is the primary way to connect
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

	// If spec.Database is provided, it might be part of the URI or specified separately.
	// If it's in the URI, mongodump uses it. If not, and spec.Database is set, use --db.
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

	// Output to stdout for piping
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