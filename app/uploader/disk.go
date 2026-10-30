package uploader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
)

var ErrInsufficientDiskSpace = errors.New("insufficient disk space")

var diskWriteMutex sync.Mutex

func AvailableDiskBytes(directory string) (int64, error) {
	return availableDiskBytes(directory)
}

func CopyWithDiskReserve(ctx context.Context, destination io.Writer, source io.Reader, directory string,
	minimum int64, available func(string) (int64, error)) (int64, error) {
	return io.Copy(diskReserveWriter{
		ctx: ctx, destination: destination, directory: directory, minimum: minimum, available: available,
	}, source)
}

type diskReserveWriter struct {
	ctx         context.Context
	destination io.Writer
	directory   string
	minimum     int64
	available   func(string) (int64, error)
}

func (writer diskReserveWriter) Write(data []byte) (int, error) {
	total := 0
	for len(data) > 0 {
		chunk := data
		if len(chunk) > 32*1024 {
			chunk = chunk[:32*1024]
		}
		written, err := writer.writeChunk(chunk)
		total += written
		if err != nil {
			return total, err
		}
		if written != len(chunk) {
			return total, io.ErrShortWrite
		}
		data = data[written:]
	}
	return total, nil
}

func (writer diskReserveWriter) writeChunk(data []byte) (int, error) {
	diskWriteMutex.Lock()
	defer diskWriteMutex.Unlock()
	if err := writer.ctx.Err(); err != nil {
		return 0, err
	}
	available, err := writer.available(writer.directory)
	if err != nil {
		return 0, fmt.Errorf("check free disk space: %w", err)
	}
	if available < writer.minimum || int64(len(data)) > available-writer.minimum {
		return 0, ErrInsufficientDiskSpace
	}
	return writer.destination.Write(data)
}
