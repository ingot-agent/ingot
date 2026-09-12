package home

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ingot-agent/ingot/internal/image"
	"github.com/ingot-agent/ingot/internal/managedruntime"
	processmgr "github.com/ingot-agent/ingot/internal/process"
)

type RuntimeView struct {
	managedruntime.Runtime
	Process         *processmgr.Record   `json:"process"`
	LastExit        *processmgr.LastExit `json:"last_exit"`
	State           string               `json:"state"`
	RestartRequired bool                 `json:"restart_required"`
}

func (home *Home) registry() managedruntime.Registry {
	return managedruntime.New(filepath.Join(home.Root, "runtimes"))
}

func (home *Home) RuntimeCreate(ctx context.Context, name, imageRef string, argv []string) (RuntimeView, error) {
	release, err := home.acquire(ctx)
	if err != nil {
		return RuntimeView{}, err
	}
	defer release()
	binding, _, err := home.resolveImageUnlocked(imageRef, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return RuntimeView{}, err
	}
	if !binding.Target.SamePlatform(runtime.GOOS, runtime.GOARCH) {
		return RuntimeView{}, fmt.Errorf("INGOT-RUNTIME-IMAGE-TARGET: want %s/%s, got %s", runtime.GOOS, runtime.GOARCH, binding.Target.Platform())
	}
	entry, err := home.registry().Create(name, binding, argv, time.Now())
	if err != nil {
		return RuntimeView{}, err
	}
	return RuntimeView{Runtime: entry, State: "stopped"}, nil
}

func (home *Home) RuntimeInspect(ctx context.Context, name string) (RuntimeView, error) {
	release, err := home.acquire(ctx)
	if err != nil {
		return RuntimeView{}, err
	}
	defer release()
	return home.runtimeInspectUnlocked(ctx, name)
}

func (home *Home) runtimeInspectUnlocked(ctx context.Context, name string) (RuntimeView, error) {
	entry, err := home.registry().Load(name)
	if err != nil {
		return RuntimeView{}, err
	}
	observation, err := processmgr.Reconcile(ctx, home.registry().RuntimeHome(name))
	if err != nil {
		return RuntimeView{}, err
	}
	restart := observation.Process != nil && (observation.Process.ImageID != entry.DesiredImage.ImageID || observation.Process.RuntimeGeneration != entry.Generation)
	return RuntimeView{Runtime: entry, Process: observation.Process, LastExit: observation.LastExit, State: observation.State, RestartRequired: restart}, nil
}

func (home *Home) RuntimeList(ctx context.Context) ([]RuntimeView, error) {
	release, err := home.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	entries, err := home.registry().List()
	if err != nil {
		return nil, err
	}
	result := make([]RuntimeView, 0, len(entries))
	for _, entry := range entries {
		view, err := home.runtimeInspectUnlocked(ctx, entry.Name)
		if err != nil {
			return nil, err
		}
		result = append(result, view)
	}
	return result, nil
}

func (home *Home) RuntimeSwitch(ctx context.Context, name, imageRef string) (RuntimeView, error) {
	release, err := home.acquire(ctx)
	if err != nil {
		return RuntimeView{}, err
	}
	defer release()
	current, err := home.runtimeInspectUnlocked(ctx, name)
	if err != nil {
		return RuntimeView{}, err
	}
	if current.State == "external" {
		return RuntimeView{}, fmt.Errorf("INGOT-RUNTIME-REGISTRY-EXTERNAL: runtime %s has an external writer", name)
	}
	binding, _, err := home.resolveImageUnlocked(imageRef, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return RuntimeView{}, err
	}
	if !binding.Target.SamePlatform(runtime.GOOS, runtime.GOARCH) {
		return RuntimeView{}, fmt.Errorf("INGOT-RUNTIME-IMAGE-TARGET: target mismatch")
	}
	_, _, err = home.registry().Switch(name, binding, time.Now())
	if err != nil {
		return RuntimeView{}, err
	}
	return home.runtimeInspectUnlocked(ctx, name)
}

