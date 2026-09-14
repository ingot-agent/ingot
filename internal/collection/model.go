// Package collection parses reusable Plugin composition recipes and plans
// their application to a project's direct Plugin set.
package collection

import (
	"bytes"
	"fmt"
	"net/url"
	"strings"
	"unicode"

	"github.com/ingot-agent/ingot/internal/canonicaljson"
	"github.com/pelletier/go-toml/v2"
	"golang.org/x/mod/module"
)

const SchemaVersion = 1

// Metadata is the small, portable display surface carried by Collection v1.
type Metadata struct {
	Name        string `toml:"name" json:"name"`
	Description string `toml:"description,omitempty" json:"description,omitempty"`
	Homepage    string `toml:"homepage,omitempty" json:"homepage,omitempty"`
}

// Plugin is one exact released Plugin reference in Collection order.
type Plugin struct {
	Module  string `toml:"module" json:"module"`
	Version string `toml:"version" json:"version"`
}

// Collection is the semantic form of an ingot Collection v1 document.
type Collection struct {
	CollectionSchema int      `toml:"collection_schema" json:"collection_schema"`
	ID               string   `toml:"id" json:"id"`
	Version          string   `toml:"version" json:"version"`
	Metadata         Metadata `toml:"metadata" json:"metadata"`
	Plugins          []Plugin `toml:"plugins" json:"plugins"`
}

// Loaded records the normalized source and semantic identity of a Collection.
type Loaded struct {
	Source     string     `json:"source"`
	Digest     string     `json:"digest"`
	Collection Collection `json:"collection"`
}

// Parse strictly parses and validates one Collection document.
func Parse(data []byte, source string) (Loaded, error) {
	var value Collection
	decoder := toml.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return Loaded{}, fmt.Errorf("INGOT-COLLECTION-PARSE: %s: %w", source, err)
	}
	if err := value.Validate(); err != nil {
		return Loaded{}, fmt.Errorf("%s: %w", source, err)
	}
	digest, err := value.Digest()
	if err != nil {
		return Loaded{}, err
	}
	return Loaded{Source: source, Digest: digest, Collection: value}, nil
}

// Validate checks the complete Collection v1 semantic contract.
func (value *Collection) Validate() error {
	if value.CollectionSchema != SchemaVersion {
		return fmt.Errorf("INGOT-COLLECTION-UNSUPPORTED-VERSION: collection_schema: expected %d, got %d", SchemaVersion, value.CollectionSchema)
	}
	if err := module.CheckPath(value.ID); err != nil {
		return fmt.Errorf("INGOT-COLLECTION-ID: %q: %w", value.ID, err)
	}
	if module.CanonicalVersion(value.Version) != value.Version {
		return fmt.Errorf("INGOT-COLLECTION-VERSION: expected canonical exact version, got %q", value.Version)
	}
	if err := module.Check(value.ID, value.Version); err != nil {
		return fmt.Errorf("INGOT-COLLECTION-VERSION: %s@%s: %w", value.ID, value.Version, err)
	}
	if err := validateText("metadata.name", value.Metadata.Name, true); err != nil {
		return err
	}
	if err := validateText("metadata.description", value.Metadata.Description, false); err != nil {
		return err
	}
	if value.Metadata.Homepage != "" {
		if err := validateText("metadata.homepage", value.Metadata.Homepage, false); err != nil {
			return err
		}
		homepage, err := url.Parse(value.Metadata.Homepage)
		if err != nil || (homepage.Scheme != "http" && homepage.Scheme != "https") || homepage.Host == "" || homepage.User != nil {
			return fmt.Errorf("INGOT-COLLECTION-METADATA: metadata.homepage must be an absolute HTTP(S) URL without credentials")
		}
	}
	if len(value.Plugins) == 0 {
		return fmt.Errorf("INGOT-COLLECTION-EMPTY: plugins must contain at least one Plugin")
	}
	seen := make(map[string]int, len(value.Plugins))
	for index, plugin := range value.Plugins {
		field := fmt.Sprintf("plugins[%d]", index)
		if err := module.CheckPath(plugin.Module); err != nil {
			return fmt.Errorf("INGOT-COLLECTION-PLUGIN-MODULE: %s.module %q: %w", field, plugin.Module, err)
		}
		if previous, exists := seen[plugin.Module]; exists {
			return fmt.Errorf("INGOT-COLLECTION-DUPLICATE-PLUGIN: %s.module %q duplicates plugins[%d]", field, plugin.Module, previous)
		}
		seen[plugin.Module] = index
		if module.CanonicalVersion(plugin.Version) != plugin.Version {
			return fmt.Errorf("INGOT-COLLECTION-PLUGIN-VERSION: %s.version: expected canonical exact version, got %q", field, plugin.Version)
		}
		if err := module.Check(plugin.Module, plugin.Version); err != nil {
			return fmt.Errorf("INGOT-COLLECTION-PLUGIN-VERSION: %s %s@%s: %w", field, plugin.Module, plugin.Version, err)
		}
	}
	return nil
}

func validateText(field, value string, required bool) error {
	if value == "" {
		if required {
			return fmt.Errorf("INGOT-COLLECTION-METADATA: %s must be non-empty", field)
		}
		return nil
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("INGOT-COLLECTION-METADATA: %s must not have surrounding whitespace", field)
	}
	for _, character := range value {
		if character == 0 || (unicode.IsControl(character) && character != '\n' && character != '\t') {
			return fmt.Errorf("INGOT-COLLECTION-METADATA: %s contains a control character", field)
		}
	}
	return nil
}

// CanonicalJSON returns the stable semantic projection used for identity.
func (value *Collection) CanonicalJSON() ([]byte, error) {
	if err := value.Validate(); err != nil {
		return nil, err
	}
	return canonicaljson.Marshal(value)
}

// Digest returns the semantic Collection SHA-256 identity.
func (value *Collection) Digest() (string, error) {
	canonical, err := value.CanonicalJSON()
	if err != nil {
		return "", err
	}
	return canonicaljson.Digest(canonical), nil
}
