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

type Dumper interface {
	Dump(ctx context.Context, spec model.BackupSpec, writer io.Writer) error
	
	TestConnection(ctx context.Context, spec model.BackupSpec) error
}

type NewDumperFunc func(spec model.BackupSpec) (Dumper, error)

var dumperFactories = make(map[string]NewDumperFunc)

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

func GetDumper(spec model.BackupSpec) (Dumper, error) {
	factory, ok := dumperFactories[spec.Type]
	if !ok {
		err := fmt.Errorf("no dumper registered for database type: %s", spec.Type)
		logger.Log.Error("Failed to get dumper: no factory registered",
			zap.String("dbType", spec.Type),
			zap.String("containerID", spec.ContainerID),
			zap.Error(err),
		)
		return nil, err
	}
	return factory(spec)
}

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
	defer gw.Close()

	if err := cmd.Start(); err != nil {
		wrappedErr := fmt.Errorf("failed to start dump command: %s: %w", cmd.Path, err)
		logger.Log.Error("StreamAndGzip: failed to start dump command", append(logFields, zap.Error(err))...)
		return wrappedErr
	}
	logger.Log.Info("StreamAndGzip: Started command", logFields...)

	var copyErr error
	var wg sync.WaitGroup
	wg.Add(1)
	
	go func() {
		defer wg.Done()
		
		buffer := make([]byte, 32*1024)
		for {
			select {
			case <-ctx.Done():
				logger.Log.Info("StreamAndGzip: Context cancelled, stopping copy", logFields...)
				return
			default:
				n, err := stdoutPipe.Read(buffer)
				if n > 0 {
					if _, writeErr := gw.Write(buffer[:n]); writeErr != nil {
						copyErr = writeErr
						return
					}
				}
				if err != nil {
					if err != io.EOF {
						copyErr = err
					}
					return
				}
			}
		}
	}()

	stderrOutput, _ := io.ReadAll(stderrPipe)

	wg.Wait()

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