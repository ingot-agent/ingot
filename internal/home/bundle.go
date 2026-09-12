package home

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ingot-agent/ingot/internal/builder"
	"github.com/ingot-agent/ingot/internal/bundle"
)

// BundleUpdateOptions controls inspection and refresh of the official plugin
// distribution materialized inside an ingot home.
type BundleUpdateOptions struct {
	BundlePath string
	Apply      bool
}

// BundleStatus describes the installed and available bundle snapshots.
type BundleStatus struct {
	bundle.State
	ManagedPlugins int `json:"managed_plugins"`
}

// BundleUpdateResult describes one bundle update attempt.
type BundleUpdateResult struct {
	BundleStatus
	Updated bool   `json:"updated"`
	Applied bool   `json:"applied"`
	ImageID string `json:"image_id,omitempty"`
}

// CheckBundle reports whether the distribution shipped with the current
// executable differs from the managed copy in this home.
func (home *Home) CheckBundle(ctx context.Context, bundlePath string) (BundleStatus, error) {
	release, err := home.acquire(ctx)
	if err != nil {
		return BundleStatus{}, err
	}
	defer release()
	return home.checkBundleUnlocked(bundlePath)
}

// UpdateBundle replaces the managed bundle with the distribution shipped
// with the current executable. It never rewrites plugins.toml or config.toml.
// Apply is retained only so older callers receive an explicit M2 removal error.
func (home *Home) UpdateBundle(ctx context.Context, options BundleUpdateOptions) (BundleUpdateResult, error) {
	if options.Apply {
		return BundleUpdateResult{}, fmt.Errorf("bundle update --apply was removed in M2")
	}
	release, err := home.acquire(ctx)
	if err != nil {
		return BundleUpdateResult{}, err
	}
	defer release()

	status, err := home.checkBundleWithDesiredUnlocked(options.BundlePath, nil)
	if err != nil {
		return BundleUpdateResult{}, err
	}
	result := BundleUpdateResult{BundleStatus: status}

	var backup string
	if status.UpdateAvailable {
		staged, digest, stageErr := bundle.Stage(status.SourcePath, home.Root)
		if stageErr != nil {
			return BundleUpdateResult{}, fmt.Errorf("stage official plugin bundle: %w", stageErr)
		}
		defer func() { _ = os.RemoveAll(staged) }()
		if digest != status.AvailableDigest {
			return BundleUpdateResult{}, fmt.Errorf("plugin distribution changed while staging: inspected %s, staged %s", status.AvailableDigest, digest)
		}
		backup, _, err = swapManagedBundle(home.Root, staged)
		if err != nil {
			return BundleUpdateResult{}, fmt.Errorf("activate staged plugin bundle: %w", err)
		}
		result.Updated = true
		defer func() { _ = os.RemoveAll(backup) }()
	}

	if result.Updated {
		result.InstalledDigest = status.AvailableDigest
		result.ManagedDigest = status.AvailableDigest
		result.UpdateAvailable = false
		result.Drifted = false
	}
	return result, nil
}

func (home *Home) checkBundleUnlocked(bundlePath string) (BundleStatus, error) {
	return home.checkBundleWithDesiredUnlocked(bundlePath, nil)
}

func (home *Home) checkBundleWithDesiredUnlocked(bundlePath string, desired *builder.DesiredPlugins) (BundleStatus, error) {
	sourceDir, err := bundle.Locate(bundlePath)
	if err != nil {
		return BundleStatus{}, err
	}
	state, err := bundle.Inspect(sourceDir, home.Root)
	if err != nil {
		return BundleStatus{}, err
	}
	status := BundleStatus{State: state}
	if desired == nil {
		return status, nil
	}
	for _, plugin := range desired.Plugins {
		if plugin.Path == "" {
			continue
		}
		absolute, resolveErr := desired.ResolvePath(plugin.Path)
		if resolveErr != nil {
			return BundleStatus{}, resolveErr
		}
		if pathWithin(absolute, state.ManagedPath) {
			status.ManagedPlugins++
		}
	}
	return status, nil
}

func pathWithin(path, root string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func swapManagedBundle(homeRoot, staged string) (backup string, hadPrevious bool, err error) {
	destination := filepath.Join(homeRoot, bundle.BundledDirectory)
	backup, err = os.MkdirTemp(homeRoot, ".bundled-plugins-backup-")
	if err != nil {
		return "", false, err
	}
	if err := os.Remove(backup); err != nil {
		return "", false, err
	}
	if _, statErr := os.Stat(destination); statErr == nil {
		hadPrevious = true
		if err := os.Rename(destination, backup); err != nil {
			return "", false, err
		}
	} else if !os.IsNotExist(statErr) {
		return "", false, statErr
	}
	if err := os.Rename(staged, destination); err != nil {
		if hadPrevious {
			_ = os.Rename(backup, destination)
		}
		return "", false, err
	}
	if err := syncDirectory(homeRoot); err != nil {
		restoreErr := restoreManagedBundle(homeRoot, backup, hadPrevious)
		return "", false, errors.Join(err, restoreErr)
	}
	return backup, hadPrevious, nil
}

func restoreManagedBundle(homeRoot, backup string, hadPrevious bool) error {
	destination := filepath.Join(homeRoot, bundle.BundledDirectory)
	if err := os.RemoveAll(destination); err != nil {
		return err
	}
	if hadPrevious {
		if err := os.Rename(backup, destination); err != nil {
			return err
		}
	}
	return syncDirectory(homeRoot)
}

func readOptionalFile(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	return data, err == nil, err
}

func restoreOptionalFile(path string, data []byte, existed bool) error {
	if existed {
		return atomicWrite(path, data, 0o600)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}
