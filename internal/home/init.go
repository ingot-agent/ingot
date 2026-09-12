package home

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ingot-agent/ingot/internal/builder"
	"github.com/ingot-agent/ingot/internal/bundle"
	"github.com/ingot-agent/ingot/internal/image"
)

// InitOptions configures ingot init.
type InitOptions struct {
	// Profile selects the official default plugin set: "default" or "minimal".
	Profile string
	// BundlePath points directly at the official plugins distribution
	// directory (the --bundle flag). When empty, the distribution is located
	// relative to the executable.
	BundlePath string
	// Force rewrites managed configuration even when its content is unchanged.
	Force bool
}

// InitPlugin is one plugin selected from an official profile.
type InitPlugin struct {
	Directory string `json:"directory"`
	Module    string `json:"module"`
	Name      string `json:"name"`
}

// InitResult describes the home state established by init.
type InitResult struct {
	Home               string       `json:"home"`
	Profile            string       `json:"profile"`
	ProfileRecipePath  string       `json:"profile_recipe_path"`
	BuilderConfigPath  string       `json:"builder_config_path"`
	BundledPath        string       `json:"bundled_path"`
	WroteProfileRecipe bool         `json:"wrote_profile_recipe"`
	WroteBuilderConfig bool         `json:"wrote_builder_config"`
	Plugins            []InitPlugin `json:"plugins"`
}

// ProjectInitOptions configures explicit creation of a project recipe.
type ProjectInitOptions struct {
	Directory string
	Profile   string
	Force     bool
}

// ProjectInitResult describes a project recipe created from an official profile.
type ProjectInitResult struct {
	Project     string       `json:"project"`
	Profile     string       `json:"profile"`
	PluginsPath string       `json:"plugins_path"`
	Plugins     []InitPlugin `json:"plugins"`
}

// Init establishes the initial usable state of an ingot home:
//
//  1. it locates the official plugin set (explicit BundlePath or relative to
//     the executable) and materializes it under <home>/bundled-plugins/
//     (idempotent);
//  2. it writes a managed profile recipe under <home>/profiles/;
//  3. it writes the default builder.toml configuration scaffold.
//
// Init does not write any runtime configuration: Plugins own their persistent
// configuration inside their own Runtime state scope and start Unconfigured.
// Init never writes to the CLI working directory. The profile recipe is derived
// managed state and is refreshed when its expected content changes. Init does
// not resolve, build, or create a Runtime.
func (home *Home) Init(options InitOptions) (InitResult, error) {
	profile, err := bundle.LookupProfile(options.Profile)
	if err != nil {
		return InitResult{}, err
	}
	sourceDir, err := bundle.Locate(options.BundlePath)
	if err != nil {
		return InitResult{}, err
	}
	if err := home.initializeSchema(); err != nil {
		return InitResult{}, err
	}
	profileRecipePath := home.ProfileRecipePath(profile.Name)
	result := InitResult{
		Home:              home.Root,
		Profile:           profile.Name,
		ProfileRecipePath: profileRecipePath,
		BuilderConfigPath: home.BuilderConfigPath(),
		BundledPath:       filepath.Join(home.Root, bundle.BundledDirectory),
	}
	entries, err := bundle.Materialize(sourceDir, home.Root, profile)
	if err != nil {
		return InitResult{}, fmt.Errorf("materialize official plugin bundle: %w", err)
	}
	desiredData, plugins, err := renderProfileRecipe(entries, result.BundledPath, filepath.Dir(profileRecipePath), profileRecipePath, true)
	if err != nil {
		return InitResult{}, err
	}
	result.Plugins = plugins
	builderConfig, err := builder.DefaultBuilderConfig()
	if err != nil {
		return InitResult{}, err
	}
	builderConfigData, err := builderConfig.MarshalTOML()
	if err != nil {
		return InitResult{}, err
	}
	wrote, err := writeManagedProfileRecipe(profileRecipePath, desiredData, options.Force)
	if err != nil {
		return InitResult{}, err
	}
	result.WroteProfileRecipe = wrote
	if options.Force {
		if err := atomicWrite(home.BuilderConfigPath(), builderConfigData, 0o600); err != nil {
			return InitResult{}, err
		}
		result.WroteBuilderConfig = true
	} else {
		wrote, err := writeIfMissing(home.BuilderConfigPath(), builderConfigData)
		if err != nil {
			return InitResult{}, err
		}
		result.WroteBuilderConfig = wrote
	}
	return result, nil
}

