package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestBundleCheckCommand(t *testing.T) {
	t.Parallel()
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	command := CLI{Stdout: &stdout, Stderr: &stderr}
	exitCode := command.Run(context.Background(), []string{
		"--home", t.TempDir(), "bundle", "check", "--bundle", filepath.Join(repositoryRoot, "plugins"),
	})
	if exitCode != 0 {
		t.Fatalf("bundle check exit = %d, stderr = %s", exitCode, stderr.String())
	}
	var result struct {
		AvailableDigest string `json:"available_digest"`
		UpdateAvailable bool   `json:"update_available"`
		ManagedPlugins  int    `json:"managed_plugins"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("bundle check output is not JSON: %v\n%s", err, stdout.String())
	}
	if result.AvailableDigest == "" || !result.UpdateAvailable || result.ManagedPlugins != 0 {
		t.Fatalf("bundle check result = %#v", result)
	}
}

func TestBundleCheckRejectsApply(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	command := CLI{Stdout: &stdout, Stderr: &stderr}
	if exitCode := command.Run(context.Background(), []string{"--home", t.TempDir(), "bundle", "check", "--apply"}); exitCode != 2 {
		t.Fatalf("bundle check --apply exit = %d, stderr = %s", exitCode, stderr.String())
	}
}
