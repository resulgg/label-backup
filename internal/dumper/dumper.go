package dumper

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"

	"label-backup/internal/logger"
	"label-backup/internal/model"

	"go.uber.org/zap"
)

// Dumper defines the interface for database-specific backup operations.
type Dumper interface {
	// Dump executes the database dump, writing output to the provided writer.
	// The output should be the raw dump data, before any compression.
	Dump(ctx context.Context, spec model.BackupSpec, writer io.Writer) error
}

// NewDumperFunc is a function type that creates a new Dumper.
// It allows for a factory pattern to get specific dumper implementations.
type NewDumperFunc func(spec model.BackupSpec) (Dumper, error)

var dumperFactories = make(map[string]NewDumperFunc)

// RegisterDumperFactory allows different dumper implementations to register themselves.
func RegisterDumperFactory(dbType string, factory NewDumperFunc) {
	if factory == nil {
		logger.Log.Fatal("Dumper factory is nil", zap.String("dbType", dbType))
	}
	if _, DumperFactoryRegistered := dumperFactories[dbType]; DumperFactoryRegistered {
		logger.Log.Fatal("Dumper factory already registered", zap.String("dbType", dbType))
	}
	dumperFactories[dbType] = factory
	logger.Log.Info("Registered dumper factory", zap.String("dbType", dbType))
}

// GetDumper returns a Dumper for the given database type from the BackupSpec.
func GetDumper(spec model.BackupSpec) (Dumper, error) {
	factory, ok := dumperFactories[spec.Type]
	if !ok {
		err := fmt.Errorf("no dumper registered for database type: %s", spec.Type)
		logger.Log.Error("Failed to get dumper: no factory registered",
			zap.String("dbType", spec.Type),
			zap.String("containerID", spec.ContainerID), // Assuming spec has ContainerID
			zap.Error(err),
		)
		return nil, err
	}
	return factory(spec)
}

// StreamAndGzip executes a command, captures its stdout, Gzips it, and writes to the destination writer.
// It also captures stderr for logging.
func StreamAndGzip(ctx context.Context, cmd *exec.Cmd, destWriter io.Writer) error {
	logFields := []zap.Field{
		zap.String("commandPath", cmd.Path),
		zap.Strings("commandArgs", cmd.Args),
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		wrappedErr := fmt.Errorf("failed to create stdout pipe: %w", err)
		logger.Log.Error("StreamAndGzip: failed to create stdout pipe", append(logFields, zap.Error(err))...)
		return wrappedErr
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		wrappedErr := fmt.Errorf("failed to create stderr pipe: %w", err)
		logger.Log.Error("StreamAndGzip: failed to create stderr pipe", append(logFields, zap.Error(err))...)
		return wrappedErr
	}

	gw := gzip.NewWriter(destWriter)
	defer gw.Close() // gw.Close() also flushes

	if err := cmd.Start(); err != nil {
		wrappedErr := fmt.Errorf("failed to start dump command: %s: %w", cmd.Path, err)
		logger.Log.Error("StreamAndGzip: failed to start dump command", append(logFields, zap.Error(err))...)
		return wrappedErr
	}
	logger.Log.Info("StreamAndGzip: Started command", logFields...)

	var copyErr error
	var wg sync.WaitGroup
	wg.Add(1)
	// Goroutine to stream and Gzip stdout
	go func() {
		defer wg.Done()
		_, copyErr = io.Copy(gw, stdoutPipe)
		// gw.Close() is deferred in the main function, which is generally fine.
		// However, if io.Copy finishes, explicitly closing here ensures data is flushed
		// before cmd.Wait() is checked, especially if stdoutPipe closes early.
		if err := gw.Close(); err != nil {
		    // This error might occur if gw was already closed by the defer or if there's an issue flushing.
		    // It's logged, but copyErr or cmd.Wait() error usually take precedence.
		    logger.Log.Warn("StreamAndGzip: error closing gzip writer in goroutine", append(logFields, zap.Error(err))...)
		}
	}()

	// Goroutine to capture stderr
	stderrOutput, _ := io.ReadAll(stderrPipe) // This blocks until stderrPipe is closed (by cmd.Wait)

	wg.Wait() // Wait for io.Copy to finish before checking cmd.Wait() or copyErr

	cmdErr := cmd.Wait()

	if cmdErr != nil {
		stderrStr := string(stderrOutput)
		logger.Log.Error("StreamAndGzip: dump command failed",
			append(logFields,
				zap.Error(cmdErr),
				zap.String("stderr", stderrStr),
			)...)
		return fmt.Errorf("dump command '%s' failed (stderr: %s): %w", cmd.Path, stderrStr, cmdErr)
	}

	// Check error from io.Copy after cmd.Wait() succeeded.
	// This can happen if the command succeeded but writing the output failed.
	if copyErr != nil {
	    logger.Log.Error("StreamAndGzip: error copying stdout to gzip writer", append(logFields, zap.Error(copyErr))...)
	    return fmt.Errorf("error copying stdout to gzip writer after command success: %w", copyErr)
	}

	if len(stderrOutput) > 0 {
		logger.Log.Warn("StreamAndGzip: dump command completed with messages on stderr",
			append(logFields, zap.String("stderr", string(stderrOutput)))...)
	}

	logger.Log.Info("StreamAndGzip: successfully streamed and gzipped output", logFields...)
	return nil
} 