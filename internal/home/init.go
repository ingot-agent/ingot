package home

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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
	Home         string       `json:"home"`
	Profile      string       `json:"profile"`
	PluginsPath  string       `json:"plugins_path"`
	ConfigPath   string       `json:"config_path"`
	BundledPath  string       `json:"bundled_path"`
	WrotePlugins bool         `json:"wrote_plugins"`
	WroteConfig  bool         `json:"wrote_config"`
	Plugins      []InitPlugin `json:"plugins"`
}

// Init establishes the initial usable state of an ingot home:
//
//  1. it locates the official plugin set (explicit BundlePath or relative to
//     the executable) and materializes it under <home>/bundled-plugins/
//     (idempotent);
//  2. it writes a default plugins.toml for the selected profile;
//  3. it writes a default config.toml template.
//
// Init never modifies an existing plugins.toml or config.toml unless Force is
// set. Init does not resolve or build; the caller decides whether to apply.
func (home *Home) Init(options InitOptions) (InitResult, error) {
	profile, err := bundle.LookupProfile(options.Profile)
	if err != nil {
		return InitResult{}, err
	}
	result := InitResult{
		Home:        home.Root,
		Profile:     profile.Name,
		PluginsPath: home.DesiredPath(),
		ConfigPath:  home.ConfigPath(),
		BundledPath: filepath.Join(home.Root, bundle.BundledDirectory),
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
	configData, err := renderConfigTOML(home.Root, entries)
	if err != nil {
		return InitResult{}, err
	}
	if err := atomicWrite(home.DesiredPath(), desiredData, 0o600); err != nil {
		return InitResult{}, err
	}
	result.WrotePlugins = true
	if options.Force {
		if err := atomicWrite(home.ConfigPath(), configData, 0o600); err != nil {
			return InitResult{}, err
		}
		result.WroteConfig = true
	} else {
		wrote, err := writeIfMissing(home.ConfigPath(), configData)
		if err != nil {
			return InitResult{}, err
		}
		result.WroteConfig = wrote
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

// renderConfigTOML renders the default runtime config template. Every plugin
// of the profile gets exactly one table (required by the runtime config
// decoder); plugins with required values get a commented sample.
func renderConfigTOML(homeRoot string, entries []bundle.Entry) ([]byte, error) {
	var output bytes.Buffer
	output.WriteString("# ingot runtime configuration.\n")
	output.WriteString("#\n")
	output.WriteString("# Every plugin in the current image needs exactly one [plugins] table, keyed by\n")
	output.WriteString("# canonical module ID or manifest short name. Empty tables are fine when defaults\n")
	output.WriteString("# are acceptable. Unknown tables are ignored; unknown keys inside a known table\n")
	output.WriteString("# are rejected at startup.\n")
	output.WriteString("#\n")
	output.WriteString("# Edit this file and restart the current image for changes to take effect.\n")
	byName := make(map[string]bundle.Entry, len(entries))
	for _, entry := range entries {
		byName[entry.Name] = entry
	}
	write := func(comment string, name string, body string) {
		output.WriteString("\n")
		if comment != "" {
			for _, line := range strings.Split(comment, "\n") {
				output.WriteString("#")
				if line != "" {
					output.WriteString(" ")
				}
				output.WriteString(line)
				output.WriteString("\n")
			}
		}
		_, _ = fmt.Fprintf(&output, "[plugins.%s]\n", strconv.Quote(name))
		output.WriteString(body)
	}
	write("--- model provider: required to run the agent ---\nFill in base_url, api_key and your model names.", "model.openai-compatible", "providers = [\n  { name = \"openai\", base_url = \"https://api.example.com/v1\", api_key = \"\", models = [\"gpt-4o-mini\"] },\n]\n")
	write("--- defaults used when a request leaves provider/model empty ---\nKeep these in sync with the provider name and models above.", "model.runtime", "default_provider = \"openai\"\ndefault_model = \"gpt-4o-mini\"\n")
	write("--- agent loop defaults (optional) ---", "agent.default", "# provider = \"openai\"\n# model = \"gpt-4o-mini\"\n# streaming = true\n# max_tool_rounds = 8\n")
	if _, ok := byName["filesystem.local"]; ok {
		write("--- workspace root for filesystem tools ---\n\".\" means the working directory where you start `ingot chat`.", "filesystem.local", "root = \".\"\n")
	}
	if _, ok := byName["tool.shell"]; ok {
		shell, shellErr := defaultShellPath()
		if shellErr != nil {
			return nil, shellErr
		}
		workingDirectory, _ := os.UserHomeDir()
		if workingDirectory == "" {
			workingDirectory = homeRoot
		}
		write("--- shell tool execution boundary ---\nAbsolute paths required; adjust them for your machine.", "tool.shell", fmt.Sprintf("working_directory = %s\nshell = %s\n# timeout_seconds = 30\n# max_output_bytes = 1048576\n", strconv.Quote(workingDirectory), strconv.Quote(shell)))
	}
	for _, entry := range entries {
		switch entry.Name {
		case "model.openai-compatible", "model.runtime", "agent.default", "filesystem.local", "tool.shell":
			continue
		}
		write("", entry.Name, "")
	}
	return output.Bytes(), nil
}

// defaultShellPath picks an absolute shell executable that exists on this
// machine so the generated config passes startup validation out of the box.
func defaultShellPath() (string, error) {
	var candidates []string
	if shell := os.Getenv("SHELL"); shell != "" {
		candidates = append(candidates, shell)
	}
	if comspec := os.Getenv("ComSpec"); comspec != "" {
		candidates = append(candidates, comspec)
	}
	candidates = append(candidates, "/bin/sh", "/bin/bash", "/usr/bin/sh", "C:\\Windows\\System32\\cmd.exe")
	for _, candidate := range candidates {
		if !filepath.IsAbs(candidate) {
			continue
		}
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no absolute executable shell found on this machine (checked %s)", strings.Join(candidates, ", "))
}
