package home

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/ingot-agent/ingot/internal/builder"
	"github.com/ingot-agent/ingot/internal/bundle"
)

// InitOptions configures ingot init.
type InitOptions struct {
	// Profile selects the official default plugin set: "default" or "minimal".
	Profile string
	// BundlePath points directly at the official plugins distribution
	// directory (the --bundle flag). When empty, the distribution is located
	// relative to the executable.
	BundlePath string
	// Force allows overwriting an already initialized home.
	Force bool
}

// InitPlugin is one plugin written into plugins.toml by init.
type InitPlugin struct {
	Directory string `json:"directory"`
	Module    string `json:"module"`
	Name      string `json:"name"`
}

// InitResult describes the home state established by init.
type InitResult struct {
	Home               string       `json:"home"`
	Profile            string       `json:"profile"`
	PluginsPath        string       `json:"plugins_path"`
	BuilderConfigPath  string       `json:"builder_config_path"`
	BundledPath        string       `json:"bundled_path"`
	WrotePlugins       bool         `json:"wrote_plugins"`
	WroteBuilderConfig bool         `json:"wrote_builder_config"`
	Plugins            []InitPlugin `json:"plugins"`
}

// Init establishes the initial usable state of an ingot home:
//
//  1. it locates the official plugin set (explicit BundlePath or relative to
//     the executable) and materializes it under <home>/bundled-plugins/
//     (idempotent);
//  2. it writes a default plugins.toml for the selected profile;
//  3. it writes the default builder.toml configuration scaffold.
//
// Init does not write any runtime configuration: Plugins own their persistent
// configuration inside their own Runtime state scope and start Unconfigured.
// Init never modifies an existing plugins.toml or builder.toml unless Force
// is set. Init does not resolve or build; the caller decides whether to apply.
func (home *Home) Init(options InitOptions) (InitResult, error) {
	profile, err := bundle.LookupProfile(options.Profile)
	if err != nil {
		return InitResult{}, err
	}
	result := InitResult{
		Home:              home.Root,
		Profile:           profile.Name,
		PluginsPath:       home.DesiredPath(),
		BuilderConfigPath: home.BuilderConfigPath(),
		BundledPath:       filepath.Join(home.Root, bundle.BundledDirectory),
	}
	if !options.Force {
		if _, err := os.Stat(home.DesiredPath()); err == nil {
			return InitResult{}, fmt.Errorf("home %s is already initialized (%s exists); use --force to overwrite", home.Root, home.DesiredPath())
		} else if !os.IsNotExist(err) {
			return InitResult{}, err
		}
	}
	sourceDir, err := bundle.Locate(options.BundlePath)
	if err != nil {
		return InitResult{}, err
	}
	entries, err := bundle.Materialize(sourceDir, home.Root, profile)
	if err != nil {
		return InitResult{}, fmt.Errorf("materialize official plugin bundle: %w", err)
	}
	plugins := make([]builder.DesiredPlugin, len(entries))
	names := make(map[string]bool, len(entries))
	modules := make(map[string]bool, len(entries))
	for i, entry := range entries {
		if names[entry.Name] {
			return InitResult{}, fmt.Errorf("bundled plugin set has duplicate name %q in %s", entry.Name, home.Root)
		}
		if modules[entry.Module] {
			return InitResult{}, fmt.Errorf("bundled plugin set has duplicate module %q", entry.Module)
		}
		names[entry.Name], modules[entry.Module] = true, true
		plugins[i] = builder.DesiredPlugin{Module: entry.Module, Path: pathForBundledPlugin(entry.Directory)}
		result.Plugins = append(result.Plugins, InitPlugin{Directory: entry.Directory, Module: entry.Module, Name: entry.Name})
	}
	desired := builder.NewDesired(home.DesiredPath(), plugins)
	if err := desired.Validate(); err != nil {
		return InitResult{}, err
	}
	desiredData, err := renderDesiredTOML(entries)
	if err != nil {
		return InitResult{}, err
	}
	builderConfig, err := builder.DefaultBuilderConfig()
	if err != nil {
		return InitResult{}, err
	}
	builderConfigData, err := builderConfig.MarshalTOML()
	if err != nil {
		return InitResult{}, err
	}
	if err := atomicWrite(home.DesiredPath(), desiredData, 0o600); err != nil {
		return InitResult{}, err
	}
	result.WrotePlugins = true
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

// pathForBundledPlugin is the plugins.toml path locator for one materialized
// official plugin. It is relative to the home root (the location of
// plugins.toml) and uses slash separators.
func pathForBundledPlugin(directory string) string {
	return bundle.BundledDirectory + "/" + directory
}

// renderDesiredTOML renders a commented, human-editable plugins.toml for the
// official plugin set, preserving semantic identity.
func renderDesiredTOML(entries []bundle.Entry) ([]byte, error) {
	var output bytes.Buffer
	output.WriteString("# ingot desired plugin set.\n")
	output.WriteString("# Generated by `ingot init`. Edit with `ingot plugin ...` or by hand;\n")
	output.WriteString("# the bundled sources are local dev sources managed by ingot under bundled-plugins/.\n")
	output.WriteString("plugins_version = 1\n")
	for _, entry := range entries {
		_, _ = fmt.Fprintf(&output, "\n# %s — %s\n", entry.Name, entry.Directory)
		_, _ = fmt.Fprintf(&output, "[[plugins]]\nmodule = %s\npath = %s\n", strconv.Quote(entry.Module), strconv.Quote(pathForBundledPlugin(entry.Directory)))
	}
	return output.Bytes(), nil
}
