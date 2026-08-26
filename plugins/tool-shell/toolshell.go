// Package toolshell exposes a bounded one-shot shell execution tool.
package toolshell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"os/exec"

	"github.com/ingot-agent/sdk"
	"github.com/ingot-agent/sdk/tool"
)

const (
	defaultTimeoutSeconds  = 120
	defaultMaxOutputBytes  = 1024 * 1024
	outputTruncationMarker = "[output truncated]"
)

var (
	// ErrInvalidConfig indicates invalid tool.shell configuration.
	ErrInvalidConfig = errors.New("invalid tool.shell config")
	// ErrInvalidArguments indicates malformed shell_exec arguments.
	ErrInvalidArguments = errors.New("invalid tool.shell arguments")
	// ErrOutputLimit is reserved for internal output collection failures.
	ErrOutputLimit = errors.New("shell output limit exceeded")
	// ErrProcessCleanup indicates that a terminated process containment primitive
	// could not be confirmed cleanly.
	ErrProcessCleanup     = errors.New("shell process cleanup failed")
	environmentKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// Config fixes the execution boundary for shell commands.
type Config struct {
	WorkingDirectory string            `toml:"working_directory"`
	Shell            string            `toml:"shell"`
	TimeoutSeconds   int               `toml:"timeout_seconds"`
	MaxOutputBytes   int               `toml:"max_output_bytes"`
	Environment      map[string]string `toml:"environment"`
	InheritEnv       []string          `toml:"inherit_env"`
}

// Dependencies is intentionally empty: approval is supplied by a runtime interceptor.
type Dependencies struct{}

// Exports contains the shell_exec tool.
type Exports struct{ Tools []tool.Tool }

type normalizedConfig struct {
	workingDirectory string
	shell            string
	timeout          time.Duration
	maxOutputBytes   int
	environment      []string
}

type shellTool struct{ config normalizedConfig }

// New validates the fixed process boundary and creates shell_exec.
func New(ctx context.Context, cfg Config, _ Dependencies) (Exports, sdk.Cleanup, error) {
	if ctx == nil {
		return Exports{}, nil, fmt.Errorf("construct tool.shell: %w", ErrInvalidConfig)
	}
	if err := ctx.Err(); err != nil {
		return Exports{}, nil, err
	}
	normalized, err := normalizeConfig(cfg)
	if err != nil {
		return Exports{}, nil, err
	}
	return Exports{Tools: []tool.Tool{&shellTool{config: normalized}}}, nil, nil
}

func normalizeConfig(cfg Config) (normalizedConfig, error) {
	if cfg.WorkingDirectory == "" || !filepath.IsAbs(cfg.WorkingDirectory) {
		return normalizedConfig{}, fmt.Errorf("working_directory must be an absolute path: %w", ErrInvalidConfig)
	}
	workingDirectory, err := filepath.Abs(cfg.WorkingDirectory)
	if err != nil {
		return normalizedConfig{}, fmt.Errorf("resolve working_directory: %w: %w", ErrInvalidConfig, err)
	}
	info, err := os.Stat(workingDirectory)
	if err != nil {
		return normalizedConfig{}, fmt.Errorf("stat working_directory: %w: %w", ErrInvalidConfig, err)
	}
	if !info.IsDir() {
		return normalizedConfig{}, fmt.Errorf("working_directory is not a directory: %w", ErrInvalidConfig)
	}
	if cfg.Shell == "" || !filepath.IsAbs(cfg.Shell) {
		return normalizedConfig{}, fmt.Errorf("shell must be an absolute executable path: %w", ErrInvalidConfig)
	}
	shell, err := filepath.Abs(cfg.Shell)
	if err != nil {
		return normalizedConfig{}, fmt.Errorf("resolve shell: %w: %w", ErrInvalidConfig, err)
	}
	shellInfo, err := os.Stat(shell)
	if err != nil {
		return normalizedConfig{}, fmt.Errorf("stat shell: %w: %w", ErrInvalidConfig, err)
	}
	if shellInfo.IsDir() || shellInfo.Mode()&0o111 == 0 && runtime.GOOS != "windows" {
		return normalizedConfig{}, fmt.Errorf("shell is not executable: %w", ErrInvalidConfig)
	}
	timeoutSeconds := cfg.TimeoutSeconds
	if timeoutSeconds == 0 {
		timeoutSeconds = defaultTimeoutSeconds
	}
	if timeoutSeconds < 1 {
		return normalizedConfig{}, fmt.Errorf("timeout_seconds must be positive: %w", ErrInvalidConfig)
	}
	if int64(timeoutSeconds) > (int64(^uint64(0)>>1) / int64(time.Second)) {
		return normalizedConfig{}, fmt.Errorf("timeout_seconds is too large: %w", ErrInvalidConfig)
	}
	maxOutput := cfg.MaxOutputBytes
	if maxOutput == 0 {
		maxOutput = defaultMaxOutputBytes
	}
	if maxOutput < 1 {
		return normalizedConfig{}, fmt.Errorf("max_output_bytes must be positive: %w", ErrInvalidConfig)
	}
	environment, err := normalizeEnvironment(cfg.Environment, cfg.InheritEnv)
	if err != nil {
		return normalizedConfig{}, err
	}
	return normalizedConfig{workingDirectory: workingDirectory, shell: shell, timeout: time.Duration(timeoutSeconds) * time.Second, maxOutputBytes: maxOutput, environment: environment}, nil
}

func normalizeEnvironment(explicit map[string]string, inherited []string) ([]string, error) {
	seen := make(map[string]struct{}, len(explicit)+len(inherited))
	keys := make([]string, 0, len(explicit))
	for key, value := range explicit {
		if err := validateEnvironmentKey(key); err != nil {
			return nil, err
		}
		identity := environmentKeyIdentity(key)
		if _, duplicate := seen[identity]; duplicate {
			return nil, fmt.Errorf("duplicate environment key %q: %w", key, ErrInvalidConfig)
		}
		if strings.ContainsRune(value, 0) || !utf8.ValidString(value) {
			return nil, fmt.Errorf("environment value %q is invalid: %w", key, ErrInvalidConfig)
		}
		seen[identity] = struct{}{}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(explicit)+len(inherited))
	for _, key := range keys {
		result = append(result, key+"="+explicit[key])
	}
	for _, key := range inherited {
		if err := validateEnvironmentKey(key); err != nil {
			return nil, err
		}
		identity := environmentKeyIdentity(key)
		if _, duplicate := seen[identity]; duplicate {
			return nil, fmt.Errorf("duplicate environment key %q: %w", key, ErrInvalidConfig)
		}
		value, ok := os.LookupEnv(key)
		if !ok {
			return nil, fmt.Errorf("inherited environment key %q is unavailable: %w", key, ErrInvalidConfig)
		}
		if strings.ContainsRune(value, 0) || !utf8.ValidString(value) {
			return nil, fmt.Errorf("inherited environment value %q is invalid: %w", key, ErrInvalidConfig)
		}
		seen[identity] = struct{}{}
		result = append(result, key+"="+value)
	}
	return result, nil
}

func validateEnvironmentKey(key string) error {
	if !environmentKeyPattern.MatchString(key) {
		return fmt.Errorf("invalid environment key %q: %w", key, ErrInvalidConfig)
	}
	return nil
}

func environmentKeyIdentity(key string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(key)
	}
	return key
}

