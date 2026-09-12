package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitCreatesM2HomeWithoutWritingCurrentDirectory(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	t.Chdir(project)
	home := t.TempDir()
	var stdout, stderr bytes.Buffer
	command := CLI{Stdout: &stdout, Stderr: &stderr}
	code := command.Run(context.Background(), []string{"--home", home, "init", "--profile", "minimal", "--bundle", filepath.Join(repositoryRoot, "plugins")})
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	var result struct {
		ProfileRecipePath string `json:"profile_recipe_path"`
		Home              string `json:"home"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Home != home || result.ProfileRecipePath != filepath.Join(home, "profiles", "minimal.toml") {
		t.Fatalf("result=%#v", result)
	}
	if _, err := os.Stat(filepath.Join(project, "plugins.toml")); !os.IsNotExist(err) {
		t.Fatalf("init wrote to cwd: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "home.json")); err != nil {
		t.Fatal(err)
	}
}

func TestProjectInitRequiresExplicitDirectory(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	project := filepath.Join(t.TempDir(), "project")
	command := CLI{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	if code := command.Run(context.Background(), []string{"--home", home, "init", "--bundle", filepath.Join(repositoryRoot, "plugins")}); code != 0 {
		t.Fatal(code)
	}
	var stdout, stderr bytes.Buffer
	command = CLI{Stdout: &stdout, Stderr: &stderr}
	if code := command.Run(context.Background(), []string{"--home", home, "project", "init", project, "--profile", "minimal"}); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(project, "plugins.toml")); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	if code := command.Run(context.Background(), []string{"--home", home, "project", "init"}); code != 2 {
		t.Fatalf("missing directory exit=%d", code)
	}
	stderr.Reset()
	if code := command.Run(context.Background(), []string{"--home", home, "project", "init", "--bogus"}); code != 2 {
		t.Fatalf("unknown option exit=%d", code)
	}
}

func TestRemovedApplyAndUnknownDispatchAreUsageErrors(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	t.Chdir(project)
	home := t.TempDir()
	command := CLI{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	if code := command.Run(context.Background(), []string{"--home", home, "init", "--profile", "minimal", "--bundle", filepath.Join(repositoryRoot, "plugins")}); code != 0 {
		t.Fatalf("init exit=%d", code)
	}
	for _, arguments := range [][]string{{"--home", home, "apply"}, {"--home", home, "web"}} {
		var stderr bytes.Buffer
		command = CLI{Stdout: &bytes.Buffer{}, Stderr: &stderr}
		if code := command.Run(context.Background(), arguments); code != 2 {
			t.Fatalf("%v exit=%d stderr=%s", arguments, code, stderr.String())
		}
		if !strings.Contains(stderr.String(), "removed") && !strings.Contains(stderr.String(), "unknown command") {
			t.Fatalf("stderr=%s", stderr.String())
		}
	}
}

func TestBundleUpdateRejectsApply(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	t.Chdir(project)
	home := t.TempDir()
	command := CLI{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	if code := command.Run(context.Background(), []string{"--home", home, "init", "--profile", "minimal", "--bundle", filepath.Join(repositoryRoot, "plugins")}); code != 0 {
		t.Fatal(code)
	}
	var stderr bytes.Buffer
	command = CLI{Stdout: &bytes.Buffer{}, Stderr: &stderr}
	if code := command.Run(context.Background(), []string{"--home", home, "bundle", "update", "--apply", "--bundle", filepath.Join(repositoryRoot, "plugins")}); code != 2 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
}
