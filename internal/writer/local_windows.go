//go:build windows

package writer

import (
	"fmt"
	"syscall"
	"unsafe"

	"label-backup/internal/logger"

	"go.uber.org/zap"
)

var (
	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	procGetDiskFreeSpaceExW = kernel32.NewProc("GetDiskFreeSpaceExW")
)

func checkDiskSpaceImpl(path string) error {
	var freeBytesAvailable, totalNumberOfBytes, totalNumberOfFreeBytes uint64

	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return fmt.Errorf("failed to convert path to UTF16: %w", err)
	}

	ret, _, err := procGetDiskFreeSpaceExW.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		uintptr(unsafe.Pointer(&freeBytesAvailable)),
		uintptr(unsafe.Pointer(&totalNumberOfBytes)),
		uintptr(unsafe.Pointer(&totalNumberOfFreeBytes)),
	)

	if ret == 0 {
		return fmt.Errorf("GetDiskFreeSpaceExW failed: %w", err)
	}

	if totalNumberOfBytes == 0 {
		return fmt.Errorf("invalid filesystem: total bytes is 0")
	}

	freePercentage := float64(freeBytesAvailable) / float64(totalNumberOfBytes) * 100
	if freePercentage < 10.0 {
		return fmt.Errorf("insufficient disk space: %.2f%% free (minimum 10%% required)", freePercentage)
	}

	logger.Log.Debug("Disk space check passed", 
		zap.String("path", path),
		zap.Float64("freePercentage", freePercentage),
		zap.Uint64("freeBytesAvailable", freeBytesAvailable),
		zap.Uint64("totalNumberOfBytes", totalNumberOfBytes),
	)
	return nil
}
