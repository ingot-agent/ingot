package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ingot-agent/ingot/internal/coreupdate"
	ingothome "github.com/ingot-agent/ingot/internal/home"
	"github.com/spf13/cobra"
)

func TestSetupInitializesHomeWithoutWritingCurrentDirectory(t *testing.T) {
	project := t.TempDir()
	t.Chdir(project)
	home := filepath.Join(t.TempDir(), "home")
	var stdout, stderr bytes.Buffer
	command := CLI{Stdout: &stdout, Stderr: &stderr}
	code := command.Run(context.Background(), []string{"setup", "--home", home, "--profile", "minimal", "--json"})
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	var result ingothome.InitResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Home != home || result.Profile != "minimal" {
		t.Fatalf("result=%#v", result)
	}
	if _, err := os.Stat(filepath.Join(project, "plugins.toml")); !os.IsNotExist(err) {
		t.Fatalf("setup wrote to cwd: %v", err)
	}
}

func TestInitCreatesProjectAndHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	project := filepath.Join(t.TempDir(), "project")
	var stdout, stderr bytes.Buffer
	command := CLI{Stdout: &stdout, Stderr: &stderr}
	code := command.Run(context.Background(), []string{"--home", home, "init", project, "--profile", "minimal", "--json"})
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	var result initCommandResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Project.PluginsPath != filepath.Join(project, "plugins.toml") {
		t.Fatalf("project=%#v", result.Project)
	}
	if _, err := os.Stat(filepath.Join(home, "home.json")); err != nil {
		t.Fatal(err)
	}
}

func TestNoArgumentsShowsHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := (CLI{Stdout: &stdout, Stderr: &stderr}).Run(context.Background(), nil)
	if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "Common Commands:") || !strings.Contains(stdout.String(), "up") {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func TestVersionDoesNotOpenHome(t *testing.T) {
	missingHome := filepath.Join(t.TempDir(), "missing")
	var stdout, stderr bytes.Buffer
	code := (CLI{Stdout: &stdout, Stderr: &stderr}).Run(context.Background(), []string{"version", "--home", missingHome, "--json"})
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	var result coreVersionResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.CoreVersion == "" || result.IngotVersion == "" || result.BuilderVersion == "" {
		t.Fatalf("result=%#v", result)
	}
	if _, err := os.Stat(missingHome); !os.IsNotExist(err) {
		t.Fatalf("version touched Home: %v", err)
	}
}

func TestVersionUsesJSONForStagedUpdateCandidate(t *testing.T) {
	candidate := filepath.Join(t.TempDir(), ".ingot-update-candidate-1234")
	if !versionJSONOutput(false, candidate) {
		t.Fatal("staged update candidate did not select JSON output")
	}
	if versionJSONOutput(false, filepath.Join(t.TempDir(), "ingot")) {
		t.Fatal("normal executable selected JSON output without --json")
	}
}

func TestUpdateUsesInjectedUpdater(t *testing.T) {
	var received coreupdate.Options
	var stdout, stderr bytes.Buffer
	command := CLI{
		Stdout: &stdout,
		Stderr: &stderr,
		updateCore: func(_ context.Context, options coreupdate.Options) (coreupdate.Result, error) {
			received = options
			return coreupdate.Result{CurrentVersion: "0.3.1", TargetVersion: "0.4.0", UpdateAvailable: true}, nil
		},
	}
	if code := command.Run(context.Background(), []string{"update", "--check", "--version", "v0.4.0", "--json"}); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	if !received.Check || received.Version != "v0.4.0" || received.Force {
		t.Fatalf("options=%#v", received)
	}
}

func TestUsageErrorsReturnTwo(t *testing.T) {
	for _, arguments := range [][]string{
		{"init", "one", "two"},
		{"update", "--check", "--force"},
		{"runtime", "switch", "only-one"},
		{"does-not-exist"},
	} {
		var stderr bytes.Buffer
		code := (CLI{Stdout: &bytes.Buffer{}, Stderr: &stderr}).Run(context.Background(), arguments)
		if code != 2 {
			t.Fatalf("%v exit=%d stderr=%s", arguments, code, stderr.String())
		}
	}
}

func TestDiscoverRecipeUsesNearestParent(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	nested := filepath.Join(project, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	recipe := filepath.Join(project, "plugins.toml")
	if err := os.WriteFile(recipe, []byte("plugins_version=1\nplugins=[]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := discoverRecipe(nested)
	if err != nil {
		t.Fatal(err)
	}
	if got != recipe {
		t.Fatalf("got %s, want %s", got, recipe)
	}
}

func TestCompletionGenerationDoesNotRequireHome(t *testing.T) {
	missingHome := filepath.Join(t.TempDir(), "missing")
	var stdout, stderr bytes.Buffer
	code := (CLI{Stdout: &stdout, Stderr: &stderr}).Run(context.Background(), []string{"completion", "zsh", "--home", missingHome})
	if code != 0 || !strings.Contains(stdout.String(), "compdef") {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(missingHome); !os.IsNotExist(err) {
		t.Fatalf("completion touched Home: %v", err)
	}
}

func TestDynamicCompletionDoesNotCreateHome(t *testing.T) {
	missingHome := filepath.Join(t.TempDir(), "missing")
	var stdout, stderr bytes.Buffer
	code := (CLI{Stdout: &stdout, Stderr: &stderr}).Run(context.Background(), []string{"--home", missingHome, "__complete", "start", ""})
	if code != 0 {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(missingHome); !os.IsNotExist(err) {
		t.Fatalf("dynamic completion touched Home: %v", err)
	}
}

func TestOptionalRuntimeNameDefaultsAndSelects(t *testing.T) {
	if got := optionalRuntimeName(nil); got != defaultRuntimeName {
		t.Fatalf("default runtime = %q", got)
	}
	if got := optionalRuntimeName([]string{"work"}); got != "work" {
		t.Fatalf("selected runtime = %q", got)
	}
}

func TestSplitRuntimeArgv(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		before   []string
		argv     []string
		hasArgv  bool
		wantCode int
	}{
		{name: "default", args: nil, before: []string{}},
		{name: "named", args: []string{"work"}, before: []string{"work"}},
		{name: "bare dash", args: []string{"--"}, before: []string{}, argv: []string{}, hasArgv: true},
		{name: "default command", args: []string{"--", "serve", "--port", "8080"}, before: []string{}, argv: []string{"serve", "--port", "8080"}, hasArgv: true},
		{name: "named command", args: []string{"work", "--", "serve"}, before: []string{"work"}, argv: []string{"serve"}, hasArgv: true},
		{name: "too many runtimes", args: []string{"one", "two"}, wantCode: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var before, argv []string
			var hasArgv bool
			command := &cobra.Command{
				Use:          "up",
				SilenceUsage: true,
				RunE: func(command *cobra.Command, args []string) error {
					var err error
					before, argv, hasArgv, err = splitRuntimeArgv(command, args)
					return err
				},
			}
			command.SetArgs(test.args)
			err := command.Execute()
			if test.wantCode != 0 {
				var commandErr commandError
				if !errors.As(err, &commandErr) || commandErr.code != test.wantCode {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(before, "\x00") != strings.Join(test.before, "\x00") || strings.Join(argv, "\x00") != strings.Join(test.argv, "\x00") || hasArgv != test.hasArgv {
				t.Fatalf("before=%q argv=%q hasArgv=%t", before, argv, hasArgv)
			}
		})
	}
}
