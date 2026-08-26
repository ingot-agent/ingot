//go:build unix

package interactioncomponent

import (
	"context"
	"io"
	"os"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

type terminalInput struct {
	file    *os.File
	pending []byte
}

func newTerminalInput(file *os.File, _ io.Writer) (inputDriver, error) {
	return &terminalInput{file: file}, nil
}

func supportsColor(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func (d *terminalInput) ReadLine(ctx context.Context, maxBytes int) (string, error) {
	discarding := false
	for {
		if index := indexByte(d.pending, '\n'); index >= 0 {
			line := append([]byte(nil), d.pending[:index]...)
			d.pending = append(d.pending[:0], d.pending[index+1:]...)
			if discarding || len(line) > maxBytes {
				return "", ErrInputLimit
			}
			return strings.TrimSuffix(string(line), "\r"), nil
		}
		if len(d.pending) > maxBytes {
			d.pending = d.pending[:0]
			discarding = true
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		poll := []unix.PollFd{{Fd: int32(d.file.Fd()), Events: unix.POLLIN | unix.POLLHUP}}
		count, err := unix.Poll(poll, 50)
		if err != nil {
			if err == syscall.EINTR {
				continue
			}
			return "", err
		}
		if count == 0 {
			continue
		}
		buffer := make([]byte, 4096)
		n, err := unix.Read(int(d.file.Fd()), buffer)
		if n > 0 {
			if discarding {
				if index := indexByte(buffer[:n], '\n'); index >= 0 {
					d.pending = append(d.pending, buffer[index+1:n]...)
					return "", ErrInputLimit
				}
			} else {
				d.pending = append(d.pending, buffer[:n]...)
			}
		}
		if err != nil && err != syscall.EAGAIN && err != syscall.EWOULDBLOCK {
			return "", err
		}
		if n == 0 && err == nil {
			// read returning 0 without error means end of stream. Pipes and
			// TTYs also surface POLLHUP, but regular files and /dev/null do
			// not, so the stream state itself is the reliable EOF signal.
			if discarding || len(d.pending) > maxBytes {
				d.pending = nil
				return "", ErrInputLimit
			}
			if len(d.pending) == 0 {
				return "", io.EOF
			}
			line := string(d.pending)
			d.pending = nil
			return strings.TrimSuffix(line, "\r"), nil
		}
	}
}

func (d *terminalInput) Close() error { return nil }

func indexByte(value []byte, target byte) int {
	for i, candidate := range value {
		if candidate == target {
			return i
		}
	}
	return -1
}
