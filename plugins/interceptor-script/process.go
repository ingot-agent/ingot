package script

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
)

type capturedOutput struct {
	mu        sync.Mutex
	limit     int
	buffer    bytes.Buffer
	truncated bool
}

func (c *capturedOutput) Write(value []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	original := len(value)
	remaining := c.limit - c.buffer.Len()
	if remaining > 0 {
		if len(value) > remaining {
			value = value[:remaining]
			c.truncated = true
		}
		_, _ = c.buffer.Write(value)
	} else if len(value) > 0 {
		c.truncated = true
	}
	return original, nil
}

func (c *capturedOutput) Bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.buffer.Bytes()...)
}

func (c *capturedOutput) Truncated() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.truncated
}

func executeHook(ctx context.Context, hook normalizedHook, input []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithTimeout(ctx, hook.timeout)
	defer cancel()
	command := exec.Command(hook.executable, hook.args...)
	command.Dir = hook.dir
	command.Env = append(make([]string, 0, len(hook.environment)), hook.environment...)
	command.Stdin = bytes.NewReader(input)
	stdout := &capturedOutput{limit: hook.maxOutput}
	stderr := &capturedOutput{limit: hook.maxOutput}
	command.Stdout = stdout
	command.Stderr = stderr
	controller, err := newProcessController(command)
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, errors.Join(err, processCleanupError(controller.Close()))
	}
	if err := controller.Attach(command.Process); err != nil {
		return nil, errors.Join(processCleanupError(err), terminateAndWait(command, controller))
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- command.Wait() }()
	var waitErr error
	select {
	case waitErr = <-waitDone:
	case <-runCtx.Done():
		cleanupErr := terminateProcess(command, controller)
		waitErr = <-waitDone
		closeErr := processCleanupError(controller.Close())
		return nil, errors.Join(runCtx.Err(), cleanupErr, closeErr, waitErr)
	}
	closeErr := processCleanupError(controller.Close())
	if closeErr != nil {
		return nil, closeErr
	}
	if stdout.Truncated() || stderr.Truncated() {
		return nil, fmt.Errorf("hook output exceeded %d bytes", hook.maxOutput)
	}
	stderrBytes := stderr.Bytes()
	if waitErr != nil {
		return nil, fmt.Errorf("hook process: %w; stderr: %s", waitErr, stderrBytes)
	}
	if len(stderrBytes) != 0 {
		return nil, fmt.Errorf("successful hook wrote stderr: %s", stderrBytes)
	}
	return stdout.Bytes(), nil
}

func terminateAndWait(command *exec.Cmd, controller processController) error {
	var cleanupErr error
	if command.Process != nil {
		if err := controller.Terminate(command.Process); err != nil && !errors.Is(err, os.ErrProcessDone) {
			cleanupErr = errors.Join(cleanupErr, err)
		}
		if err := command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			cleanupErr = errors.Join(cleanupErr, err)
		}
		if err := command.Wait(); err != nil && !isExpectedKilledWait(err) {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	if err := controller.Close(); err != nil {
		cleanupErr = errors.Join(cleanupErr, err)
	}
	if cleanupErr != nil {
		return fmt.Errorf("%w: %w", ErrProcessCleanup, cleanupErr)
	}
	return nil
}

func isExpectedKilledWait(err error) bool {
	var exitErr *exec.ExitError
	return errors.Is(err, os.ErrProcessDone) || errors.As(err, &exitErr)
}

func processCleanupError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrProcessCleanup, err)
}

func terminateProcess(command *exec.Cmd, controller processController) error {
	if command.Process == nil {
		return nil
	}
	var cleanupErr error
	if err := controller.Terminate(command.Process); err != nil && !errors.Is(err, os.ErrProcessDone) {
		cleanupErr = errors.Join(cleanupErr, err)
	}
	if err := command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		cleanupErr = errors.Join(cleanupErr, err)
	}
	if cleanupErr != nil {
		return fmt.Errorf("%w: %w", ErrProcessCleanup, cleanupErr)
	}
	return nil
}
