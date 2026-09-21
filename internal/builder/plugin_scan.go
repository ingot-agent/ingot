package builder

import (
	"fmt"
	"os"
	"path/filepath"
)

// ScanPluginDirectory builds a local-source recipe from the plugin modules in
// sourceDirectory. Plugins must be direct child directories containing both a
// go.mod and an ingot.plugin.toml file.
func ScanPluginDirectory(sourceDirectory, recipePath string) (*DesiredPlugins, error) {
	sourceRoot, err := filepath.Abs(sourceDirectory)
	if err != nil {
		return nil, fmt.Errorf("resolve plugin directory: %w", err)
	}
	info, err := os.Stat(sourceRoot)
	if err != nil {
		return nil, fmt.Errorf("read plugin directory %s: %w", sourceRoot, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("plugin directory %s is not a directory", sourceRoot)
	}
	recipe, err := filepath.Abs(recipePath)
	if err != nil {
		return nil, fmt.Errorf("resolve recipe path: %w", err)
	}
	entries, err := os.ReadDir(sourceRoot)
	if err != nil {
		return nil, fmt.Errorf("read plugin directory %s: %w", sourceRoot, err)
	}

	plugins := make([]DesiredPlugin, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pluginRoot := filepath.Join(sourceRoot, entry.Name())
		goModPath := filepath.Join(pluginRoot, "go.mod")
		manifestPath := filepath.Join(pluginRoot, "ingot.plugin.toml")
		goModExists, err := regularFileExists(goModPath)
		if err != nil {
			return nil, err
		}
		manifestExists, err := regularFileExists(manifestPath)
		if err != nil {
			return nil, err
		}
		if !goModExists || !manifestExists {
			continue
		}
		modulePath, err := ModuleIdentity(goModPath)
		if err != nil {
			return nil, err
		}
		if _, err := ParseManifest(manifestPath); err != nil {
			return nil, err
		}
		plugins = append(plugins, DesiredPlugin{Module: modulePath, Path: pluginRoot})
	}
	if len(plugins) == 0 {
		return nil, fmt.Errorf("no plugins found in %s", sourceRoot)
	}
	desired := NewDesired(recipe, plugins)
	if err := desired.Validate(); err != nil {
		return nil, err
	}
	return desired, nil
}

func regularFileExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect plugin marker %s: %w", path, err)
	}
	return info.Mode().IsRegular(), nil
}
