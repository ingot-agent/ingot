package builder

import (
	_ "embed"
	"fmt"
	"os"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

//go:embed default_builder.toml
var defaultBuilderConfigData []byte

// BuilderConfig is the user-editable build configuration. There is no
// configurable SDK list anymore: contract modules are ordinary Go modules
// imported by plugins, and the ingot ABI is pinned by the Builder itself.
type BuilderConfig struct {
	BuilderConfigVersion int `toml:"builder_config_version"`
}

// DefaultBuilderConfig returns the embedded distribution defaults.
func DefaultBuilderConfig() (BuilderConfig, error) {
	return parseBuilderConfig(defaultBuilderConfigData, "embedded default_builder.toml")
}

// LoadBuilderConfig loads path when it exists, otherwise it uses the embedded
// distribution defaults.
func LoadBuilderConfig(path string) (BuilderConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return BuilderConfig{}, diagnostic("INGOT-BUILDER-CONFIG-PARSE", path, "", err)
		}
		data = defaultBuilderConfigData
	}
	return parseBuilderConfig(data, path)
}

func parseBuilderConfig(data []byte, path string) (BuilderConfig, error) {
	var config BuilderConfig
	decoder := toml.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return BuilderConfig{}, diagnostic("INGOT-BUILDER-CONFIG-PARSE", path, "", err)
	}
	if err := config.Validate(); err != nil {
		if diagnosticErr, ok := err.(*Error); ok && diagnosticErr.Path == "" {
			diagnosticErr.Path = path
		}
		return BuilderConfig{}, err
	}
	return config, nil
}

// Validate checks the complete builder configuration.
func (config *BuilderConfig) Validate() error {
	if config.BuilderConfigVersion != 1 {
		return &Error{Code: "INGOT-BUILDER-CONFIG-VERSION", Field: "builder_config_version", Want: "1", Actual: fmt.Sprint(config.BuilderConfigVersion)}
	}
	return nil
}

// MarshalTOML returns a stable builder.toml representation.
func (config *BuilderConfig) MarshalTOML() ([]byte, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return writeTOML(config)
}
