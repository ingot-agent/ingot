// Package home coordinates the M2 managed Home, project builds, images,
// runtimes, and process observations.
package home

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ingot-agent/ingot/internal/builder"
	"github.com/ingot-agent/ingot/internal/image"
	"github.com/ingot-agent/ingot/internal/layout"
)

type Home struct{ Root string }

// Status remains the project inspection payload embedded in Inspection. M2
// intentionally has no current image or deployment fields.
type Status struct {
	DesiredDigest string `json:"desired_digest,omitempty"`
	LockedDigest  string `json:"locked_digest,omitempty"`
	LockedImageID string `json:"locked_image_id,omitempty"`
	DesiredLocked bool   `json:"desired_locked"`
	LockedSources bool   `json:"locked_sources"`
	Built         bool   `json:"built"`
}

type Inspection struct {
	Status                 Status              `json:"status"`
	DirectPlugins          []PluginInspection  `json:"direct_plugins"`
	ComponentCreationOrder []string            `json:"component_creation_order"`
	ManyOrder              map[string][]string `json:"many_order"`
	HostDependencies       map[string][]string `json:"host_dependencies,omitempty"`
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
	home, err := openPath(root)
	if err != nil {
		return nil, err
	}
	if err := home.validateSchema(); err != nil {
		return nil, err
	}
	release, err := home.acquire(context.Background())
	if err != nil {
		return nil, err
	}
	defer release()
	if err := home.recoverHomeTransactions(); err != nil {
		return nil, err
	}
	if err := home.validateSchema(); err != nil {
		return nil, err
	}
	return home, nil
}

// OpenForSupervisor validates the managed Home without acquiring its writer
// lock. A per-process supervisor is launched while its parent CLI holds that
// lock through child spawn acknowledgement, so acquiring it here deadlocks
// startup and violates the M2 lock hierarchy.
func OpenForSupervisor(root string) (*Home, error) {
	home, err := openPath(root)
	if err != nil {
		return nil, err
	}
	if err := home.validateSchema(); err != nil {
		return nil, err
	}
	return home, nil
}

func openPath(root string) (*Home, error) {
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
	return &Home{Root: filepath.Clean(absolute)}, nil
}

func (home *Home) ensure() error {
	for _, directory := range []string{home.Root, filepath.Join(home.Root, "profiles"), filepath.Join(home.Root, "images"), filepath.Join(home.Root, "cache", "gomod"), filepath.Join(home.Root, "runtimes"), filepath.Join(home.Root, ".transactions")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return err
		}
	}
	return nil
}
func (home *Home) BuilderConfigPath() string { return filepath.Join(home.Root, "builder.toml") }
func (home *Home) ProfileRecipePath(profile string) string {
	return filepath.Join(home.Root, "profiles", profile+".toml")
}
func (home *Home) imageDirectory(imageID string) string {
	return filepath.Join(home.Root, "images", layout.ImageDirectoryName(imageID, runtime.GOOS))
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

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	return image.AtomicWrite(path, data, mode)
}

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

func decodeTransactionValue(value string) ([]byte, error) {
	data, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, err
	}
	return data, nil
}
