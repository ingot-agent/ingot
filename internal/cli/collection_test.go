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

func TestCollectionInspectDoesNotRequireHome(t *testing.T) {
	directory := t.TempDir()
	collectionPath := writeCLICollection(t, directory, []string{"a"})
	missingHome := filepath.Join(directory, "missing-home")
	var stdout, stderr bytes.Buffer
	command := CLI{Stdout: &stdout, Stderr: &stderr}
	code := command.Run(context.Background(), []string{"collection", "inspect", collectionPath, "--home", missingHome, "--json"})
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	var output struct {
		Digest string `json:"digest"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil || !strings.HasPrefix(output.Digest, "sha256:") {
		t.Fatalf("output=%s err=%v", stdout.String(), err)
	}
	if _, err := os.Stat(missingHome); !os.IsNotExist(err) {
		t.Fatalf("inspect touched Home: %v", err)
	}
}

func TestCollectionPlanConflictAndApplyFailure(t *testing.T) {
	project := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	recipe := filepath.Join(project, "plugins.toml")
	if err := os.WriteFile(recipe, []byte("plugins_version=1\n[[plugins]]\nmodule='example.com/plugins/a'\nversion='v1.0.0'\n[[plugins]]\nmodule='example.com/plugins/b'\nversion='v1.0.0'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	collectionPath := writeCLICollection(t, project, []string{"b", "a"})
	if code := (CLI{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}).Run(context.Background(), []string{"setup", "--home", home, "--profile", "minimal"}); code != 0 {
		t.Fatalf("setup exit=%d", code)
	}

	var stdout, stderr bytes.Buffer
	command := CLI{Stdout: &stdout, Stderr: &stderr}
	code := command.Run(context.Background(), []string{"collection", "plan", collectionPath, "--file", recipe, "--home", home, "--json"})
	if code != 0 {
		t.Fatalf("plan exit=%d stderr=%s", code, stderr.String())
	}
	var output struct {
		Plan struct {
			Applicable bool `json:"applicable"`
			Order      struct {
				Status string `json:"status"`
			} `json:"order"`
		} `json:"plan"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil || output.Plan.Applicable || output.Plan.Order.Status != "order_conflict" {
		t.Fatalf("output=%s err=%v", stdout.String(), err)
	}
	stdout.Reset()
	stderr.Reset()
	code = command.Run(context.Background(), []string{"collection", "apply", collectionPath, "--file", recipe, "--home", home, "--json"})
	if code != 1 || !strings.Contains(stderr.String(), "INGOT-COLLECTION-CONFLICT") {
		t.Fatalf("apply exit=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
}

func writeCLICollection(t *testing.T, directory string, modules []string) string {
	t.Helper()
	var data strings.Builder
	data.WriteString("collection_schema=1\nid='example.com/collections/test'\nversion='v1.0.0'\n[metadata]\nname='Test'\n")
	for _, module := range modules {
		data.WriteString("[[plugins]]\nmodule='example.com/plugins/")
		data.WriteString(module)
		data.WriteString("'\nversion='v1.0.0'\n")
	}
	path := filepath.Join(directory, "collection.toml")
	if err := os.WriteFile(path, []byte(data.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
