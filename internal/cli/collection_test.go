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
	t.Parallel()
	directory := t.TempDir()
	collectionPath := writeCLICollection(t, directory, []string{"a"})
	var stdout, stderr bytes.Buffer
	command := CLI{Stdout: &stdout, Stderr: &stderr}
	code := command.Run(context.Background(), []string{"--home", filepath.Join(directory, "missing-home"), "collection", "inspect", collectionPath})
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	var output struct {
		Digest string `json:"digest"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil || !strings.HasPrefix(output.Digest, "sha256:") {
		t.Fatalf("output=%s err=%v", stdout.String(), err)
	}
}

func TestCollectionPlanConflictIsMachineReadableAndApplyFails(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "plugins.toml"), []byte("plugins_version=1\n[[plugins]]\nmodule='example.com/plugins/a'\nversion='v1.0.0'\n[[plugins]]\nmodule='example.com/plugins/b'\nversion='v1.0.0'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	collectionPath := writeCLICollection(t, project, []string{"b", "a"})
	initializer := CLI{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	if code := initializer.Run(context.Background(), []string{"--home", home, "init", "--profile", "minimal"}); code != 0 {
		t.Fatalf("init exit=%d", code)
	}

	var stdout, stderr bytes.Buffer
	command := CLI{Stdout: &stdout, Stderr: &stderr}
	code := command.Run(context.Background(), []string{"--home", home, "collection", "plan", "--use", filepath.Join(project, "plugins.toml"), collectionPath})
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
		t.Fatalf("plan output=%s err=%v", stdout.String(), err)
	}
	stdout.Reset()
	stderr.Reset()
	code = command.Run(context.Background(), []string{"--home", home, "collection", "apply", "--use", filepath.Join(project, "plugins.toml"), collectionPath})
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
