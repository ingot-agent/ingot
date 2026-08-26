// Package home implements the user-facing ingot home, mutation,
// resolution, image switching, rollback, inspection, and runtime dispatch.
package home

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/ingot-agent/ingot/internal/builder"
	"github.com/ingot-agent/ingot/internal/layout"
)

type Home struct{ Root string }

type Status struct {
	DesiredDigest  string `json:"desired_digest,omitempty"`
	LockedDigest   string `json:"locked_digest,omitempty"`
	LockedImageID  string `json:"locked_image_id,omitempty"`
	CurrentImageID string `json:"current_image_id,omitempty"`
	DesiredLocked  bool   `json:"desired_locked"`
	LockedSources  bool   `json:"locked_sources"`
	Built          bool   `json:"built"`
	Current        bool   `json:"current"`
}

type Inspection struct {
	Status                 Status              `json:"status"`
	DirectPlugins          []PluginInspection  `json:"direct_plugins"`
	ComponentCreationOrder []string            `json:"component_creation_order"`
	ManyOrder              map[string][]string `json:"many_order"`
}

type PluginInspection struct {
	DirectPluginIndex int                       `json:"direct_plugin_index"`
	ID                string                    `json:"id"`
	Name              string                    `json:"name"`
	SourceKind        string                    `json:"source_kind"`
	Version           string                    `json:"version,omitempty"`
	ManifestDigest    string                    `json:"manifest_digest"`
	Components        []builder.LockedComponent `json:"components"`
}

func Open(root string) (*Home, error) {
	if root == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		root = filepath.Join(userHome, ".ingot")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	home := &Home{Root: filepath.Clean(absolute)}
	if err := home.ensure(); err != nil {
		return nil, err
	}
	release, err := home.acquire(context.Background())
	if err != nil {
		return nil, err
	}
	if err := home.recoverTransaction(); err != nil {
		release()
		return nil, err
	}
	release()
	return home, nil
}

