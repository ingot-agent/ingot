package process

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ingot-agent/ingot/internal/image"
)

type Config struct {
	RuntimeHome       string
	Binary            string
	Binding           image.Binding
	RuntimeGeneration uint64
	Argv              []string
	Mode              string
	ProcessID         string
	Stdin             io.Reader
	Stdout            io.Writer
	Stderr            io.Writer
	LogPath           *string
	Environment       []string
	Started           func(error)
}

func Run(ctx context.Context, config Config) (code int, runErr error) {
	notified := false
	defer func() {
		if !notified && config.Started != nil {
			config.Started(runErr)
		}
	}()
	if config.Mode != "foreground" && config.Mode != "detached" {
		return 1, fmt.Errorf("INGOT-PROCESS-START-MODE: invalid mode")
	}
	if config.ProcessID == "" {
		var err error
		config.ProcessID, err = NewID()
		if err != nil {
			return 1, err
		}
	}
	if err := ValidateID(config.ProcessID); err != nil {
		return 1, err
	}
	if err := config.Binding.Validate(); err != nil {
		return 1, err
	}
	if config.RuntimeGeneration == 0 {
		return 1, fmt.Errorf("INGOT-PROCESS-START-GENERATION: runtime generation must be positive")
	}
	if config.Argv == nil {
		config.Argv = []string{}
	}
	if err := validateArgv(config.Argv); err != nil {
		return 1, err
	}
	if err := os.MkdirAll(RunDirectory(config.RuntimeHome), 0o700); err != nil {
		return 1, err
	}
	release, err := acquireFileLock(ctx, SupervisorLockPath(config.RuntimeHome), false)
	if err != nil {
		return 1, fmt.Errorf("INGOT-PROCESS-LOCK-SUPERVISOR: %w", err)
	}
	defer release()
	if held, err := lockHeld(WriterLockPath(config.RuntimeHome)); err != nil {
		return 1, err
	} else if held {
		return 1, fmt.Errorf("INGOT-PROCESS-LOCK-WRITER: runtime is already running")
	}
	verified, err := image.Verify(filepath.Dir(config.Binary), config.Binding.ImageID, nil)
	if err != nil {
		return 1, err
	}
	if verified.BinaryPath != config.Binary || verified.Manifest.ArtifactDigest != config.Binding.ArtifactDigest || !verified.Manifest.Target.Equal(config.Binding.Target) {
		return 1, fmt.Errorf("INGOT-RUNTIME-IMAGE-MISMATCH: launch binding differs from verified image")
	}
	supervisorBirth, err := BirthIdentity(os.Getpid())
	if err != nil {
		return 1, err
	}
	now := time.Now().UTC()
	record := Record{ProcessVersion: 1, ProcessID: config.ProcessID, Mode: config.Mode, Phase: "starting", SupervisorPID: os.Getpid(), SupervisorBirthID: supervisorBirth, ImageID: config.Binding.ImageID, ArtifactDigest: config.Binding.ArtifactDigest, Target: config.Binding.Target, RuntimeGeneration: config.RuntimeGeneration, Argv: append([]string{}, config.Argv...), StartedAt: now, LogPath: config.LogPath}
	if err := writeJSON(ProcessPath(config.RuntimeHome), record); err != nil {
		return 1, err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return startupFailure(config.RuntimeHome, record, fmt.Errorf("control listen: %w", err))
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		listener.Close()
		return startupFailure(config.RuntimeHome, record, err)
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	control := Control{ControlVersion: 1, ProcessID: record.ProcessID, Endpoint: "http://" + listener.Addr().String(), Token: token}
	if err := writeJSON(ControlPath(config.RuntimeHome), control); err != nil {
		listener.Close()
		return startupFailure(config.RuntimeHome, record, err)
	}
	command := exec.Command(config.Binary, config.Argv...)
	configureRuntimeProcess(command)
	command.Stdin, command.Stdout, command.Stderr = config.Stdin, config.Stdout, config.Stderr
	command.Env = replaceEnvironment(config.Environment, "INGOT_RUNTIME_HOME", config.RuntimeHome)
	if err := command.Start(); err != nil {
		listener.Close()
		return startupFailure(config.RuntimeHome, record, err)
	}
	runtimePID := command.Process.Pid
	runtimeBirth, err := BirthIdentity(runtimePID)
	if err != nil {
		_ = terminate(command.Process)
		_, _ = command.Process.Wait()
		listener.Close()
		return startupFailure(config.RuntimeHome, record, err)
	}
	record.RuntimePID, record.RuntimeBirthID, record.Phase = &runtimePID, runtimeBirth, "running"
	if err := writeJSON(ProcessPath(config.RuntimeHome), record); err != nil {
		_ = terminate(command.Process)
		_, _ = command.Process.Wait()
		listener.Close()
		return startupFailure(config.RuntimeHome, record, err)
	}
	if config.Started != nil {
		config.Started(nil)
		notified = true
	}
	var shutdownOnce sync.Once
	server := &http.Server{Handler: controlHandler(token, record, func() {
		shutdownOnce.Do(func() {
			record.Phase = "stopping"
			_ = writeJSON(ProcessPath(config.RuntimeHome), record)
			_ = terminate(command.Process)
		})
	}), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, MaxHeaderBytes: 16 << 10}
	serverDone := make(chan struct{})
	go func() { _ = server.Serve(listener); close(serverDone) }()
	childDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdownOnce.Do(func() {
				record.Phase = "stopping"
				_ = writeJSON(ProcessPath(config.RuntimeHome), record)
				_ = terminate(command.Process)
			})
		case <-childDone:
		}
	}()
	waitErr := command.Wait()
	close(childDone)
	_ = server.Shutdown(context.Background())
	<-serverDone
	exitCode, reason, summary := exitDetails(waitErr)
	last := snapshotExit(record, exitCode, reason, summary)
	if err := writeJSON(LastExitPath(config.RuntimeHome), last); err != nil {
		return exitCode, err
	}
	_ = os.Remove(ProcessPath(config.RuntimeHome))
	_ = os.Remove(ControlPath(config.RuntimeHome))
	return exitCode, nil
}

