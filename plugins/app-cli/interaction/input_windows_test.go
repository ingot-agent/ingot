//go:build windows

package interactioncomponent

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWindowsPipeInputDiscardsOversizeLine(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})
	driver := &terminalInput{file: reader, handle: windowsHandle(reader), pipe: true}
	writeDone := make(chan error, 1)
	go func() {
		_, writeErr := writer.Write([]byte("oversize\nok\n"))
		writeDone <- writeErr
	}()
	if _, err := driver.ReadLine(context.Background(), 3); !errors.Is(err, ErrInputLimit) {
		t.Fatalf("oversize ReadLine() error=%v", err)
	}
	line, err := driver.ReadLine(context.Background(), 3)
	if err != nil || line != "ok" {
		t.Fatalf("next ReadLine() line=%q error=%v", line, err)
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
}

func TestWindowsPipeInputObservesCancellation(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})
	driver := &terminalInput{file: reader, handle: windowsHandle(reader), pipe: true}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := driver.ReadLine(ctx, 64); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ReadLine() error=%v", err)
	}
}

func windowsHandle(file *os.File) windows.Handle {
	return windows.Handle(file.Fd())
}
