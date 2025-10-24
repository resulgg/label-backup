package encryption

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"

	"label-backup/internal/logger"

	"go.uber.org/zap"
)

type EncryptedReader struct {
	reader io.Reader
	cmd    *exec.Cmd
	stderr *bytes.Buffer
	ctx    context.Context
}

func (r *EncryptedReader) Read(p []byte) (n int, err error) {
	return r.reader.Read(p)
}

func (r *EncryptedReader) Close() error {
	// Wait for command to finish and check for errors
	err := r.cmd.Wait()
	if err != nil {
		return fmt.Errorf("GPG encryption failed: %w (stderr: %s)", err, r.stderr.String())
	}
	logger.Log.Debug("GPG encryption completed successfully")
	return nil
}

type GPGEncryptor struct {
	publicKeyPath string
	enabled       bool
}

func NewGPGEncryptor(publicKeyPath string) (*GPGEncryptor, error) {
	if publicKeyPath == "" {
		return &GPGEncryptor{enabled: false}, nil
	}

	if _, err := exec.LookPath("gpg"); err != nil {
		return nil, fmt.Errorf("GPG not found in PATH: %w", err)
	}

	if _, err := os.Stat(publicKeyPath); err != nil {
		return nil, fmt.Errorf("public key file not found: %w", err)
	}

	// Validate GPG public key format
	cmd := exec.Command("gpg", "--import", "--dry-run", publicKeyPath)
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("invalid GPG public key: %w", err)
	}

	logger.Log.Info("GPG encryption enabled", zap.String("publicKeyPath", publicKeyPath))
	return &GPGEncryptor{
		publicKeyPath: publicKeyPath,
		enabled:       true,
	}, nil
}

func (e *GPGEncryptor) Encrypt(ctx context.Context, input io.Reader) (io.ReadCloser, error) {
	if !e.enabled {
		return io.NopCloser(input), nil
	}

	cmd := exec.CommandContext(ctx, "gpg", 
		"--encrypt",
		"--recipient-file", e.publicKeyPath,
		"--armor",
		"--batch",
		"--yes",
	)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start GPG command: %w", err)
	}

	// Copy input to stdin with context cancellation
	go func() {
		defer stdin.Close()
		select {
		case <-ctx.Done():
			logger.Log.Debug("GPG encryption cancelled during input copy")
			return
		default:
			if _, err := io.Copy(stdin, input); err != nil {
				logger.Log.Error("Failed to copy data to GPG stdin", zap.Error(err))
			}
		}
	}()

	logger.Log.Debug("GPG encryption started")
	return &EncryptedReader{
		reader: stdout,
		cmd:    cmd,
		stderr: stderr,
		ctx:    ctx,
	}, nil
}

func (e *GPGEncryptor) IsEnabled() bool {
	return e.enabled
}

func (e *GPGEncryptor) GetEncryptedExtension() string {
	if e.enabled {
		return ".gpg"
	}
	return ""
}
