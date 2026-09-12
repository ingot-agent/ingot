package home

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ingot-agent/ingot/internal/builder"
	"github.com/ingot-agent/ingot/internal/image"
)

func testBundleSource(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "plugins")
}

func initHome(t *testing.T) (*Home, string) {
	t.Helper()
	root := t.TempDir()
	project := t.TempDir()
	home, err := OpenForInit(root)
	if err != nil {
		t.Fatal(err)
	}
	return home, project
}

func TestInitWritesSchemaCatalogAndManagedProfileRecipe(t *testing.T) {
	home, project := initHome(t)
	bundleSource := testBundleSource(t)
	t.Chdir(project)
	result, err := home.Init(InitOptions{BundlePath: bundleSource})
	if err != nil {
		t.Fatal(err)
	}
	if !result.WroteProfileRecipe || !result.WroteBuilderConfig {
		t.Fatalf("result = %#v", result)
	}
	if result.ProfileRecipePath != filepath.Join(home.Root, "profiles", "default.toml") {
		t.Fatalf("recipe path = %s", result.ProfileRecipePath)
	}
	if _, err := os.Stat(filepath.Join(project, "plugins.toml")); !os.IsNotExist(err) {
		t.Fatalf("init wrote to current directory: %v", err)
	}
	desired, err := builder.ParseDesired(result.ProfileRecipePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(desired.Plugins) != 11 {
		t.Fatalf("plugins = %d", len(desired.Plugins))
	}
	for _, plugin := range desired.Plugins {
		absolute, err := desired.ResolvePath(plugin.Path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(absolute, "ingot.plugin.toml")); err != nil {
			t.Fatalf("plugin locator %q: %v", plugin.Path, err)
		}
	}
	if _, err := image.LoadCatalog(home.CatalogPath()); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(home.Root); err != nil {
		t.Fatal(err)
	}
	for _, legacy := range []string{"current", "state"} {
		if _, err := os.Stat(filepath.Join(home.Root, legacy)); !os.IsNotExist(err) {
			t.Fatalf("legacy path %s exists", legacy)
		}
	}
}

func TestInitIsIdempotentAndRefreshesManagedProfileRecipe(t *testing.T) {
	home, _ := initHome(t)
	options := InitOptions{BundlePath: testBundleSource(t)}
	first, err := home.Init(options)
	if err != nil {
		t.Fatal(err)
	}
	second, err := home.Init(options)
	if err != nil {
		t.Fatal(err)
	}
	if second.WroteProfileRecipe {
		t.Fatal("idempotent init rewrote an unchanged managed recipe")
	}
	corrupt := []byte("plugins_version = 1\nplugins = []\n")
	if err := os.WriteFile(first.ProfileRecipePath, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	refreshed, err := home.Init(options)
	if err != nil {
		t.Fatal(err)
	}
	if !refreshed.WroteProfileRecipe {
		t.Fatal("init did not restore the managed recipe")
	}
	if data, _ := os.ReadFile(first.ProfileRecipePath); string(data) == string(corrupt) {
		t.Fatal("managed recipe remained corrupt")
	}
	forced, err := home.Init(InitOptions{BundlePath: testBundleSource(t), Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if !forced.WroteProfileRecipe || !forced.WroteBuilderConfig {
		t.Fatal("force did not rewrite managed configuration")
	}
}

func TestInitProjectRequiresExplicitDirectoryAndDoesNotOverwrite(t *testing.T) {
	home, project := initHome(t)
	if _, err := home.Init(InitOptions{BundlePath: testBundleSource(t)}); err != nil {
		t.Fatal(err)
	}
	result, err := home.InitProject(context.Background(), ProjectInitOptions{Directory: project, Profile: "minimal"})
	if err != nil {
		t.Fatal(err)
	}
	if result.PluginsPath != filepath.Join(project, "plugins.toml") {
		t.Fatalf("plugins path = %s", result.PluginsPath)
	}
	desired, err := builder.ParseDesired(result.PluginsPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(desired.Plugins) != 9 {
		t.Fatalf("plugins = %d", len(desired.Plugins))
	}
	original, err := os.ReadFile(result.PluginsPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := home.InitProject(context.Background(), ProjectInitOptions{Directory: project}); err == nil {
		t.Fatal("project init overwrote an existing recipe")
	}
	data, _ := os.ReadFile(result.PluginsPath)
	if string(data) != string(original) {
		t.Fatal("existing project recipe changed")
	}
	if _, err := home.InitProject(context.Background(), ProjectInitOptions{Directory: project, Force: true}); err != nil {
		t.Fatal(err)
	}
}

func TestInitMinimalProfile(t *testing.T) {
	home, _ := initHome(t)
	result, err := home.Init(InitOptions{Profile: "minimal", BundlePath: testBundleSource(t)})
	if err != nil {
		t.Fatal(err)
	}
	desired, err := builder.ParseDesired(result.ProfileRecipePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(desired.Plugins) != 9 {
		t.Fatalf("plugins = %d", len(desired.Plugins))
	}
}

func TestInitRejectsNonEmptyIncompatibleHome(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "unknown"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenForInit(root); err == nil {
		t.Fatal("non-empty incompatible home accepted")
	}
}