// InitProject creates plugins.toml only in the explicitly selected directory.
func (home *Home) InitProject(ctx context.Context, options ProjectInitOptions) (ProjectInitResult, error) {
	if options.Directory == "" {
		return ProjectInitResult{}, fmt.Errorf("project directory is required")
	}
	profile, err := bundle.LookupProfile(options.Profile)
	if err != nil {
		return ProjectInitResult{}, err
	}
	projectDirectory, err := filepath.Abs(options.Directory)
	if err != nil {
		return ProjectInitResult{}, err
	}
	if err := os.MkdirAll(projectDirectory, 0o755); err != nil {
		return ProjectInitResult{}, err
	}
	info, err := os.Stat(projectDirectory)
	if err != nil {
		return ProjectInitResult{}, err
	}
	if !info.IsDir() {
		return ProjectInitResult{}, fmt.Errorf("project path %s is not a directory", projectDirectory)
	}
	pluginsPath := filepath.Join(projectDirectory, "plugins.toml")
	paths := ProjectPaths{Recipe: pluginsPath, Lock: filepath.Join(projectDirectory, "plugins.lock")}
	release, err := acquireProjectLock(ctx, projectWriterLockPath(paths))
	if err != nil {
		return ProjectInitResult{}, err
	}
	defer release()
	if err := recoverProjectTransaction(paths); err != nil {
		return ProjectInitResult{}, err
	}
	if _, err := os.Lstat(pluginsPath); err == nil && !options.Force {
		return ProjectInitResult{}, fmt.Errorf("INGOT-PROJECT-INIT-EXISTS: %s already exists; pass --force to overwrite it", pluginsPath)
	} else if err != nil && !os.IsNotExist(err) {
		return ProjectInitResult{}, err
	}
	entries, err := bundle.ManagedEntries(home.Root, profile)
	if err != nil {
		return ProjectInitResult{}, fmt.Errorf("read managed plugin profile: %w", err)
	}
	data, plugins, err := renderProfileRecipe(entries, filepath.Join(home.Root, bundle.BundledDirectory), projectDirectory, pluginsPath, false)
	if err != nil {
		return ProjectInitResult{}, err
	}
	if err := image.AtomicWriteUserFile(pluginsPath, data, 0o644); err != nil {
		return ProjectInitResult{}, err
	}
	return ProjectInitResult{Project: projectDirectory, Profile: profile.Name, PluginsPath: pluginsPath, Plugins: plugins}, nil
}

func writeManagedProfileRecipe(path string, data []byte, force bool) (bool, error) {
	paths := ProjectPaths{Recipe: path, Lock: strings.TrimSuffix(path, filepath.Ext(path)) + ".lock"}
	release, err := acquireProjectLock(context.Background(), projectWriterLockPath(paths))
	if err != nil {
		return false, err
	}
	defer release()
	if err := recoverProjectTransaction(paths); err != nil {
		return false, err
	}
	existing, err := os.ReadFile(path)
	if err == nil && bytes.Equal(existing, data) && !force {
		return false, nil
	}
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	if err := atomicWrite(path, data, 0o600); err != nil {
		return false, err
	}
	return true, nil
}

func writeIfMissing(path string, data []byte) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if err := atomicWrite(path, data, 0o600); err != nil {
		return false, err
	}
	return true, nil
}

// pathForBundledPlugin is the recipe path locator for one materialized
// official plugin. It is relative to the recipe directory and uses slash
// separators.
func pathForBundledPlugin(bundledPath, recipeDirectory, directory string) (string, error) {
	absolute := filepath.Join(bundledPath, directory)
	relative, err := filepath.Rel(recipeDirectory, absolute)
	if err != nil {
		return filepath.Clean(absolute), nil
	}
	return filepath.ToSlash(relative), nil
}

func renderProfileRecipe(entries []bundle.Entry, bundledPath, recipeDirectory, recipePath string, managed bool) ([]byte, []InitPlugin, error) {
	plugins := make([]builder.DesiredPlugin, len(entries))
	locators := make([]string, len(entries))
	result := make([]InitPlugin, 0, len(entries))
	names := make(map[string]bool, len(entries))
	modules := make(map[string]bool, len(entries))
	for i, entry := range entries {
		if names[entry.Name] {
			return nil, nil, fmt.Errorf("bundled plugin set has duplicate name %q", entry.Name)
		}
		if modules[entry.Module] {
			return nil, nil, fmt.Errorf("bundled plugin set has duplicate module %q", entry.Module)
		}
		names[entry.Name], modules[entry.Module] = true, true
		locator, err := pathForBundledPlugin(bundledPath, recipeDirectory, entry.Directory)
		if err != nil {
			return nil, nil, err
		}
		locators[i] = locator
		plugins[i] = builder.DesiredPlugin{Module: entry.Module, Path: locator}
		result = append(result, InitPlugin{Directory: entry.Directory, Module: entry.Module, Name: entry.Name})
	}
	desired := builder.NewDesired(recipePath, plugins)
	if err := desired.Validate(); err != nil {
		return nil, nil, err
	}
	data, err := renderDesiredTOML(entries, locators, managed)
	return data, result, err
}

// renderDesiredTOML renders a commented, human-editable plugins.toml for the
// official plugin set, preserving semantic identity.
func renderDesiredTOML(entries []bundle.Entry, locators []string, managed bool) ([]byte, error) {
	var output bytes.Buffer
	output.WriteString("# ingot desired plugin set.\n")
	if managed {
		output.WriteString("# Managed by `ingot init`; local edits may be replaced.\n")
	} else {
		output.WriteString("# Generated by `ingot project init`. Edit with `ingot plugin ...` or by hand.\n")
	}
	output.WriteString("# the bundled sources are local dev sources managed by ingot under bundled-plugins/.\n")
	output.WriteString("plugins_version = 1\n")
	for index, entry := range entries {
		_, _ = fmt.Fprintf(&output, "\n# %s — %s\n", entry.Name, entry.Directory)
		_, _ = fmt.Fprintf(&output, "[[plugins]]\nmodule = %s\npath = %s\n", strconv.Quote(entry.Module), strconv.Quote(locators[index]))
	}
	return output.Bytes(), nil
}