func (home *Home) RuntimeRollback(ctx context.Context, name string) (RuntimeView, error) {
	release, err := home.acquire(ctx)
	if err != nil {
		return RuntimeView{}, err
	}
	defer release()
	current, err := home.runtimeInspectUnlocked(ctx, name)
	if err != nil {
		return RuntimeView{}, err
	}
	if current.State == "external" {
		return RuntimeView{}, fmt.Errorf("INGOT-RUNTIME-REGISTRY-EXTERNAL: runtime %s has an external writer", name)
	}
	if current.RollbackImage == nil {
		return RuntimeView{}, fmt.Errorf("INGOT-RUNTIME-REGISTRY-ROLLBACK: runtime %s has no rollback image", name)
	}
	if !current.RollbackImage.Target.SamePlatform(runtime.GOOS, runtime.GOARCH) {
		return RuntimeView{}, fmt.Errorf("INGOT-RUNTIME-IMAGE-TARGET: rollback target mismatch")
	}
	_, err = home.registry().Rollback(name, time.Now())
	if err != nil {
		return RuntimeView{}, err
	}
	return home.runtimeInspectUnlocked(ctx, name)
}

func (home *Home) RuntimeCommand(ctx context.Context, name string, argv []string) (RuntimeView, error) {
	release, err := home.acquire(ctx)
	if err != nil {
		return RuntimeView{}, err
	}
	defer release()
	current, err := home.runtimeInspectUnlocked(ctx, name)
	if err != nil {
		return RuntimeView{}, err
	}
	if current.State == "external" {
		return RuntimeView{}, fmt.Errorf("INGOT-RUNTIME-REGISTRY-EXTERNAL: runtime %s has an external writer", name)
	}
	if _, _, err := home.registry().SetCommand(name, argv, time.Now()); err != nil {
		return RuntimeView{}, err
	}
	return home.runtimeInspectUnlocked(ctx, name)
}

func (home *Home) RuntimeDelete(ctx context.Context, name string, purge bool) error {
	release, err := home.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	view, err := home.runtimeInspectUnlocked(ctx, name)
	if err != nil {
		return err
	}
	if view.Process != nil || view.State == "external" {
		return fmt.Errorf("INGOT-RUNTIME-REGISTRY-ACTIVE: runtime %s is %s", name, view.State)
	}
	supervisorHeld, writerHeld, err := processmgr.LocksHeld(home.registry().RuntimeHome(name))
	if err != nil {
		return err
	}
	if supervisorHeld || writerHeld {
		return fmt.Errorf("INGOT-RUNTIME-REGISTRY-ACTIVE: runtime %s has an active lock", name)
	}
	registry := home.registry()
	if err := registry.ValidateDelete(name, purge); err != nil {
		return err
	}
	journalPath, err := home.writeHomeTransaction(homeTransaction{Kind: "runtime_delete", RuntimeDelete: &runtimeDeleteTransaction{Name: name, Purge: purge}})
	if err != nil {
		return err
	}
	if err := os.RemoveAll(registry.RuntimeHome(name)); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Join(home.Root, "runtimes")); err != nil {
		return err
	}
	return home.removeHomeTransaction(journalPath)
}

func (home *Home) RuntimeRun(ctx context.Context, name string, temporaryArgv []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	release, err := home.acquire(ctx)
	if err != nil {
		return 1, err
	}
	view, err := home.runtimeInspectUnlocked(ctx, name)
	if err != nil {
		release()
		return 1, err
	}
	if view.Process != nil || view.State == "external" {
		release()
		return 1, fmt.Errorf("INGOT-PROCESS-LOCK-WRITER: runtime %s is already active", name)
	}
	argv := view.DefaultArgv
	if temporaryArgv != nil {
		argv = temporaryArgv
	}
	verified, err := home.resolveImageBinding(view.DesiredImage)
	if err != nil {
		release()
		return 1, err
	}
	started := make(chan error, 1)
	done := make(chan struct {
		code int
		err  error
	}, 1)
	go func() {
		code, runErr := processmgr.Run(ctx, processmgr.Config{RuntimeHome: home.registry().RuntimeHome(name), Binary: verified.BinaryPath, Binding: view.DesiredImage, RuntimeGeneration: view.Generation, Argv: argv, Mode: "foreground", Stdin: stdin, Stdout: stdout, Stderr: stderr, Environment: os.Environ(), Started: func(err error) { started <- err }})
		done <- struct {
			code int
			err  error
		}{code, runErr}
	}()
	startErr := <-started
	release()
	if startErr != nil {
		return 1, startErr
	}
	result := <-done
	return result.code, result.err
}