func (home *Home) ensure() error {
	for _, directory := range []string{home.Root, filepath.Join(home.Root, "images"), filepath.Join(home.Root, "cache", "gomod"), filepath.Join(home.Root, "state")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func (home *Home) DesiredPath() string { return filepath.Join(home.Root, "plugins.toml") }
func (home *Home) LockPath() string    { return filepath.Join(home.Root, "plugins.lock") }
func (home *Home) ConfigPath() string  { return filepath.Join(home.Root, "config.toml") }
func (home *Home) CurrentPath() string { return filepath.Join(home.Root, "current") }
func (home *Home) imageDirectory(imageID string) string {
	return filepath.Join(home.Root, "images", layout.ImageDirectoryName(imageID, runtime.GOOS))
}

func (home *Home) Resolve(ctx context.Context, options builder.ResolveOptions) (*builder.Lock, error) {
	release, err := home.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	desired, err := builder.ParseDesired(home.DesiredPath())
	if err != nil {
		return nil, err
	}
	lock, err := home.resolveCandidate(ctx, desired, options)
	if err != nil {
		return nil, err
	}
	data, err := lock.MarshalTOML()
	if err != nil {
		return nil, err
	}
	if err := atomicWrite(home.LockPath(), data, 0o600); err != nil {
		return nil, err
	}
	return lock, nil
}

func (home *Home) resolveCandidate(ctx context.Context, desired *builder.DesiredPlugins, options builder.ResolveOptions) (*builder.Lock, error) {
	if options.GOMODCACHE == "" {
		options.GOMODCACHE = filepath.Join(home.Root, "cache", "gomod")
	}
	return builder.Resolve(ctx, desired, options)
}

func (home *Home) Build(ctx context.Context) (*builder.BuildResult, error) {
	release, err := home.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return home.buildUnlocked(ctx)
}

func (home *Home) buildUnlocked(ctx context.Context) (*builder.BuildResult, error) {
	desired, err := builder.ParseDesired(home.DesiredPath())
	if err != nil {
		return nil, err
	}
	lock, err := builder.ParseLock(home.LockPath())
	if err != nil {
		return nil, err
	}
	return builder.Build(ctx, desired, lock, builder.BuildOptions{Home: home.Root, ConfigPath: home.ConfigPath(), GOMODCACHE: filepath.Join(home.Root, "cache", "gomod")})
}

func (home *Home) Apply(ctx context.Context, options builder.ResolveOptions) (*builder.BuildResult, error) {
	release, err := home.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	desired, err := builder.ParseDesired(home.DesiredPath())
	if err != nil {
		return nil, err
	}
	lock, err := home.resolveCandidate(ctx, desired, options)
	if err != nil {
		return nil, err
	}
	lockData, err := lock.MarshalTOML()
	if err != nil {
		return nil, err
	}
	if err := atomicWrite(home.LockPath(), lockData, 0o600); err != nil {
		return nil, err
	}
	result, err := builder.Build(ctx, desired, lock, builder.BuildOptions{Home: home.Root, ConfigPath: home.ConfigPath(), GOMODCACHE: filepath.Join(home.Root, "cache", "gomod")})
	if err != nil {
		return nil, err
	}
	if err := home.switchCurrent(result.ImageID); err != nil {
		return nil, err
	}
	return result, nil
}

func (home *Home) Add(ctx context.Context, plugin builder.DesiredPlugin, options builder.ResolveOptions, apply bool) (*builder.BuildResult, error) {
	return home.mutateAllowMissing(ctx, options, apply, true, func(desired *builder.DesiredPlugins, lock *builder.Lock) error {
		for _, existing := range desired.Plugins {
			if existing.Module == plugin.Module {
				return fmt.Errorf("plugin %s is already present", plugin.Module)
			}
		}
		desired.Plugins = append(desired.Plugins, plugin)
		return nil
	})
}

func (home *Home) Remove(ctx context.Context, reference string, options builder.ResolveOptions, apply bool) (*builder.BuildResult, error) {
	return home.mutate(ctx, options, apply, func(desired *builder.DesiredPlugins, lock *builder.Lock) error {
		index, err := findPlugin(desired, lock, reference)
		if err != nil {
			return err
		}
		desired.Plugins = append(desired.Plugins[:index], desired.Plugins[index+1:]...)
		return nil
	})
}

func (home *Home) Update(ctx context.Context, reference string, replacement builder.DesiredPlugin, options builder.ResolveOptions, apply bool) (*builder.BuildResult, error) {
	return home.mutate(ctx, options, apply, func(desired *builder.DesiredPlugins, lock *builder.Lock) error {
		index, err := findPlugin(desired, lock, reference)
		if err != nil {
			return err
		}
		if replacement.Module != "" && replacement.Module != desired.Plugins[index].Module {
			return fmt.Errorf("update cannot change canonical module path; remove and add for a major identity change")
		}
		replacement.Module = desired.Plugins[index].Module
		desired.Plugins[index] = replacement
		return nil
	})
}

func (home *Home) Reorder(ctx context.Context, reference, anchor string, before bool, options builder.ResolveOptions, apply bool) (*builder.BuildResult, error) {
	return home.mutate(ctx, options, apply, func(desired *builder.DesiredPlugins, lock *builder.Lock) error {
		from, err := findPlugin(desired, lock, reference)
		if err != nil {
			return err
		}
		to, err := findPlugin(desired, lock, anchor)
		if err != nil {
			return err
		}
		if from == to {
			return fmt.Errorf("plugin and anchor are identical")
		}
		entry := desired.Plugins[from]
		desired.Plugins = append(desired.Plugins[:from], desired.Plugins[from+1:]...)
		if from < to {
			to--
		}
		if !before {
			to++
		}
		desired.Plugins = append(desired.Plugins, builder.DesiredPlugin{})
		copy(desired.Plugins[to+1:], desired.Plugins[to:])
		desired.Plugins[to] = entry
		return nil
	})
}

type mutation func(*builder.DesiredPlugins, *builder.Lock) error

func (home *Home) mutate(ctx context.Context, options builder.ResolveOptions, apply bool, change mutation) (*builder.BuildResult, error) {
	return home.mutateAllowMissing(ctx, options, apply, false, change)
}

func (home *Home) mutateAllowMissing(ctx context.Context, options builder.ResolveOptions, apply, allowMissing bool, change mutation) (*builder.BuildResult, error) {
	release, err := home.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	desired, err := builder.ParseDesired(home.DesiredPath())
	if err != nil {
		if !allowMissing || !os.IsNotExist(errors.Unwrap(err)) {
			return nil, err
		}
		desired = builder.NewDesired(home.DesiredPath(), nil)
	}
	lock, lockErr := builder.ParseLock(home.LockPath())
	if lockErr != nil && !os.IsNotExist(errors.Unwrap(lockErr)) {
		return nil, lockErr
	}
	if err := change(desired, lock); err != nil {
		return nil, err
	}
	if err := desired.Validate(); err != nil {
		return nil, err
	}
	candidateLock, err := home.resolveCandidate(ctx, desired, options)
	if err != nil {
		return nil, err
	}
	if err := home.commitPair(desired, candidateLock); err != nil {
		return nil, err
	}
	if !apply {
		return nil, nil
	}
	result, err := builder.Build(ctx, desired, candidateLock, builder.BuildOptions{Home: home.Root, ConfigPath: home.ConfigPath(), GOMODCACHE: filepath.Join(home.Root, "cache", "gomod")})
	if err != nil {
		return nil, err
	}
	if err := home.switchCurrent(result.ImageID); err != nil {
		return nil, err
	}
	return result, nil
}

func findPlugin(desired *builder.DesiredPlugins, lock *builder.Lock, reference string) (int, error) {
	for i, plugin := range desired.Plugins {
		if plugin.Module == reference {
			return i, nil
		}
	}
	if lock != nil {
		for i, plugin := range lock.Plugins {
			if plugin.Name == reference && i < len(desired.Plugins) && desired.Plugins[i].Module == plugin.ID {
				return i, nil
			}
		}
	}
	return -1, fmt.Errorf("plugin %q not found by canonical id or locked name", reference)
}

type transaction struct {
	Desired string `json:"desired"`
	Lock    string `json:"lock"`
}

func (home *Home) transactionPath() string { return filepath.Join(home.Root, ".plugins.transaction") }

func (home *Home) commitPair(desired *builder.DesiredPlugins, lock *builder.Lock) error {
	desiredData, err := marshalDesiredPreservingComments(home.DesiredPath(), desired)
	if err != nil {
		return err
	}
	lockData, err := lock.MarshalTOML()
	if err != nil {
		return err
	}
	marker, _ := json.Marshal(transaction{Desired: base64.StdEncoding.EncodeToString(desiredData), Lock: base64.StdEncoding.EncodeToString(lockData)})
	if err := atomicWrite(home.transactionPath(), marker, 0o600); err != nil {
		return err
	}
	if err := atomicWrite(home.DesiredPath(), desiredData, 0o600); err != nil {
		return err
	}
	if err := atomicWrite(home.LockPath(), lockData, 0o600); err != nil {
		return err
	}
	if err := os.Remove(home.transactionPath()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syncDirectory(home.Root)
}

func (home *Home) recoverTransaction() error {
	data, err := os.ReadFile(home.transactionPath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var marker transaction
	if err := json.Unmarshal(data, &marker); err != nil {
		return fmt.Errorf("recover plugin transaction: %w", err)
	}
	desired, err := base64.StdEncoding.DecodeString(marker.Desired)
	if err != nil {
		return err
	}
	lock, err := base64.StdEncoding.DecodeString(marker.Lock)
	if err != nil {
		return err
	}
	if err := atomicWrite(home.DesiredPath(), desired, 0o600); err != nil {
		return err
	}
	if err := atomicWrite(home.LockPath(), lock, 0o600); err != nil {
		return err
	}
	return os.Remove(home.transactionPath())
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".tmp-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	remove := true
	defer func() {
		if remove {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := atomicReplace(temporaryPath, path); err != nil {
		return err
	}
	remove = false
	return syncDirectory(directory)
}

func (home *Home) switchCurrent(imageID string) error {
	if !validImageID(imageID) {
		return fmt.Errorf("invalid image id %q", imageID)
	}
	if _, err := builder.VerifyImage(home.imageDirectory(imageID), imageID, nil); err != nil {
		return err
	}
	previous, _ := home.Current()
	if previous != "" && previous != imageID {
		if err := atomicWrite(filepath.Join(home.Root, "current.previous"), []byte(previous+"\n"), 0o600); err != nil {
			return err
		}
	}
	return atomicWrite(home.CurrentPath(), []byte(imageID+"\n"), 0o600)
}

func (home *Home) Current() (string, error) {
	data, err := os.ReadFile(home.CurrentPath())
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(data))
	if !validImageID(value) {
		return "", fmt.Errorf("invalid current image id %q", value)
	}
	return value, nil
}

func validImageID(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || strings.ContainsAny(value, `/\\`) {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil && len(decoded) == 32
}

func (home *Home) Rollback(ctx context.Context, imageID string) error {
	release, err := home.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	if imageID == "" {
		data, readErr := os.ReadFile(filepath.Join(home.Root, "current.previous"))
		if readErr != nil {
			return readErr
		}
		imageID = strings.TrimSpace(string(data))
	}
	return home.switchCurrent(imageID)
}

func (home *Home) Status() (Status, error) {
	var status Status
	desired, desiredErr := builder.ParseDesired(home.DesiredPath())
	if desiredErr == nil {
		status.DesiredDigest, _ = desired.Digest()
	} else if !os.IsNotExist(errors.Unwrap(desiredErr)) {
		return status, desiredErr
	}
	lock, lockErr := builder.ParseLock(home.LockPath())
	if lockErr == nil {
		status.LockedDigest = lock.PluginsDigest
		status.LockedImageID, _ = lock.ImageID()
		status.LockedSources = true
		for _, replacement := range lock.Replacements {
			digest, digestErr := builder.DevSourceDigest(replacement.DevPath)
			if digestErr != nil || digest != replacement.ContentSHA256 {
				status.LockedSources = false
				break
			}
		}
	} else if !os.IsNotExist(errors.Unwrap(lockErr)) {
		return status, lockErr
	}
	current, currentErr := home.Current()
	if currentErr == nil {
		status.CurrentImageID = current
	} else if !os.IsNotExist(currentErr) {
		return status, currentErr
	}
	status.DesiredLocked = status.DesiredDigest != "" && status.DesiredDigest == status.LockedDigest
	if status.LockedImageID != "" {
		expected, _ := lock.CanonicalBuildManifest()
		_, verifyErr := builder.VerifyImage(home.imageDirectory(status.LockedImageID), status.LockedImageID, expected)
		status.Built = verifyErr == nil
	}
	status.Current = status.DesiredLocked && status.LockedSources && status.Built && status.CurrentImageID == status.LockedImageID
	return status, nil
}

func (home *Home) Inspect(reference string) (Inspection, error) {
	status, err := home.Status()
	if err != nil {
		return Inspection{}, err
	}
	lock, err := builder.ParseLock(home.LockPath())
	if err != nil {
		return Inspection{}, err
	}
	inspection := Inspection{Status: status, ManyOrder: map[string][]string{}}
	for i, plugin := range lock.Plugins {
		if reference != "" && reference != plugin.ID && reference != plugin.Name {
			continue
		}
		inspection.DirectPlugins = append(inspection.DirectPlugins, PluginInspection{DirectPluginIndex: i, ID: plugin.ID, Name: plugin.Name, SourceKind: plugin.SourceKind, Version: plugin.Version, ManifestDigest: plugin.ManifestDigest, Components: plugin.Components})
	}
	if reference != "" && len(inspection.DirectPlugins) == 0 {
		return Inspection{}, fmt.Errorf("plugin %q not found", reference)
	}
	manifestPath := filepath.Join(home.imageDirectory(status.LockedImageID), "manifest.json")
	if data, readErr := os.ReadFile(manifestPath); readErr == nil {
		var manifest builder.ImageManifest
		if json.Unmarshal(data, &manifest) == nil {
			inspection.ComponentCreationOrder, inspection.ManyOrder = manifest.ComponentCreationOrder, manifest.ManyOrder
		}
	}
	return inspection, nil
}

func (home *Home) RunCurrent(ctx context.Context, arguments []string) error {
	imageID, err := home.Current()
	if err != nil {
		return err
	}
	binary := filepath.Join(home.imageDirectory(imageID), layout.RuntimeExecutableName(runtime.GOOS))
	if _, err := os.Stat(binary); err != nil {
		return err
	}
	command := exec.CommandContext(ctx, binary, arguments...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	command.Env = replaceEnv(os.Environ(), "INGOT_HOME", home.Root)
	if err := command.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return &ExitError{Code: exitErr.ExitCode()}
		}
		return err
	}
	return nil
}

type ExitError struct{ Code int }

func (e *ExitError) Error() string { return fmt.Sprintf("runtime exited with code %d", e.Code) }

func replaceEnv(environment []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(environment)+1)
	for _, item := range environment {
		if !strings.HasPrefix(item, prefix) {
			result = append(result, item)
		}
	}
	return append(result, prefix+value)
}

func (home *Home) GC(ctx context.Context, keepRecent int) ([]string, error) {
	if keepRecent < 0 {
		return nil, fmt.Errorf("keepRecent must be non-negative")
	}
	release, err := home.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	keep := map[string]bool{}
	if current, err := home.Current(); err == nil {
		keep[current] = true
	}
	if data, err := os.ReadFile(filepath.Join(home.Root, "current.previous")); err == nil {
		keep[strings.TrimSpace(string(data))] = true
	}
	type candidate struct {
		id, path string
		modified time.Time
	}
	var candidates []candidate
	entries, err := os.ReadDir(filepath.Join(home.Root, "images"))
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), ".staging-") {
			stagingPath := filepath.Join(home.Root, "images", entry.Name())
			if err := os.RemoveAll(stagingPath); err != nil {
				return nil, err
			}
			continue
		}
		imageID := layout.ImageIDFromDirectoryName(entry.Name(), runtime.GOOS)
		if !entry.IsDir() || !validImageID(imageID) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate{id: imageID, path: filepath.Join(home.Root, "images", entry.Name()), modified: info.ModTime()})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].modified.After(candidates[j].modified) })
	for i := 0; i < keepRecent && i < len(candidates); i++ {
		keep[candidates[i].id] = true
	}
	removed := []string{}
	for _, candidate := range candidates {
		if keep[candidate.id] {
			continue
		}
		if err := os.RemoveAll(candidate.path); err != nil {
			return removed, err
		}
		removed = append(removed, candidate.id)
	}
	return removed, nil
}
