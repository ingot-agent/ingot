package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ingot-agent/ingot/internal/coreupdate"
)

func TestInitCreatesM2HomeWithoutWritingCurrentDirectory(t *testing.T) {
	project := t.TempDir()
	t.Chdir(project)
	home := t.TempDir()
	var stdout, stderr bytes.Buffer
	command := CLI{Stdout: &stdout, Stderr: &stderr}
	code := command.Run(context.Background(), []string{"--home", home, "init", "--profile", "minimal"})
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
	home := t.TempDir()
	project := filepath.Join(t.TempDir(), "project")
	command := CLI{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	if code := command.Run(context.Background(), []string{"--home", home, "init"}); code != 0 {
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
	project := t.TempDir()
	t.Chdir(project)
	home := t.TempDir()
	command := CLI{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	if code := command.Run(context.Background(), []string{"--home", home, "init", "--profile", "minimal"}); code != 0 {
		t.Fatalf("init exit=%d", code)
	}
	for _, arguments := range [][]string{{"--home", home, "apply"}, {"--home", home, "web"}, {"--home", home, "bundle"}} {
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

func TestExtractBoolOptionAcceptsShortAlias(t *testing.T) {
	for _, option := range []string{"-d", "--detach"} {
		remaining, found, err := extractBoolOption([]string{"image", option}, "detach", "-d")
		if err != nil || !found || len(remaining) != 1 || remaining[0] != "image" {
			t.Fatalf("option %s: remaining=%v found=%t err=%v", option, remaining, found, err)
		}
	}
	if _, _, err := extractBoolOption([]string{"-d", "--detach"}, "detach", "-d"); err == nil || !strings.Contains(err.Error(), "only once") {
		t.Fatalf("duplicate detach error = %v", err)
	}
	if _, _, err := extractBoolOption([]string{"-d=true"}, "detach", "-d"); err == nil || !strings.Contains(err.Error(), "does not take a value") {
		t.Fatalf("valued detach error = %v", err)
	}
}

func TestVersionCommandsDoNotOpenHome(t *testing.T) {
	missingHome := filepath.Join(t.TempDir(), "must-not-be-created")
	for _, arguments := range [][]string{{"--version"}, {"version"}} {
		var stdout, stderr bytes.Buffer
		command := CLI{Stdout: &stdout, Stderr: &stderr}
		t.Setenv("INGOT_HOME", missingHome)
		if code := command.Run(context.Background(), arguments); code != 0 {
			t.Fatalf("%v exit=%d stderr=%s", arguments, code, stderr.String())
		}
		if _, err := os.Stat(missingHome); !os.IsNotExist(err) {
			t.Fatalf("%v touched INGOT_HOME: %v", arguments, err)
		}
	}
}

func TestStructuredVersionIncludesIndependentIdentities(t *testing.T) {
	var stdout, stderr bytes.Buffer
	command := CLI{Stdout: &stdout, Stderr: &stderr}
	if code := command.Run(context.Background(), []string{"version"}); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	var result coreVersionResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.CoreVersion == "" || result.IngotVersion == "" || result.BuilderVersion == "" || result.Target == "" {
		t.Fatalf("version result = %#v", result)
	}
}

func TestVersionRejectsHomeAndArguments(t *testing.T) {
	command := CLI{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	for _, arguments := range [][]string{{"--home", t.TempDir(), "version"}, {"--home", t.TempDir(), "--version"}} {
		if code := command.Run(context.Background(), arguments); code != 2 {
			t.Fatalf("%v exit=%d", arguments, code)
		}
	}
	if code := command.Run(context.Background(), []string{"version", "extra"}); code != 2 {
		t.Fatalf("version extra exit=%d", code)
	}
}

func TestUpdateParsingAndHomeBypass(t *testing.T) {
	missingHome := filepath.Join(t.TempDir(), "must-not-be-created")
	var received coreupdate.Options
	called := false
	var stdout, stderr bytes.Buffer
	command := CLI{
		Stdout: &stdout,
		Stderr: &stderr,
		updateCore: func(_ context.Context, options coreupdate.Options) (coreupdate.Result, error) {
			called = true
			received = options
			return coreupdate.Result{CurrentVersion: "0.3.1-dev", TargetVersion: "0.3.1", UpdateAvailable: true}, nil
		},
	}
	t.Setenv("INGOT_HOME", missingHome)
	if code := command.Run(context.Background(), []string{"update", "--check", "--version", "v0.3.1"}); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if !called || !received.Check || received.Version != "v0.3.1" || received.Force {
		t.Fatalf("update options = %#v, called=%t", received, called)
	}
	if _, err := os.Stat(missingHome); !os.IsNotExist(err) {
		t.Fatalf("update touched INGOT_HOME: %v", err)
	}
}

func TestUpdateRejectsInvalidFlagCombinations(t *testing.T) {
	called := false
	command := CLI{
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
		updateCore: func(context.Context, coreupdate.Options) (coreupdate.Result, error) {
			called = true
			return coreupdate.Result{}, nil
		},
	}
	for _, arguments := range [][]string{
		{"update", "--check", "--force"},
		{"update", "extra"},
		{"--home", t.TempDir(), "update"},
	} {
		if code := command.Run(context.Background(), arguments); code != 2 {
			t.Fatalf("%v exit=%d", arguments, code)
		}
	}
	if called {
		t.Fatal("invalid update arguments called updater")
	}
}
