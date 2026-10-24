package voicetools

import (
	"context"
	"errors"
	"io"
	"math"
	"os"
	"syscall"
)

var (
	ErrFileMissing     = errors.New("voice tools: file missing")
	ErrUnsupportedFile = errors.New("voice tools: unsupported file")
	ErrFileTooLarge    = errors.New("voice tools: file too large")
	ErrStaleFile       = errors.New("voice tools: file changed during read")
	ErrFileRead        = errors.New("voice tools: file read failed")
	ErrBundleMetadata  = errors.New("voice tools: unsupported bundle metadata")
)

// ReadRegularFile reads one bounded regular file without blocking on FIFOs.
// It rejects observed replacement or metadata changes during the read.
func ReadRegularFile(ctx context.Context, path string, maxBytes int64) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if path == "" || maxBytes < 0 || maxBytes == math.MaxInt64 {
		return nil, ErrUnsupportedFile
	}

	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) {
			return nil, ErrFileMissing
		}
		if errors.Is(err, syscall.ELOOP) {
			return nil, ErrUnsupportedFile
		}
		return nil, ErrFileRead
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = syscall.Close(fd)
		return nil, ErrFileRead
	}
	defer file.Close()

	before, err := file.Stat()
	if err != nil {
		return nil, ErrFileRead
	}
	if !before.Mode().IsRegular() {
		return nil, ErrUnsupportedFile
	}
	if before.Size() > maxBytes {
		return nil, ErrFileTooLarge
	}

	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, ErrFileRead
	}
	if int64(len(data)) > maxBytes {
		return nil, ErrFileTooLarge
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	after, err := file.Stat()
	if err != nil {
		return nil, ErrFileRead
	}
	if !sameFileVersion(before, after) || int64(len(data)) != after.Size() {
		return nil, ErrStaleFile
	}
	currentPath, err := os.Stat(path)
	if err != nil || !sameFileVersion(after, currentPath) {
		return nil, ErrStaleFile
	}
	return data, nil
}

func sameFileVersion(left, right os.FileInfo) bool {
	return os.SameFile(left, right) &&
		left.Size() == right.Size() &&
		left.Mode() == right.Mode() &&
		left.ModTime().Equal(right.ModTime())
}
