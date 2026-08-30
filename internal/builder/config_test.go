package builder

import (
	"path/filepath"
	"testing"
)

func TestBuilderConfigRejectsUnknownFieldsAndVersions(t *testing.T) {
	t.Parallel()
	if _, err := parseBuilderConfig([]byte(`
builder_config_version = 1

[[sdks]]
module = "example.com/sdk"
version = "v1.0.0"
`), "builder.toml"); err == nil {
		t.Fatal("legacy [[sdks]] configuration was accepted")
	}
	if _, err := parseBuilderConfig([]byte("builder_config_version = 2\n"), "builder.toml"); err == nil {
		t.Fatal("unsupported version was accepted")
	}
	config, err := parseBuilderConfig([]byte("builder_config_version = 1\n"), "builder.toml")
	if err != nil {
		t.Fatal(err)
	}
	if config.BuilderConfigVersion != 1 {
		t.Fatalf("version = %d", config.BuilderConfigVersion)
	}
}

func TestDefaultBuilderConfigIsMinimal(t *testing.T) {
	t.Parallel()
	config, err := DefaultBuilderConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.BuilderConfigVersion != 1 {
		t.Fatalf("default version = %d", config.BuilderConfigVersion)
	}
	data, err := config.MarshalTOML()
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "builder_config_version = 1\n" {
		t.Fatalf("default configuration = %q", data)
	}
}

func TestLoadBuilderConfigUsesDefaultsWhenMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "builder.toml")
	config, err := LoadBuilderConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.BuilderConfigVersion != 1 {
		t.Fatalf("version = %d", config.BuilderConfigVersion)
	}
	t.Setenv("INGOT_BUILDER_SDKS", "example.com/sdk@v1.0.0")
	if _, err := LoadBuilderConfig(path); err != nil {
		t.Fatalf("legacy SDK environment override must be ignored: %v", err)
	}
	if _, err := LoadBuilderConfig(filepath.Join(t.TempDir(), "missing", "builder.toml")); err != nil {
		t.Fatalf("missing configuration must fall back to defaults: %v", err)
	}
}
