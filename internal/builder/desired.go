package builder

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"golang.org/x/mod/module"
)

// DesiredPlugins is the semantic form of plugins.toml v1.
type DesiredPlugins struct {
	PluginsVersion int             `toml:"plugins_version"`
	Plugins        []DesiredPlugin `toml:"plugins"`
	filePath       string
}

// DesiredPlugin describes one ordered direct plugin.
type DesiredPlugin struct {
	Module  string `toml:"module"`
	Version string `toml:"version,omitempty"`
	Path    string `toml:"path,omitempty"`
}

// NewDesired constructs a v1 semantic desired-state model whose relative paths
// resolve from filePath.
func NewDesired(filePath string, plugins []DesiredPlugin) *DesiredPlugins {
	return &DesiredPlugins{PluginsVersion: 1, Plugins: append([]DesiredPlugin(nil), plugins...), filePath: filePath}
}

// ParseDesired strictly parses and validates a plugins.toml file.
func ParseDesired(filePath string) (*DesiredPlugins, error) {
	var desired DesiredPlugins
	if err := decodeExactFile(filePath, &desired, "INGOT-PLUGINS-PARSE"); err != nil {
		return nil, err
	}
	desired.filePath = filePath
	if err := desired.Validate(); err != nil {
		if diagnosticErr, ok := err.(*Error); ok && diagnosticErr.Path == "" {
			diagnosticErr.Path = filePath
		}
		return nil, err
	}
	return &desired, nil
}

// Validate checks the complete v1 desired-state schema.
func (d *DesiredPlugins) Validate() error {
	if d.PluginsVersion != 1 {
		return &Error{Code: "INGOT-PLUGINS-UNSUPPORTED-VERSION", Field: "plugins_version", Want: "1", Actual: fmt.Sprint(d.PluginsVersion)}
	}
	if len(d.Plugins) == 0 {
		return &Error{Code: "INGOT-PLUGINS-EMPTY", Field: "plugins", Want: "at least one plugin", Actual: "empty"}
	}
	seen := make(map[string]int, len(d.Plugins))
	for i := range d.Plugins {
		plugin := &d.Plugins[i]
		field := fmt.Sprintf("plugins[%d]", i)
		if err := module.CheckPath(plugin.Module); err != nil {
			return &Error{Code: "INGOT-PLUGINS-MODULE", Field: field + ".module", Actual: plugin.Module, Err: err}
		}
		if previous, ok := seen[plugin.Module]; ok {
			return &Error{Code: "INGOT-PLUGINS-DUPLICATE-MODULE", Field: field + ".module", Actual: plugin.Module, Want: fmt.Sprintf("unique (first at plugins[%d])", previous)}
		}
		seen[plugin.Module] = i
		if (plugin.Version == "") == (plugin.Path == "") {
			return &Error{Code: "INGOT-PLUGINS-SOURCE-UNION", Field: field, Want: "exactly one of version or path"}
		}
		if plugin.Version != "" {
			if module.CanonicalVersion(plugin.Version) != plugin.Version {
				return &Error{Code: "INGOT-PLUGINS-VERSION", Field: field + ".version", Want: "canonical exact Go module version", Actual: plugin.Version}
			}
			if err := module.Check(plugin.Module, plugin.Version); err != nil {
				return &Error{Code: "INGOT-PLUGINS-MODULE-VERSION", Field: field + ".version", Actual: plugin.Version, Err: err}
			}
		} else {
			cleaned, err := cleanDeclarationPath(plugin.Path)
			if err != nil {
				return &Error{Code: "INGOT-PLUGINS-PATH", Field: field + ".path", Actual: plugin.Path, Err: err}
			}
			plugin.Path = cleaned
		}
	}
	return nil
}

func cleanDeclarationPath(value string) (string, error) {
	if value == "" || strings.ContainsRune(value, 0) {
		return "", fmt.Errorf("path is empty or contains NUL")
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value), nil
	}
	// Declaration paths have slash semantics independent of the host. Accept a
	// host separator on Windows only through filepath.ToSlash.
	cleaned := path.Clean(filepath.ToSlash(value))
	if cleaned == "." && value != "." {
		return "", fmt.Errorf("path resolves to an empty locator")
	}
	return cleaned, nil
}

// ResolvePath converts a local declaration locator to an absolute clean path.
func (d *DesiredPlugins) ResolvePath(locator string) (string, error) {
	if filepath.IsAbs(locator) {
		return filepath.Clean(locator), nil
	}
	base := "."
	if d.filePath != "" {
		base = filepath.Dir(d.filePath)
	}
	return filepath.Abs(filepath.Join(base, filepath.FromSlash(locator)))
}

type desiredProjection struct {
	SchemaVersion int                       `json:"schema_version"`
	Plugins       []desiredPluginProjection `json:"plugins"`
}

type desiredPluginProjection struct {
	Module string                  `json:"module"`
	Source desiredSourceProjection `json:"source"`
}

type desiredSourceProjection struct {
	Kind    string `json:"kind"`
	Version string `json:"version,omitempty"`
	Path    string `json:"path,omitempty"`
}

// CanonicalJSON returns CanonicalDesiredPluginsV1 in RFC 8785 form.
func (d *DesiredPlugins) CanonicalJSON() ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	projection := desiredProjection{SchemaVersion: 1, Plugins: make([]desiredPluginProjection, len(d.Plugins))}
	for i, plugin := range d.Plugins {
		source := desiredSourceProjection{Kind: "path", Path: plugin.Path}
		if plugin.Version != "" {
			source = desiredSourceProjection{Kind: "module", Version: plugin.Version}
		}
		projection.Plugins[i] = desiredPluginProjection{Module: plugin.Module, Source: source}
	}
	return canonicalJSON(projection)
}

// Digest returns the semantic plugins_digest.
func (d *DesiredPlugins) Digest() (string, error) {
	data, err := d.CanonicalJSON()
	if err != nil {
		return "", err
	}
	return digestBytes(data), nil
}

// MarshalTOML returns a stable, order-preserving plugins.toml representation.
func (d *DesiredPlugins) MarshalTOML() ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return writeTOML(d)
}
