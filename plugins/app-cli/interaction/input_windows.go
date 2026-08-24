//go:build windows

package interactioncomponent

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"
)

const waitPollMilliseconds = 50

var readConsoleW = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReadConsoleW")
var peekNamedPipe = windows.NewLazySystemDLL("kernel32.dll").NewProc("PeekNamedPipe")

type terminalInput struct {
	file         *os.File
	handle       windows.Handle
	console      bool
	pipe         bool
	originalMode uint32
	echo         io.Writer
	pending      []byte
}

func newTerminalInput(file *os.File, echo io.Writer) (inputDriver, error) {
	driver := &terminalInput{file: file, handle: windows.Handle(file.Fd()), echo: echo}
	var mode uint32
	if err := windows.GetConsoleMode(driver.handle, &mode); err == nil {
		driver.console = true
		driver.originalMode = mode
		mode &^= windows.ENABLE_LINE_INPUT | windows.ENABLE_ECHO_INPUT
		if err := windows.SetConsoleMode(driver.handle, mode); err != nil {
			return nil, err
		}
	} else if fileType, _ := windows.GetFileType(driver.handle); fileType == windows.FILE_TYPE_PIPE {
		driver.pipe = true
	}
	return driver, nil
}

func supportsColor(file *os.File) bool {
	var mode uint32
	if err := windows.GetConsoleMode(windows.Handle(file.Fd()), &mode); err != nil {
		return false
	}
	return mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0
}

func (d *terminalInput) ReadLine(ctx context.Context, maxBytes int) (string, error) {
	if !d.console {
		return d.readPipeLine(ctx, maxBytes)
	}
	runes := make([]rune, 0, 128)
	bytesUsed := 0
	over := false
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		status, err := windows.WaitForSingleObject(d.handle, waitPollMilliseconds)
		if err != nil {
			return "", err
		}
		if status == uint32(windows.WAIT_TIMEOUT) {
			continue
		}
		unit, err := d.readConsoleUnit()
		if err != nil {
			return "", err
		}
		switch unit {
		case '\r', '\n':
			_, _ = io.WriteString(d.echo, "\r\n")
			if over {
				return "", ErrInputLimit
			}
			return string(runes), nil
		case '\b':
			if len(runes) > 0 && !over {
				last := runes[len(runes)-1]
				runes = runes[:len(runes)-1]
				bytesUsed -= utf8.RuneLen(last)
				_, _ = io.WriteString(d.echo, "\b \b")
			}
			continue
		case 0x1a:
			if len(runes) == 0 {
				return "", io.EOF
			}
		}
		r := rune(unit)
		if utf16.IsSurrogate(r) {
			second, err := d.readConsoleUnit()
			if err != nil {
				return "", err
			}
			r = utf16.DecodeRune(r, rune(second))
			if r == utf8.RuneError {
				return "", ErrInvalidInput
			}
		}
		if !over {
			width := utf8.RuneLen(r)
			if width < 0 || bytesUsed > maxBytes-width {
				over = true
			} else {
				runes = append(runes, r)
				bytesUsed += width
				_, _ = io.WriteString(d.echo, string(r))
			}
		}
	}
}

func (d *terminalInput) readConsoleUnit() (uint16, error) {
	var unit uint16
	var read uint32
	result, _, callErr := readConsoleW.Call(uintptr(d.handle), uintptr(unsafe.Pointer(&unit)), 1, uintptr(unsafe.Pointer(&read)), 0)
	if result == 0 {
		return 0, callErr
	}
	if read == 0 {
		return 0, io.EOF
	}
	return unit, nil
}

func (d *terminalInput) readPipeLine(ctx context.Context, maxBytes int) (string, error) {
	for {
		if index := indexByte(d.pending, '\n'); index >= 0 {
			line := append([]byte(nil), d.pending[:index]...)
			d.pending = append(d.pending[:0], d.pending[index+1:]...)
			if len(line) > maxBytes {
				return "", ErrInputLimit
			}
			return strings.TrimSuffix(string(line), "\r"), nil
		}
		if len(d.pending) > maxBytes {
			for {
				eof, err := d.waitReadable(ctx)
				if err != nil {
					return "", err
				}
				if eof {
					d.pending = nil
					return "", ErrInputLimit
				}
				buffer := make([]byte, 4096)
				n, readErr := d.file.Read(buffer)
				if index := indexByte(buffer[:n], '\n'); index >= 0 {
					d.pending = append(d.pending[:0], buffer[index+1:n]...)
					return "", ErrInputLimit
				}
				if readErr != nil {
					return "", ErrInputLimit
				}
			}
		}
		eof, err := d.waitReadable(ctx)
		if err != nil {
			return "", err
		}
		if eof {
			if len(d.pending) == 0 {
				return "", io.EOF
			}
			line := string(d.pending)
			d.pending = nil
			return strings.TrimSuffix(line, "\r"), nil
		}
		buffer := make([]byte, 4096)
		n, readErr := d.file.Read(buffer)
		if n > 0 {
			d.pending = append(d.pending, buffer[:n]...)
		}
		if readErr != nil {
			if readErr != io.EOF {
				return "", readErr
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

func (d *terminalInput) waitReadable(ctx context.Context) (bool, error) {
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if d.pipe {
			var available uint32
			result, _, callErr := peekNamedPipe.Call(uintptr(d.handle), 0, 0, 0, uintptr(unsafe.Pointer(&available)), 0)
			if result == 0 {
				if errors.Is(callErr, windows.ERROR_BROKEN_PIPE) {
					return true, nil
				}
				return false, callErr
			}
			if available > 0 {
				return false, nil
			}
			timer := time.NewTimer(waitPollMilliseconds * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return false, ctx.Err()
			case <-timer.C:
			}
			continue
		}
		status, err := windows.WaitForSingleObject(d.handle, waitPollMilliseconds)
		if err != nil {
			return false, err
		}
		if status != uint32(windows.WAIT_TIMEOUT) {
			return false, nil
		}
	}
}

func (d *terminalInput) Close() error {
	if d.console {
		return windows.SetConsoleMode(d.handle, d.originalMode)
	}
	return nil
}

func indexByte(value []byte, target byte) int {
	for i, candidate := range value {
		if candidate == target {
			return i
		}
	}
	return -1
}
