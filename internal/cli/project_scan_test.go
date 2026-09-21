package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/ingot-agent/ingot/internal/builder"
)

func TestProjectScanCreatesRecipeWithAbsolutePluginPaths(t *testing.T) {
	root := t.TempDir()
	pluginsRoot := filepath.Join(root, "plugins")
	projectRoot := filepath.Join(root, "project")
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	writeCLIPlugin(t, pluginsRoot, "second", "example.com/plugins/second")
	writeCLIPlugin(t, pluginsRoot, "first", "example.com/plugins/first")
	t.Chdir(projectRoot)

	missingHome := filepath.Join(root, "missing-home")
	var stdout, stderr bytes.Buffer
	code := (CLI{Stdout: &stdout, Stderr: &stderr}).Run(context.Background(), []string{
		"--home", missingHome, "--json", "project", "scan", pluginsRoot,
	})
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	var result scanCommandResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.SourceDirectory != pluginsRoot || result.Recipe != filepath.Join(projectRoot, "plugins.toml") {
		t.Fatalf("result = %#v", result)
	}
	if _, err := os.Stat(missingHome); !os.IsNotExist(err) {
		t.Fatalf("project scan touched Home: %v", err)
	}
	desired, err := builder.ParseDesired(result.Recipe)
	if err != nil {
		t.Fatal(err)
	}
	wantModules := []string{"example.com/plugins/first", "example.com/plugins/second"}
	if len(desired.Plugins) != len(wantModules) {
		t.Fatalf("plugins = %#v", desired.Plugins)
	}
	for index, plugin := range desired.Plugins {
		if plugin.Module != wantModules[index] {
			t.Fatalf("plugins[%d].module = %q", index, plugin.Module)
		}
		if !filepath.IsAbs(plugin.Path) || plugin.Path != filepath.Join(pluginsRoot, filepath.Base(plugin.Path)) {
			t.Fatalf("plugins[%d].path = %q", index, plugin.Path)
		}
	}
}

func TestProjectScanRequiresForceToOverwrite(t *testing.T) {
	root := t.TempDir()
	pluginsRoot := filepath.Join(root, "plugins")
	writeCLIPlugin(t, pluginsRoot, "one", "example.com/plugins/one")
	recipePath := filepath.Join(root, "plugins.toml")
	command := CLI{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	if code := command.Run(context.Background(), []string{"project", "scan", pluginsRoot, "-o", recipePath}); code != 0 {
		t.Fatalf("initial exit = %d", code)
	}
	original, err := os.ReadFile(recipePath)
	if err != nil {
		t.Fatal(err)
	}
	writeCLIPlugin(t, pluginsRoot, "two", "example.com/plugins/two")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if code := command.Run(context.Background(), []string{"project", "scan", pluginsRoot, "-o", recipePath}); code != 1 {
		t.Fatalf("overwrite exit=%d stderr=%s", code, stderr.String())
	}
	unchanged, err := os.ReadFile(recipePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(unchanged, original) {
		t.Fatal("existing recipe changed without --force")
	}
	if code := command.Run(context.Background(), []string{"project", "scan", pluginsRoot, "-o", recipePath, "--force"}); code != 0 {
		t.Fatalf("forced exit = %d", code)
	}
	desired, err := builder.ParseDesired(recipePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(desired.Plugins) != 2 {
		t.Fatalf("plugins = %#v", desired.Plugins)
	}
}

func writeCLIPlugin(t *testing.T, root, directory, modulePath string) {
	t.Helper()
	pluginRoot := filepath.Join(root, directory)
	if err := os.MkdirAll(pluginRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginRoot, "go.mod"), []byte("module "+modulePath+"\n\ngo 1.24.2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := "manifest_version = 1\nname = " + strconv.Quote(directory) + "\ningot = \">=0.3.0 <0.4.0\"\nconfig_package = \".\"\n\n[[components]]\nname = \"default\"\npackage = \".\"\n"
	if err := os.WriteFile(filepath.Join(pluginRoot, "ingot.plugin.toml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
}