func (home *Home) RuntimeStart(ctx context.Context, name string, temporaryArgv []string, timeout time.Duration) (*processmgr.Record, error) {
	release, err := home.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	view, err := home.runtimeInspectUnlocked(ctx, name)
	if err != nil {
		return nil, err
	}
	if view.Process != nil || view.State == "external" {
		return nil, fmt.Errorf("INGOT-PROCESS-LOCK-WRITER: runtime %s is already active", name)
	}
	argv := view.DefaultArgv
	if temporaryArgv != nil {
		argv = temporaryArgv
	}
	processID, err := processmgr.NewID()
	if err != nil {
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	encodedArgv, _ := json.Marshal(argv)
	command := exec.Command(executable, "--home", home.Root, "supervise", "--runtime", name, "--process", processID, "--argv", base64.RawURLEncoding.EncodeToString(encodedArgv))
	configureDetached(command)
	command.Stdin = nil
	command.Stdout, command.Stderr = nil, nil
	if err := command.Start(); err != nil {
		return nil, err
	}
	_ = command.Process.Release()
	startResultPath := processmgr.StartResultPath(home.registry().RuntimeHome(name), processID)
	defer os.Remove(startResultPath)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		record, readErr := processmgr.ReadRecord(home.registry().RuntimeHome(name))
		if readErr == nil && record.ProcessID == processID && record.Phase == "running" {
			return record, nil
		}
		last, lastErr := processmgr.ReadLastExit(home.registry().RuntimeHome(name))
		if lastErr == nil && last.ProcessID == processID {
			return nil, fmt.Errorf("INGOT-PROCESS-START-FAILED: %s", last.Error)
		}
		startResult, startErr := processmgr.ReadStartResult(home.registry().RuntimeHome(name), processID)
		if startErr == nil && startResult.Error != "" {
			return nil, fmt.Errorf("INGOT-PROCESS-START-FAILED: %s", startResult.Error)
		}
		observation, reconcileErr := processmgr.Reconcile(ctx, home.registry().RuntimeHome(name))
		if reconcileErr == nil && observation.Process != nil && observation.Process.ProcessID != processID {
			return nil, fmt.Errorf("INGOT-PROCESS-LOCK-SUPERVISOR: runtime %s is already managed by process %s", name, observation.Process.ProcessID)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	return nil, fmt.Errorf("INGOT-PROCESS-START-TIMEOUT: supervisor did not acknowledge within %s", timeout)
}

func (home *Home) SuperviseDetached(ctx context.Context, name, processID, encodedArgv string) (code int, superviseErr error) {
	if err := image.ValidateRuntimeName(name); err != nil {
		return 1, err
	}
	if err := processmgr.ValidateID(processID); err != nil {
		return 1, err
	}
	runtimeHome := home.registry().RuntimeHome(name)
	reported := false
	report := func(startErr error) {
		if reported {
			return
		}
		reported = true
		_ = processmgr.WriteStartResult(runtimeHome, processID, startErr)
	}
	defer func() {
		if !reported {
			report(superviseErr)
		}
	}()
	entry, err := home.registry().Load(name)
	if err != nil {
		return 1, err
	}
	data, err := base64.RawURLEncoding.DecodeString(encodedArgv)
	if err != nil {
		return 1, err
	}
	if int64(len(data)) > processmgr.MaxProcessJSONSize {
		return 1, fmt.Errorf("INGOT-PROCESS-START-ARGV: encoded argv exceeds limit")
	}
	var argv []string
	if err := image.StrictDecode(data, &argv); err != nil {
		return 1, err
	}
	verified, err := home.resolveImageBinding(entry.DesiredImage)
	if err != nil {
		return 1, err
	}
	relative := filepath.ToSlash(filepath.Join("logs", processID+".log"))
	logPath := filepath.Join(home.registry().RuntimeHome(name), filepath.FromSlash(relative))
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 1, err
	}
	defer logFile.Close()
	return processmgr.Run(ctx, processmgr.Config{RuntimeHome: runtimeHome, Binary: verified.BinaryPath, Binding: entry.DesiredImage, RuntimeGeneration: entry.Generation, Argv: argv, Mode: "detached", Stdout: logFile, Stderr: logFile, LogPath: &relative, Environment: os.Environ(), ProcessID: processID, Started: report})
}

type verifiedBinding struct{ BinaryPath string }

func (home *Home) resolveImageBinding(binding image.Binding) (verifiedBinding, error) {
	verified, err := image.Verify(home.imageDirectory(binding.ImageID), binding.ImageID, nil)
	if err != nil {
		return verifiedBinding{}, err
	}
	if verified.Manifest.ArtifactDigest != binding.ArtifactDigest || !verified.Manifest.Target.Equal(binding.Target) {
		return verifiedBinding{}, fmt.Errorf("INGOT-RUNTIME-IMAGE-MISMATCH: registry binding differs from image")
	}
	return verifiedBinding{BinaryPath: verified.BinaryPath}, nil
}

func (home *Home) RuntimeStop(ctx context.Context, name, processID string, timeout time.Duration) error {
	release, err := home.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	return processmgr.Stop(ctx, home.registry().RuntimeHome(name), processID, timeout)
}
func (home *Home) RuntimeRestart(ctx context.Context, name string, timeout time.Duration) (*processmgr.Record, error) {
	if err := home.RuntimeStop(ctx, name, "", timeout); err != nil {
		return nil, err
	}
	return home.RuntimeStart(ctx, name, nil, timeout)
}

func (home *Home) Processes(ctx context.Context) ([]RuntimeView, error) {
	views, err := home.RuntimeList(ctx)
	if err != nil {
		return nil, err
	}
	result := views[:0]
	for _, view := range views {
		if view.Process != nil {
			result = append(result, view)
		}
	}
	return result, nil
}
func (home *Home) StopProcess(ctx context.Context, processID string, timeout time.Duration) error {
	views, err := home.RuntimeList(ctx)
	if err != nil {
		return err
	}
	for _, view := range views {
		if view.Process != nil && view.Process.ProcessID == processID {
			return home.RuntimeStop(ctx, view.Name, processID, timeout)
		}
	}
	return fmt.Errorf("INGOT-PROCESS-CONTROL-NOT-FOUND: %s", processID)
}

func (home *Home) RuntimeLogs(ctx context.Context, name, processID string, follow bool, output io.Writer) error {
	view, err := home.RuntimeInspect(ctx, name)
	if err != nil {
		return err
	}
	var relative *string
	if processID != "" {
		candidate := filepath.ToSlash(filepath.Join("logs", processID+".log"))
		relative = &candidate
	} else if view.Process != nil && view.Process.Mode == "detached" {
		relative = view.Process.LogPath
	} else if view.LastExit != nil {
		relative = view.LastExit.LogPath
	}
	if relative == nil {
		return fmt.Errorf("INGOT-PROCESS-LOG-NOT-FOUND: runtime %s has no detached log", name)
	}
	clean := filepath.Clean(filepath.FromSlash(*relative))
	if filepath.Dir(clean) != "logs" || !strings.HasSuffix(filepath.Base(clean), ".log") {
		return fmt.Errorf("INGOT-PROCESS-LOG-PATH: invalid log path")
	}
	path := filepath.Join(home.registry().RuntimeHome(name), clean)
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	for {
		_, copyErr := io.Copy(output, reader)
		if copyErr != nil {
			return copyErr
		}
		if !follow {
			return nil
		}
		current, inspectErr := home.RuntimeInspect(ctx, name)
		if inspectErr != nil {
			return inspectErr
		}
		if current.Process == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
