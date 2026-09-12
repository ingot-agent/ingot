package managedruntime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ingot-agent/ingot/internal/image"
)

const RuntimeVersion = 1

type Runtime struct {
	RuntimeVersion int            `json:"runtime_version"`
	Name           string         `json:"name"`
	DesiredImage   image.Binding  `json:"desired_image"`
	RollbackImage  *image.Binding `json:"rollback_image"`
	DefaultArgv    []string       `json:"default_argv"`
	Generation     uint64         `json:"generation"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

func (entry Runtime) Validate(directoryName string) error {
	if entry.RuntimeVersion != RuntimeVersion {
		return fmt.Errorf("INGOT-RUNTIME-REGISTRY-VERSION: want 1, got %d", entry.RuntimeVersion)
	}
	if err := image.ValidateRuntimeName(entry.Name); err != nil {
		return err
	}
	if entry.Name != directoryName {
		return fmt.Errorf("INGOT-RUNTIME-REGISTRY-NAME: metadata name %q differs from directory %q", entry.Name, directoryName)
	}
	if err := entry.DesiredImage.Validate(); err != nil {
		return err
	}
	if entry.RollbackImage != nil {
		if err := entry.RollbackImage.Validate(); err != nil {
			return err
		}
	}
	if entry.DefaultArgv == nil || entry.Generation == 0 || entry.CreatedAt.IsZero() || entry.UpdatedAt.IsZero() {
		return fmt.Errorf("INGOT-RUNTIME-REGISTRY-SCHEMA: missing required value")
	}
	for _, argument := range entry.DefaultArgv {
		if strings.ContainsRune(argument, 0) {
			return fmt.Errorf("INGOT-RUNTIME-REGISTRY-ARGV: argv contains NUL")
		}
	}
	return nil
}

type Registry struct{ Root string }

func New(root string) Registry                           { return Registry{Root: root} }
func (registry Registry) RuntimeHome(name string) string { return filepath.Join(registry.Root, name) }
func (registry Registry) Path(name string) string {
	return filepath.Join(registry.RuntimeHome(name), "runtime.json")
}

func (registry Registry) Load(name string) (Runtime, error) {
	if err := image.ValidateRuntimeName(name); err != nil {
		return Runtime{}, err
	}
	data, err := os.ReadFile(registry.Path(name))
	if err != nil {
		return Runtime{}, err
	}
	var entry Runtime
	if err := image.StrictDecode(data, &entry); err != nil {
		return Runtime{}, fmt.Errorf("INGOT-RUNTIME-REGISTRY-SCHEMA: %w", err)
	}
	if err := entry.Validate(name); err != nil {
		return Runtime{}, err
	}
	return entry, nil
}

func (registry Registry) List() ([]Runtime, error) {
	entries, err := os.ReadDir(registry.Root)
	if err != nil {
		return nil, err
	}
	result := []Runtime{}
	for _, directory := range entries {
		if !directory.IsDir() || strings.HasPrefix(directory.Name(), ".") {
			continue
		}
		entry, err := registry.Load(directory.Name())
		if err != nil {
			return nil, err
		}
		result = append(result, entry)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func (registry Registry) Create(name string, binding image.Binding, argv []string, now time.Time) (Runtime, error) {
	if err := image.ValidateRuntimeName(name); err != nil {
		return Runtime{}, err
	}
	if err := binding.Validate(); err != nil {
		return Runtime{}, err
	}
	if argv == nil {
		argv = []string{}
	}
	for _, argument := range argv {
		if strings.ContainsRune(argument, 0) {
			return Runtime{}, fmt.Errorf("INGOT-RUNTIME-REGISTRY-ARGV: argv contains NUL")
		}
	}
	if _, err := os.Stat(registry.RuntimeHome(name)); err == nil {
		return Runtime{}, fmt.Errorf("INGOT-RUNTIME-REGISTRY-EXISTS: runtime %s already exists", name)
	} else if !os.IsNotExist(err) {
		return Runtime{}, err
	}
	if err := os.MkdirAll(registry.Root, 0o700); err != nil {
		return Runtime{}, err
	}
	staging, err := os.MkdirTemp(registry.Root, ".staging-")
	if err != nil {
		return Runtime{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(staging)
		}
	}()
	for _, path := range []string{"state", "run", "logs"} {
		if err := os.Mkdir(filepath.Join(staging, path), 0o700); err != nil {
			return Runtime{}, err
		}
	}
	now = now.UTC()
	entry := Runtime{RuntimeVersion: RuntimeVersion, Name: name, DesiredImage: binding, DefaultArgv: append([]string{}, argv...), Generation: 1, CreatedAt: now, UpdatedAt: now}
	if err := writeRuntime(filepath.Join(staging, "runtime.json"), entry); err != nil {
		return Runtime{}, err
	}
	if err := os.Rename(staging, registry.RuntimeHome(name)); err != nil {
		return Runtime{}, err
	}
	if err := syncDirectory(registry.Root); err != nil {
		return Runtime{}, err
	}
	committed = true
	return entry, nil
}

func (registry Registry) Switch(name string, binding image.Binding, now time.Time) (Runtime, bool, error) {
	entry, err := registry.Load(name)
	if err != nil {
		return Runtime{}, false, err
	}
	if err := binding.Validate(); err != nil {
		return Runtime{}, false, err
	}
	if entry.DesiredImage.ImageID == binding.ImageID {
		return entry, false, nil
	}
	previous := entry.DesiredImage
	entry.RollbackImage = &previous
	entry.DesiredImage = binding
	entry.Generation++
	entry.UpdatedAt = now.UTC()
	return entry, true, writeRuntime(registry.Path(name), entry)
}

func (registry Registry) Rollback(name string, now time.Time) (Runtime, error) {
	entry, err := registry.Load(name)
	if err != nil {
		return Runtime{}, err
	}
	if entry.RollbackImage == nil {
		return Runtime{}, fmt.Errorf("INGOT-RUNTIME-REGISTRY-ROLLBACK: runtime %s has no rollback image", name)
	}
	oldDesired := entry.DesiredImage
	entry.DesiredImage = *entry.RollbackImage
	entry.RollbackImage = &oldDesired
	entry.Generation++
	entry.UpdatedAt = now.UTC()
	return entry, writeRuntime(registry.Path(name), entry)
}

func (registry Registry) SetCommand(name string, argv []string, now time.Time) (Runtime, bool, error) {
	entry, err := registry.Load(name)
	if err != nil {
		return Runtime{}, false, err
	}
	if argv == nil {
		argv = []string{}
	}
	for _, argument := range argv {
		if strings.ContainsRune(argument, 0) {
			return Runtime{}, false, fmt.Errorf("INGOT-RUNTIME-REGISTRY-ARGV: argv contains NUL")
		}
	}
	if equalStrings(entry.DefaultArgv, argv) {
		return entry, false, nil
	}
	entry.DefaultArgv = append([]string{}, argv...)
	entry.Generation++
	entry.UpdatedAt = now.UTC()
	return entry, true, writeRuntime(registry.Path(name), entry)
}

func (registry Registry) Delete(name string, purge bool) error {
	if err := registry.ValidateDelete(name, purge); err != nil {
		return err
	}
	if err := os.RemoveAll(registry.RuntimeHome(name)); err != nil {
		return err
	}
	return syncDirectory(registry.Root)
}

func (registry Registry) ValidateDelete(name string, purge bool) error {
	if _, err := registry.Load(name); err != nil {
		return err
	}
	home := registry.RuntimeHome(name)
	if !purge {
		for _, child := range []string{"state", "logs"} {
			entries, err := os.ReadDir(filepath.Join(home, child))
			if err != nil {
				return err
			}
			if len(entries) != 0 {
				return fmt.Errorf("INGOT-RUNTIME-REGISTRY-NONEMPTY: %s contains data; use --purge", child)
			}
		}
	}
	return nil
}

func writeRuntime(path string, entry Runtime) error {
	if err := entry.Validate(entry.Name); err != nil {
		return err
	}
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return err
	}
	return image.AtomicWrite(path, append(data, '\n'), 0o600)
}
func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
