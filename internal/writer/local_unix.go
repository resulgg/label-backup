//go:build !windows

package writer

import (
	"fmt"
	"syscall"

	"label-backup/internal/logger"

	"go.uber.org/zap"
)

func checkDiskSpaceImpl(path string) error {
	var stat syscall.Statfs_t
	err := syscall.Statfs(path, &stat)
	if err != nil {
		return fmt.Errorf("failed to get filesystem stats: %w", err)
	}

	totalBlocks := stat.Blocks
	freeBlocks := stat.Bavail
	if totalBlocks == 0 {
		return fmt.Errorf("invalid filesystem: total blocks is 0")
	}

	freePercentage := float64(freeBlocks) / float64(totalBlocks) * 100
	if freePercentage < 10.0 {
		return fmt.Errorf("insufficient disk space: %.2f%% free (minimum 10%% required)", freePercentage)
	}

	logger.Log.Debug("Disk space check passed", 
		zap.String("path", path),
		zap.Float64("freePercentage", freePercentage),
		zap.Uint64("freeBlocks", freeBlocks),
		zap.Uint64("totalBlocks", totalBlocks),
	)
	return nil
}