func (t *shellTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "shell_exec",
		Description: "Execute one command through the configured shell.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["command"],"properties":{"command":{"type":"string","minLength":1},"timeout_seconds":{"type":"integer","minimum":1}}}`),
	}
}

func (t *shellTool) Invoke(ctx context.Context, call tool.Call) (tool.Result, error) {
	if ctx == nil {
		return tool.Result{}, fmt.Errorf("shell_exec: nil context")
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	if call.Name != "" && call.Name != "shell_exec" {
		return tool.Result{}, fmt.Errorf("call name %q: %w", call.Name, ErrInvalidArguments)
	}
	var args struct {
		Command        *string `json:"command"`
		TimeoutSeconds *int    `json:"timeout_seconds"`
	}
	if err := decodeObject(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	if args.Command == nil || *args.Command == "" || !utf8.ValidString(*args.Command) {
		return tool.Result{}, fmt.Errorf("command must be a non-empty UTF-8 string: %w", ErrInvalidArguments)
	}
	timeout := t.config.timeout
	if args.TimeoutSeconds != nil {
		if *args.TimeoutSeconds < 1 {
			return tool.Result{}, fmt.Errorf("timeout_seconds must be positive: %w", ErrInvalidArguments)
		}
		if int64(*args.TimeoutSeconds) > (int64(^uint64(0)>>1) / int64(time.Second)) {
			return tool.Result{}, fmt.Errorf("timeout_seconds is too large: %w", ErrInvalidArguments)
		}
		requested := time.Duration(*args.TimeoutSeconds) * time.Second
		if requested > timeout {
			return tool.Result{}, fmt.Errorf("per-call timeout exceeds configured timeout: %w", ErrInvalidArguments)
		}
		timeout = requested
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.Command(t.config.shell, shellCommandArgs(*args.Command)...)
	command.Dir = t.config.workingDirectory
	command.Env = make([]string, len(t.config.environment))
	copy(command.Env, t.config.environment)
	collector := newOutputCollector(t.config.maxOutputBytes)
	command.Stdout = outputWriter{collector: collector}
	command.Stderr = outputWriter{collector: collector, stderr: true}
	controller, err := newProcessController(command)
	if err != nil {
		return tool.Result{}, err
	}
	if err := command.Start(); err != nil {
		_ = controller.Close()
		return tool.Result{}, err
	}
	if err := controller.Attach(command.Process); err != nil {
		cleanupErr := terminateAndWait(command, controller)
		if cleanupErr != nil {
			return tool.Result{}, errors.Join(err, cleanupErr)
		}
		return tool.Result{}, err
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- command.Wait() }()
	var waitErr error
	select {
	case waitErr = <-waitDone:
	case <-runCtx.Done():
		cleanupErr := terminateProcess(command, controller)
		waitErr = <-waitDone
		if closeErr := controller.Close(); closeErr != nil {
			cleanupErr = errors.Join(cleanupErr, closeErr)
		}
		if cleanupErr != nil {
			return tool.Result{}, errors.Join(runCtx.Err(), cleanupErr)
		}
		return tool.Result{}, runCtx.Err()
	}
	closeErr := controller.Close()
	if closeErr != nil {
		return tool.Result{}, fmt.Errorf("close process controller: %w: %w", ErrProcessCleanup, closeErr)
	}
	if waitErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(waitErr, &exitErr) {
			return tool.Result{}, waitErr
		}
	}
	return tool.Result{Content: collector.format(exitCode(waitErr))}, nil
}

func shellCommandArgs(command string) []string {
	if runtime.GOOS == "windows" {
		return []string{"/C", command}
	}
	return []string{"-c", command}
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func terminateAndWait(command *exec.Cmd, controller processController) error {
	var cleanupErr error
	if command.Process != nil {
		if err := controller.Terminate(command.Process); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
		if err := command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			cleanupErr = errors.Join(cleanupErr, err)
		}
		if err := command.Wait(); err != nil {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) && !errors.Is(err, os.ErrProcessDone) {
				cleanupErr = errors.Join(cleanupErr, err)
			}
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

type outputCollector struct {
	mu              sync.Mutex
	stdoutLimit     int
	stderrLimit     int
	stdoutUsed      int
	stderrUsed      int
	stdout, stderr  bytes.Buffer
	stdoutTruncated bool
	stderrTruncated bool
}

func newOutputCollector(limit int) *outputCollector {
	stdoutLimit := limit / 2
	if limit%2 != 0 {
		stdoutLimit++
	}
	return &outputCollector{stdoutLimit: stdoutLimit, stderrLimit: limit / 2}
}
func (c *outputCollector) write(stderr bool, p []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	limit, used := c.stdoutLimit, c.stdoutUsed
	if stderr {
		limit, used = c.stderrLimit, c.stderrUsed
	}
	remaining := limit - used
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
			if stderr {
				c.stderrTruncated = true
			} else {
				c.stdoutTruncated = true
			}
		}
		if stderr {
			_, _ = c.stderr.Write(p)
		} else {
			_, _ = c.stdout.Write(p)
		}
		if stderr {
			c.stderrUsed += len(p)
		} else {
			c.stdoutUsed += len(p)
		}
	} else {
		if stderr {
			c.stderrTruncated = true
		} else {
			c.stdoutTruncated = true
		}
	}
}
func (c *outputCollector) format(code int) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	stdout, stderr := c.stdout.String(), c.stderr.String()
	if c.stdoutTruncated {
		stdout = trimIncompleteUTF8Suffix(stdout)
		stdout += "\n" + outputTruncationMarker
	}
	if c.stderrTruncated {
		stderr = trimIncompleteUTF8Suffix(stderr)
		stderr += "\n" + outputTruncationMarker
	}
	return fmt.Sprintf("exit_code: %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
}

func trimIncompleteUTF8Suffix(value string) string {
	if value == "" || utf8.ValidString(value) {
		return value
	}
	data := []byte(value)
	start := len(data) - 1
	for start > 0 && !utf8.RuneStart(data[start]) {
		start--
	}
	if utf8.FullRune(data[start:]) {
		return value
	}
	return string(data[:start])
}

type outputWriter struct {
	collector *outputCollector
	stderr    bool
}

func (w outputWriter) Write(p []byte) (int, error) {
	w.collector.write(w.stderr, p)
	return len(p), nil
}

func decodeObject(raw json.RawMessage, target any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return fmt.Errorf("arguments are required: %w", ErrInvalidArguments)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode arguments: %w: %w", ErrInvalidArguments, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("arguments contain trailing JSON: %w", ErrInvalidArguments)
		}
		return fmt.Errorf("decode trailing arguments: %w: %w", ErrInvalidArguments, err)
	}
	return nil
}

var _ tool.Tool = (*shellTool)(nil)
