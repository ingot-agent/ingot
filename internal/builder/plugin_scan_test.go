package builder

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestScanPluginDirectoryBuildsSortedLocalRecipe(t *testing.T) {
	root := t.TempDir()
	pluginsRoot := filepath.Join(root, "plugins")
	projectRoot := filepath.Join(root, "project")
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	writeScannedPlugin(t, pluginsRoot, "zeta", "example.com/plugins/zeta", "zeta")
	writeScannedPlugin(t, pluginsRoot, "alpha", "example.com/plugins/alpha", "alpha")
	if err := os.MkdirAll(filepath.Join(pluginsRoot, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginsRoot, "README.md"), []byte("plugins\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	recipePath := filepath.Join(projectRoot, "plugins.toml")
	desired, err := ScanPluginDirectory(pluginsRoot, recipePath)
	if err != nil {
		t.Fatal(err)
	}
	want := []DesiredPlugin{
		{Module: "example.com/plugins/alpha", Path: filepath.Join(pluginsRoot, "alpha")},
		{Module: "example.com/plugins/zeta", Path: filepath.Join(pluginsRoot, "zeta")},
	}
	if len(desired.Plugins) != len(want) {
		t.Fatalf("plugins = %#v", desired.Plugins)
	}
	for index := range want {
		if desired.Plugins[index] != want[index] {
			t.Fatalf("plugins[%d] = %#v, want %#v", index, desired.Plugins[index], want[index])
		}
	}
}

func TestScanPluginDirectoryRejectsInvalidPlugin(t *testing.T) {
	root := t.TempDir()
	writeScannedPlugin(t, root, "broken", "example.com/plugins/broken", "INVALID NAME")
	_, err := ScanPluginDirectory(root, filepath.Join(t.TempDir(), "plugins.toml"))
	if err == nil || !strings.Contains(err.Error(), "INGOT-MANIFEST-NAME") {
		t.Fatalf("error = %v", err)
	}
}

func TestScanPluginDirectoryRejectsEmptyDirectory(t *testing.T) {
	root := t.TempDir()
	if _, err := ScanPluginDirectory(root, filepath.Join(t.TempDir(), "plugins.toml")); err == nil || !strings.Contains(err.Error(), "no plugins found") {
		t.Fatalf("error = %v", err)
	}
}

func writeScannedPlugin(t *testing.T, root, directory, modulePath, name string) {
	t.Helper()
	pluginRoot := filepath.Join(root, directory)
	if err := os.MkdirAll(pluginRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginRoot, "go.mod"), []byte("module "+modulePath+"\n\ngo 1.24.2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := "manifest_version = 1\nname = " + strconv.Quote(name) + "\ningot = \">=0.3.0 <0.4.0\"\nconfig_package = \".\"\n\n[[components]]\nname = \"default\"\npackage = \".\"\n"
	if err := os.WriteFile(filepath.Join(pluginRoot, "ingot.plugin.toml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
}
