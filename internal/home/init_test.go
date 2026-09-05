package home

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/ingot-agent/ingot/internal/builder"
	"github.com/ingot-agent/ingot/internal/prompts"
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
	if !result.WrotePlugins || !result.WroteBuilderConfig || !result.WroteConfig {
		t.Fatalf("init must write plugins, builder, and runtime config files: %#v", result)
	}
	if len(result.Plugins) != 12 {
		t.Fatalf("default profile has %d plugins, want 12: %#v", len(result.Plugins), result.Plugins)

	}
	desired, err := builder.ParseDesired(home.DesiredPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(desired.Plugins) != 12 {
		t.Fatalf("plugins.toml has %d plugins, want 12", len(desired.Plugins))

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
	assertEveryPluginHasConfigTable(t, home, result.Plugins)
	configData, err := os.ReadFile(home.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(configData), "streaming =") {
		t.Fatalf("config.toml advertises the ignored agent streaming key:\n%s", configData)
	}
	var document struct {
		Plugins map[string]struct {
			SystemPrompt string `toml:"system_prompt"`
		} `toml:"plugins"`
	}
	if err := toml.Unmarshal(configData, &document); err != nil {
		t.Fatalf("config.toml does not parse: %v\n%s", err, configData)
	}
	promptConfig, ok := document.Plugins["prompt.default"]
	if !ok {
		t.Fatal("default config lacks [plugins.\"prompt.default\"]")
	}
	if promptConfig.SystemPrompt != prompts.CodingAgent() {
		t.Fatal("default config system_prompt does not match the official coding-agent prompt")
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

func TestInitPreservesExistingConfig(t *testing.T) {
	t.Parallel()
	home := initHome(t)
	custom := "# my custom config\n"
	if err := os.WriteFile(home.ConfigPath(), []byte(custom), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := home.Init(InitOptions{BundlePath: testBundleSource(t)})
	if err != nil {
		t.Fatal(err)
	}
	if result.WroteConfig {
		t.Fatal("init must not overwrite an existing config.toml")
	}
	data, err := os.ReadFile(home.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != custom {
		t.Fatalf("config.toml was modified: %q", data)
	}
	// The plugin set is still initialized so apply is the only missing step.
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
	configData, err := os.ReadFile(home.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Plugins map[string]map[string]any `toml:"plugins"`
	}
	if err := toml.Unmarshal(configData, &document); err != nil {
		t.Fatalf("config.toml does not parse: %v\n%s", err, configData)
	}
	if _, ok := document.Plugins["filesystem.local"]; ok {
		t.Fatal("minimal profile must not configure filesystem.local")
	}
	for _, entry := range result.Plugins {
		if _, ok := document.Plugins[entry.Name]; !ok {
			t.Fatalf("minimal config lacks table for %s (%s)", entry.Name, entry.Module)
		}
	}
	promptConfig := document.Plugins["prompt.default"]
	if value, ok := promptConfig["system_prompt"]; ok {
		prompt, isString := value.(string)
		if !isString || prompt != "" {
			t.Fatalf("minimal config must not contain a default system prompt: %#v", value)
		}
	}
}

func TestRenderTOMLMultilineStringRoundTrip(t *testing.T) {
	original := "You are \"Ingot\".\n\nRun:\ngo test ./...\n\nWindows:\nC:\\workspace\\ingot\r\n中文测试。\ncontrol:\b\f\x01\x7f\t\ntail\\"
	data := []byte("value = " + renderTOMLMultilineString(original) + "\n")
	var document struct {
		Value string `toml:"value"`
	}
	if err := toml.Unmarshal(data, &document); err != nil {
		t.Fatalf("generated TOML does not parse: %v\n%s", err, data)
	}
	if document.Value != original {
		t.Fatalf("TOML round-trip changed the value:\n got %q\nwant %q", document.Value, original)
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

// assertEveryPluginHasConfigTable verifies the strict runtime config
// requirement: every locked plugin needs exactly one matching [plugins] table.
func assertEveryPluginHasConfigTable(t *testing.T, home *Home, plugins []InitPlugin) {
	t.Helper()
	configData, err := os.ReadFile(home.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Plugins map[string]map[string]any `toml:"plugins"`
	}
	if err := toml.Unmarshal(configData, &document); err != nil {
		t.Fatalf("config.toml does not parse: %v\n%s", err, configData)
	}
	for _, entry := range plugins {
		_, ok := document.Plugins[entry.Name]
		if !ok {
			t.Fatalf("config.toml lacks [plugins.%q] table for %s", entry.Name, entry.Module)
		}
	}
}
