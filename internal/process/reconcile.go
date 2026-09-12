package process

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Observation struct {
	State    string    `json:"state"`
	Process  *Record   `json:"process"`
	LastExit *LastExit `json:"last_exit"`
}

func Reconcile(ctx context.Context, runtimeHome string) (Observation, error) {
	defer cleanupStartResults(runtimeHome)
	record, err := ReadRecord(runtimeHome)
	if os.IsNotExist(err) {
		held, probeErr := lockHeld(WriterLockPath(runtimeHome))
		if probeErr != nil {
			return Observation{}, probeErr
		}
		last, lastErr := ReadLastExit(runtimeHome)
		if os.IsNotExist(lastErr) {
			last = nil
		} else if lastErr != nil {
			return Observation{}, lastErr
		}
		state := "stopped"
		if last != nil && (last.ExitCode != 0 || last.Reason == "unknown") {
			state = "failed"
		}
		if held {
			state = "external"
		}
		return Observation{State: state, LastExit: last}, nil
	}
	if err != nil {
		return Observation{}, err
	}
	supervisorAlive := IdentityAlive(record.SupervisorPID, record.SupervisorBirthID)
	runtimeAlive := record.RuntimePID != nil && IdentityAlive(*record.RuntimePID, record.RuntimeBirthID)
	if supervisorAlive && runtimeAlive {
		control, controlErr := ReadControl(runtimeHome)
		if controlErr == nil && control.ProcessID == record.ProcessID {
			requestCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
			metadata, pingErr := Metadata(requestCtx, *control)
			cancel()
			if pingErr == nil && sameProcessIdentity(record, metadata) {
				return Observation{State: record.Phase, Process: record}, nil
			}
		}
		return Observation{State: "unresponsive", Process: record}, nil
	}
	if !supervisorAlive && runtimeAlive {
		return Observation{State: "orphaned", Process: record}, nil
	}
	last := snapshotExit(*record, 1, "unknown", "recorded process identities are no longer live")
	if err := writeJSON(LastExitPath(runtimeHome), last); err != nil {
		return Observation{}, err
	}
	_ = os.Remove(ProcessPath(runtimeHome))
	_ = os.Remove(ControlPath(runtimeHome))
	return Observation{State: "failed", LastExit: &last}, nil
}

func cleanupStartResults(runtimeHome string) {
	entries, err := os.ReadDir(RunDirectory(runtimeHome))
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || len(name) != len("start-")+36+len(".json") || name[:len("start-")] != "start-" || name[len(name)-len(".json"):] != ".json" {
			continue
		}
		processID := name[len("start-") : len(name)-len(".json")]
		if !validProcessID(processID) {
			continue
		}
		info, err := entry.Info()
		if err == nil && time.Since(info.ModTime()) > time.Minute {
			_ = os.Remove(filepath.Join(RunDirectory(runtimeHome), name))
		}
	}
}

func Stop(ctx context.Context, runtimeHome, processID string, timeout time.Duration) error {
	observation, err := Reconcile(ctx, runtimeHome)
	if err != nil {
		return err
	}
	if observation.Process == nil {
		if processID != "" {
			return fmt.Errorf("INGOT-PROCESS-CONTROL-NOT-FOUND: process %s is not live", processID)
		}
		return nil
	}
	if processID != "" && observation.Process.ProcessID != processID {
		return fmt.Errorf("INGOT-PROCESS-CONTROL-MISMATCH: live process is %s", observation.Process.ProcessID)
	}
	control, err := ReadControl(runtimeHome)
	if err != nil {
		return err
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := Shutdown(requestCtx, *control); err != nil {
		return err
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-requestCtx.Done():
			return fmt.Errorf("INGOT-PROCESS-CONTROL-TIMEOUT: %w", requestCtx.Err())
		case <-ticker.C:
			current, err := Reconcile(ctx, runtimeHome)
			if err != nil {
				return err
			}
			if current.Process == nil {
				return nil
			}
		}
	}
}

func sameProcessIdentity(record *Record, metadata *Record) bool {
	if record == nil || metadata == nil {
		return false
	}
	return record.ProcessID == metadata.ProcessID &&
		record.SupervisorPID == metadata.SupervisorPID &&
		record.SupervisorBirthID == metadata.SupervisorBirthID &&
		record.RuntimePID != nil && metadata.RuntimePID != nil &&
		*record.RuntimePID == *metadata.RuntimePID &&
		record.RuntimeBirthID == metadata.RuntimeBirthID &&
		record.ImageID == metadata.ImageID &&
		record.ArtifactDigest == metadata.ArtifactDigest &&
		record.RuntimeGeneration == metadata.RuntimeGeneration
}
