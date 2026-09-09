package home

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/ingot-agent/ingot/internal/builder"
)

func testBundleSource(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "plugins")
}

func initHome(t *testing.T) *Home {
	t.Helper()
	home, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return home
}

func TestInitWritesDefaultProfile(t *testing.T) {
	t.Parallel()
	home := initHome(t)
	result, err := home.Init(InitOptions{BundlePath: testBundleSource(t)})
	if err != nil {
		t.Fatal(err)
	}
	if !result.WrotePlugins || !result.WroteBuilderConfig {
		t.Fatalf("init must write plugins and builder config files: %#v", result)
	}
	if len(result.Plugins) != 11 {
		t.Fatalf("default profile has %d plugins, want 11: %#v", len(result.Plugins), result.Plugins)

	}
	desired, err := builder.ParseDesired(home.DesiredPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(desired.Plugins) != 11 {
		t.Fatalf("plugins.toml has %d plugins, want 11", len(desired.Plugins))

	}
	builderData, err := os.ReadFile(home.BuilderConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	var builderConfig builder.BuilderConfig
	if err := toml.Unmarshal(builderData, &builderConfig); err != nil {
		t.Fatal(err)
	}
	if err := builderConfig.Validate(); err != nil {
		t.Fatal(err)
	}
	if builderConfig.BuilderConfigVersion != 1 {
		t.Fatalf("default builder config = %#v", builderConfig)
	}
	for index, plugin := range desired.Plugins {
		if plugin.Version != "" {
			t.Fatalf("bundled plugin %s must be a local source, not a remote version", plugin.Module)
		}
		absolute, err := desired.ResolvePath(plugin.Path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(absolute, "ingot.plugin.toml")); err != nil {
			t.Fatalf("plugin %s materialized at %s: %v", plugin.Module, absolute, err)
		}
		if !strings.HasPrefix(plugin.Path, "bundled-plugins/") {
			t.Fatalf("plugin %s path %q is not under bundled-plugins", plugin.Module, plugin.Path)
		}
		if entry := result.Plugins[index]; entry.Module != plugin.Module {
			t.Fatalf("result entry %d module %s does not match plugins.toml %s", index, entry.Module, plugin.Module)
		}
	}
	// init must not create any unified runtime configuration: plugins own
	// their persistent configuration inside their Runtime state scope.
	if _, err := os.Stat(filepath.Join(home.Root, "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("init must not write a unified config.toml: %v", err)
	}
}

func TestInitIsIdempotentAndForceOverwrites(t *testing.T) {
	t.Parallel()
	home := initHome(t)
	if _, err := home.Init(InitOptions{BundlePath: testBundleSource(t)}); err != nil {
		t.Fatal(err)
	}
	if _, err := home.Init(InitOptions{BundlePath: testBundleSource(t)}); err == nil || !strings.Contains(err.Error(), "already initialized") {
		t.Fatalf("second init must fail: %v", err)
	}
	if _, err := home.Init(InitOptions{BundlePath: testBundleSource(t), Force: true}); err != nil {
		t.Fatalf("forced init must succeed: %v", err)
	}
	if _, err := builder.ParseDesired(home.DesiredPath()); err != nil {
		t.Fatal(err)
	}
}

func TestInitDoesNotCreateRuntimeConfig(t *testing.T) {
	t.Parallel()
	home := initHome(t)
	if _, err := home.Init(InitOptions{BundlePath: testBundleSource(t)}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home.Root, "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("init must not create a unified runtime config.toml: %v", err)
	}
	// The plugin set is initialized so apply is the only missing step.
	if _, err := builder.ParseDesired(home.DesiredPath()); err != nil {
		t.Fatal(err)
	}
}

func TestInitMinimalProfile(t *testing.T) {
	t.Parallel()
	home := initHome(t)
	result, err := home.Init(InitOptions{Profile: "minimal", BundlePath: testBundleSource(t)})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Plugins) != 9 {
		t.Fatalf("minimal profile has %d plugins, want 9: %#v", len(result.Plugins), result.Plugins)
	}
	desired, err := builder.ParseDesired(home.DesiredPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, plugin := range desired.Plugins {
		if plugin.Module == "github.com/ingot-agent/tool-shell" {
			t.Fatal("minimal profile must not include tool.shell")
		}
	}
}

func TestInitRejectsUnknownProfile(t *testing.T) {
	t.Parallel()
	home := initHome(t)
	if _, err := home.Init(InitOptions{Profile: "nope", BundlePath: testBundleSource(t)}); err == nil || !strings.Contains(err.Error(), "unknown profile") {
		t.Fatalf("unknown profile must fail: %v", err)
	}
}

func TestInitRejectsBrokenBundle(t *testing.T) {
	t.Parallel()
	home := initHome(t)
	if _, err := home.Init(InitOptions{BundlePath: t.TempDir()}); err == nil {
		t.Fatal("init with a missing plugin bundle must fail")
	}
}
