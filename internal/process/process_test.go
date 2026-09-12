package process

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ingot-agent/ingot/internal/image"
)

func TestProcessIDsAreUUIDs(t *testing.T) {
	first, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	if first == second || len(first) != 36 {
		t.Fatalf("ids=%q %q", first, second)
	}
}

func TestReconcileProjectsExternalWriter(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(RunDirectory(home), 0o700); err != nil {
		t.Fatal(err)
	}
	release, err := acquireFileLock(context.Background(), WriterLockPath(home), false)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	observation, err := Reconcile(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	if observation.State != "external" {
		t.Fatalf("state=%s", observation.State)
	}
}

func TestReconcileCleansStaleRecord(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(RunDirectory(home), 0o700); err != nil {
		t.Fatal(err)
	}
	pid := 999999
	logPath := "logs/00000000-0000-4000-8000-000000000000.log"
	record := Record{ProcessVersion: 1, ProcessID: "00000000-0000-4000-8000-000000000000", Mode: "detached", Phase: "running", SupervisorPID: pid, SupervisorBirthID: "missing", RuntimePID: &pid, RuntimeBirthID: "missing", ImageID: "sha256:0000000000000000000000000000000000000000000000000000000000000000", ArtifactDigest: "sha256:0000000000000000000000000000000000000000000000000000000000000000", Target: image.Target{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, GOExperiment: []string{}, Tuning: []image.TargetKey{}}, RuntimeGeneration: 1, Argv: []string{}, StartedAt: time.Now().UTC(), LogPath: &logPath}
	if err := writeJSON(ProcessPath(home), record); err != nil {
		t.Fatal(err)
	}
	observation, err := Reconcile(context.Background(), home)
	if err != nil {
		t.Fatal(err)
	}
	if observation.State != "failed" || observation.LastExit == nil || observation.LastExit.Reason != "unknown" {
		t.Fatalf("observation=%#v", observation)
	}
	if _, err := os.Stat(ProcessPath(home)); !os.IsNotExist(err) {
		t.Fatalf("stale process record remains: %v", err)
	}
}

func TestProcessMetadataIsBoundedAndControlIsLoopback(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(RunDirectory(home), 0o700); err != nil {
		t.Fatal(err)
	}
	oversized := strings.Repeat("x", int(MaxProcessJSONSize)+1)
	if err := os.WriteFile(ProcessPath(home), []byte(oversized), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRecord(home); err == nil || !strings.Contains(err.Error(), "INGOT-PROCESS-RECORD-SIZE") {
		t.Fatalf("oversized record error = %v", err)
	}
	control := Control{ControlVersion: 1, ProcessID: "00000000-0000-4000-8000-000000000000", Endpoint: "http://example.com:80", Token: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}
	if err := control.Validate(); err == nil {
		t.Fatal("non-loopback control endpoint was accepted")
	}
}

func TestStartResultRoundTrip(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	id := "00000000-0000-4000-8000-000000000000"
	if err := WriteStartResult(home, id, os.ErrPermission); err != nil {
		t.Fatal(err)
	}
	result, err := ReadStartResult(home, id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Error, "permission") {
		t.Fatalf("start result = %#v", result)
	}
}
