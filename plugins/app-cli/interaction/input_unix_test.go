//go:build unix

package interactioncomponent

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUnixPipeInputDiscardsOversizeLine(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})
	driver := &terminalInput{file: reader}
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

func TestUnixPipeInputObservesCancellation(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})
	driver := &terminalInput{file: reader}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := driver.ReadLine(ctx, 64); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ReadLine() error=%v", err)
	}
}

func TestUnixInputReturnsEOFForNonPipeStreamEnd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input.txt")
	if err := os.WriteFile(path, []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	driver := &terminalInput{file: file}
	line, err := driver.ReadLine(context.Background(), 64)
	if err != nil || line != "hello" {
		t.Fatalf("first ReadLine() line=%q error=%v", line, err)
	}
	if _, err := driver.ReadLine(context.Background(), 64); !errors.Is(err, io.EOF) {
		t.Fatalf("EOF ReadLine() error=%v", err)
	}
}

func TestUnixInputReturnsEOFForEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.txt")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	if _, err := (&terminalInput{file: file}).ReadLine(context.Background(), 64); !errors.Is(err, io.EOF) {
		t.Fatalf("ReadLine() error=%v", err)
	}
}
