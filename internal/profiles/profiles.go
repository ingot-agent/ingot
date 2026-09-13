// Package profiles contains the released Official Plugin compositions shipped
// by the Ingot Core release.
package profiles

import (
	"bytes"
	"embed"
	"fmt"
	"path"
	"strings"

	"github.com/ingot-agent/ingot/internal/builder"
	"github.com/pelletier/go-toml/v2"
)

// profileFiles contains the exact plugin versions selected for each Core
// profile. These files are source data for the profile loader and are not
// mutable runtime configuration.
//
//go:embed *.toml
var profileFiles embed.FS

const officialModulePrefix = "github.com/ingot-agent/plugins/"

// Plugin is one released plugin selected by an Official Profile.
type Plugin struct {
	Directory string
	Module    string
	Version   string
	Name      string
}

// Profile is an ordered, exact-version Official Plugin composition.
type Profile struct {
	Name    string
	Plugins []Plugin
}

type profileFile struct {
	PluginsVersion int                     `toml:"plugins_version"`
	Plugins        []builder.DesiredPlugin `toml:"plugins"`
}

// The short names are manifest identity, not version authority. Keeping them
// here lets init report the same human-readable identity without reading a
// local plugin source tree.
var officialPluginNames = map[string]string{
	"agent-default":           "agent.default",
	"app-webui":               "app.backend",
	"asset-local":             "asset.local",
	"context-compact":         "context.compact",
	"http-default":            "http.default",
	"interceptor-approval":    "interceptor.approval",
	"interceptor-script":      "interceptor.script",
	"model-openai-compatible": "model.openai-compatible",
	"model-runtime":           "model.runtime",
	"prompt-default":          "prompt.default",
	"session-sqlite":          "session.sqlite",
	"tool-ask":                "tool.ask",
	"tool-edit":               "tool.edit",
	"tool-runtime":            "tool.runtime",
	"tool-shell":              "tool.shell",
	"usage-default":           "usage.default",
}

// Lookup returns a copy of the named released Official Profile.
func Lookup(name string) (*Profile, error) {
	if name == "" {
		name = "default"
	}
	data, err := profileFiles.ReadFile(name + ".toml")
	if err != nil {
		return nil, fmt.Errorf("unknown profile %q", name)
	}
	var source profileFile
	decoder := toml.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&source); err != nil {
		return nil, fmt.Errorf("parse official profile %q: %w", name, err)
	}
	if source.PluginsVersion != 1 {
		return nil, fmt.Errorf("validate official profile %q: unsupported plugins_version %d", name, source.PluginsVersion)
	}
	desired := builder.NewDesired("profiles/"+name+".toml", source.Plugins)
	if err := desired.Validate(); err != nil {
		return nil, fmt.Errorf("validate official profile %q: %w", name, err)
	}
	profile := &Profile{Name: name, Plugins: make([]Plugin, len(source.Plugins))}
	for index, declaration := range source.Plugins {
		directory, err := pluginDirectory(declaration.Module)
		if err != nil {
			return nil, fmt.Errorf("validate official profile %q plugin %d: %w", name, index, err)
		}
		shortName, ok := officialPluginNames[directory]
		if !ok {
			return nil, fmt.Errorf("validate official profile %q plugin %d: unknown official plugin %q", name, index, directory)
		}
		profile.Plugins[index] = Plugin{Directory: directory, Module: declaration.Module, Version: declaration.Version, Name: shortName}
	}
	return profile, nil
}

func pluginDirectory(module string) (string, error) {
	if !strings.HasPrefix(module, officialModulePrefix) {
		return "", fmt.Errorf("module %q must use %s", module, officialModulePrefix)
	}
	remaining := strings.TrimPrefix(module, officialModulePrefix)
	if remaining == "" || strings.Contains(remaining, "/") {
		return "", fmt.Errorf("module %q must identify one first-level official plugin", module)
	}
	directory := path.Base(module)
	if directory == "." || directory == "/" {
		return "", fmt.Errorf("module %q has no plugin directory", module)
	}
	return directory, nil
}