func controlHandler(token string, record Record, shutdown func()) http.Handler {
	mux := http.NewServeMux()
	authorize := func(next http.HandlerFunc) http.HandlerFunc {
		return func(writer http.ResponseWriter, request *http.Request) {
			if request.Header.Get("Authorization") != "Bearer "+token {
				http.Error(writer, "unauthorized", http.StatusUnauthorized)
				return
			}
			if request.Body != nil {
				limited := http.MaxBytesReader(writer, request.Body, 1)
				data, err := io.ReadAll(limited)
				if err != nil || len(data) != 0 {
					http.Error(writer, "request body must be empty", http.StatusBadRequest)
					return
				}
			}
			next(writer, request)
		}
	}
	mux.HandleFunc("GET /v1/metadata", authorize(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(record)
	}))
	mux.HandleFunc("POST /v1/shutdown", authorize(func(writer http.ResponseWriter, _ *http.Request) {
		shutdown()
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte("{\"accepted\":true}\n"))
	}))
	return mux
}

func Metadata(ctx context.Context, control Control) (*Record, error) {
	var metadata Record
	if err := requestJSON(ctx, control, http.MethodGet, "/v1/metadata", &metadata); err != nil {
		return nil, err
	}
	if err := metadata.Validate(); err != nil {
		return nil, err
	}
	return &metadata, nil
}

func Shutdown(ctx context.Context, control Control) error {
	var responseValue struct {
		Accepted bool `json:"accepted"`
	}
	if err := requestJSON(ctx, control, http.MethodPost, "/v1/shutdown", &responseValue); err != nil {
		return err
	}
	if !responseValue.Accepted {
		return fmt.Errorf("INGOT-PROCESS-CONTROL-RESPONSE: shutdown was not accepted")
	}
	return nil
}

func requestJSON(ctx context.Context, control Control, method, path string, output any) error {
	if err := control.Validate(); err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, method, control.Endpoint+path, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+control.Token)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return fmt.Errorf("INGOT-PROCESS-CONTROL-REDIRECT: redirects are not allowed")
	}}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(response.Body, MaxProcessJSONSize+1))
	if readErr != nil {
		return readErr
	}
	if int64(len(data)) > MaxProcessJSONSize {
		return fmt.Errorf("INGOT-PROCESS-CONTROL-RESPONSE: response exceeds limit")
	}
	if response.StatusCode/100 != 2 {
		return fmt.Errorf("INGOT-PROCESS-CONTROL-STATUS: %s", response.Status)
	}
	if err := image.StrictDecode(data, output); err != nil {
		return fmt.Errorf("INGOT-PROCESS-CONTROL-RESPONSE: %w", err)
	}
	return nil
}

func startupFailure(runtimeHome string, record Record, err error) (int, error) {
	last := snapshotExit(record, 1, "startup_error", sanitizeError(err))
	_ = writeJSON(LastExitPath(runtimeHome), last)
	_ = os.Remove(ProcessPath(runtimeHome))
	_ = os.Remove(ControlPath(runtimeHome))
	return 1, err
}
func snapshotExit(record Record, code int, reason, summary string) LastExit {
	return LastExit{ProcessVersion: 1, ProcessID: record.ProcessID, Mode: record.Mode, SupervisorPID: record.SupervisorPID, SupervisorBirthID: record.SupervisorBirthID, RuntimePID: record.RuntimePID, RuntimeBirthID: record.RuntimeBirthID, ImageID: record.ImageID, ArtifactDigest: record.ArtifactDigest, Target: record.Target, RuntimeGeneration: record.RuntimeGeneration, Argv: record.Argv, StartedAt: record.StartedAt, ExitedAt: time.Now().UTC(), ExitCode: code, Reason: reason, Error: summary, LogPath: record.LogPath}
}
func exitDetails(err error) (int, string, string) {
	if err == nil {
		return 0, "completed", ""
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code := exit.ExitCode()
		reason := "runtime_error"
		if code < 0 {
			reason = "signal"
		}
		return code, reason, sanitizeError(err)
	}
	return 1, "supervisor_error", sanitizeError(err)
}
func sanitizeError(err error) string {
	if err == nil {
		return ""
	}
	value := strings.ReplaceAll(err.Error(), "\n", " ")
	if len(value) > 512 {
		value = value[:512]
	}
	return value
}
func replaceEnvironment(environment []string, key, value string) []string {
	if environment == nil {
		environment = os.Environ()
	}
	prefix := key + "="
	result := make([]string, 0, len(environment)+1)
	for _, item := range environment {
		if !strings.HasPrefix(item, prefix) {
			result = append(result, item)
		}
	}
	return append(result, prefix+value)
}
