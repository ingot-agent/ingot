package builder

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"golang.org/x/mod/module"
)

const (
	// BuilderSDKsEnv replaces the configured SDK list with a comma-separated
	// list of module@version entries.
	BuilderSDKsEnv = "INGOT_BUILDER_SDKS"
	// BuilderSDKModuleEnv overrides the module path of the first configured SDK.
	BuilderSDKModuleEnv = "INGOT_BUILDER_SDK_MODULE"
	// BuilderSDKVersionEnv overrides the version of the first configured SDK.
	BuilderSDKVersionEnv = "INGOT_BUILDER_SDK_VERSION"
)

//go:embed default_builder.toml
var defaultBuilderConfigData []byte

// SDKConfig declares one SDK module required by generated runtime images.
// The first SDK is the primary SDK used by the generated runtime support.
type SDKConfig struct {
	Module  string `toml:"module"`
	Version string `toml:"version"`
	Path    string `toml:"path,omitempty"`
}

// BuilderConfig is the user-editable build configuration.
type BuilderConfig struct {
	BuilderConfigVersion int         `toml:"builder_config_version"`
	SDKs                 []SDKConfig `toml:"sdks"`
}

// DefaultBuilderConfig returns the embedded distribution defaults.
func DefaultBuilderConfig() (BuilderConfig, error) {
	return parseBuilderConfig(defaultBuilderConfigData, "embedded default_builder.toml")
}

// LoadBuilderConfig loads path when it exists, otherwise it uses the embedded
// distribution defaults, then applies SDK environment overrides.
func LoadBuilderConfig(path string) (BuilderConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return BuilderConfig{}, diagnostic("INGOT-BUILDER-CONFIG-PARSE", path, "", err)
		}
		data = defaultBuilderConfigData
	}
	config, err := parseBuilderConfig(data, path)
	if err != nil {
		return BuilderConfig{}, err
	}
	if err := config.ApplyEnvironment(os.LookupEnv); err != nil {
		return BuilderConfig{}, err
	}
	for index := range config.SDKs {
		if config.SDKs[index].Path != "" && !filepath.IsAbs(config.SDKs[index].Path) {
			config.SDKs[index].Path = filepath.Join(filepath.Dir(path), filepath.FromSlash(config.SDKs[index].Path))
		}
	}
	return config, nil
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

// ApplyEnvironment applies the supported SDK environment overrides.
func (config *BuilderConfig) ApplyEnvironment(lookup func(string) (string, bool)) error {
	list, hasList := lookup(BuilderSDKsEnv)
	moduleOverride, hasModule := lookup(BuilderSDKModuleEnv)
	versionOverride, hasVersion := lookup(BuilderSDKVersionEnv)
	if hasList && (hasModule || hasVersion) {
		return &Error{Code: "INGOT-BUILDER-CONFIG-ENV-CONFLICT", Field: BuilderSDKsEnv, Want: "either the SDK list or first-SDK overrides, not both"}
	}
	if hasList {
		sdks, err := parseSDKEnvironment(list)
		if err != nil {
			return err
		}
		config.SDKs = sdks
	} else if hasModule || hasVersion {
		if len(config.SDKs) == 0 {
			return &Error{Code: "INGOT-BUILDER-CONFIG-ENV", Field: "sdks", Want: "at least one configured SDK before applying first-SDK overrides"}
		}
		if hasModule {
			config.SDKs[0].Module = strings.TrimSpace(moduleOverride)
		}
		if hasVersion {
			config.SDKs[0].Version = strings.TrimSpace(versionOverride)
		}
	}
	return config.Validate()
}

func parseSDKEnvironment(value string) ([]SDKConfig, error) {
	parts := strings.Split(value, ",")
	if strings.TrimSpace(value) == "" {
		parts = nil
	}
	sdks := make([]SDKConfig, 0, len(parts))
	for index, part := range parts {
		part = strings.TrimSpace(part)
		separator := strings.LastIndex(part, "@")
		if separator <= 0 || separator == len(part)-1 {
			return nil, &Error{Code: "INGOT-BUILDER-CONFIG-ENV", Field: fmt.Sprintf("%s[%d]", BuilderSDKsEnv, index), Actual: part, Want: "module@version"}
		}
		sdks = append(sdks, SDKConfig{Module: part[:separator], Version: part[separator+1:]})
	}
	return sdks, nil
}

// Validate checks the complete builder configuration.
func (config *BuilderConfig) Validate() error {
	if config.BuilderConfigVersion != 1 {
		return &Error{Code: "INGOT-BUILDER-CONFIG-VERSION", Field: "builder_config_version", Want: "1", Actual: fmt.Sprint(config.BuilderConfigVersion)}
	}
	if len(config.SDKs) == 0 {
		return &Error{Code: "INGOT-BUILDER-CONFIG-SDKS", Field: "sdks", Want: "at least one SDK"}
	}
	seen := make(map[string]int, len(config.SDKs))
	for index := range config.SDKs {
		sdk := &config.SDKs[index]
		field := fmt.Sprintf("sdks[%d]", index)
		if err := module.CheckPath(sdk.Module); err != nil {
			return &Error{Code: "INGOT-BUILDER-CONFIG-SDK-MODULE", Field: field + ".module", Actual: sdk.Module, Err: err}
		}
		if previous, ok := seen[sdk.Module]; ok {
			return &Error{Code: "INGOT-BUILDER-CONFIG-DUPLICATE-SDK", Field: field + ".module", Actual: sdk.Module, Want: fmt.Sprintf("unique (first at sdks[%d])", previous)}
		}
		seen[sdk.Module] = index
		if module.CanonicalVersion(sdk.Version) != sdk.Version || module.Check(sdk.Module, sdk.Version) != nil {
			return &Error{Code: "INGOT-BUILDER-CONFIG-SDK-VERSION", Field: field + ".version", Actual: sdk.Version, Want: "canonical version matching the SDK module path"}
		}
		if sdk.Path != "" {
			cleaned, err := cleanDeclarationPath(sdk.Path)
			if err != nil {
				return &Error{Code: "INGOT-BUILDER-CONFIG-SDK-PATH", Field: field + ".path", Actual: sdk.Path, Err: err}
			}
			sdk.Path = cleaned
		}
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
