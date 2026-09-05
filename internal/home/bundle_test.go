package home

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestBundleUpdatePreservesUserFilesAndRefreshesManagedSources(t *testing.T) {
	t.Parallel()
	home := initHome(t)
	distribution := testBundleSource(t)
	if _, err := home.Init(InitOptions{BundlePath: distribution}); err != nil {
		t.Fatal(err)
	}
	desiredBefore, err := os.ReadFile(home.DesiredPath())
	if err != nil {
		t.Fatal(err)
	}
	configBefore, err := os.ReadFile(home.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}

	updatedDistribution := filepath.Join(t.TempDir(), "plugins")
	if err := os.CopyFS(updatedDistribution, os.DirFS(distribution)); err != nil {
		t.Fatal(err)
	}
	changedSource := filepath.Join(updatedDistribution, "tool-edit", "go.mod")
	file, err := os.OpenFile(changedSource, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("\n// bundle update test\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	status, err := home.CheckBundle(context.Background(), updatedDistribution)
	if err != nil {
		t.Fatal(err)
	}
	if !status.UpdateAvailable || status.ManagedPlugins != 12 {
		t.Fatalf("pre-update status = %#v", status)
	}
	result, err := home.UpdateBundle(context.Background(), BundleUpdateOptions{BundlePath: updatedDistribution})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Updated || result.Applied || result.UpdateAvailable || result.Drifted {
		t.Fatalf("update result = %#v", result)
	}

	desiredAfter, err := os.ReadFile(home.DesiredPath())
	if err != nil {
		t.Fatal(err)
	}
	configAfter, err := os.ReadFile(home.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(desiredAfter) != string(desiredBefore) {
		t.Fatal("bundle update rewrote plugins.toml")
	}
	if string(configAfter) != string(configBefore) {
		t.Fatal("bundle update rewrote config.toml")
	}
	managedSource := filepath.Join(home.Root, "bundled-plugins", "tool-edit", "go.mod")
	updatedData, err := os.ReadFile(changedSource)
	if err != nil {
		t.Fatal(err)
	}
	managedData, err := os.ReadFile(managedSource)
	if err != nil {
		t.Fatal(err)
	}
	if string(managedData) != string(updatedData) {
		t.Fatal("managed plugin source was not refreshed")
	}

	second, err := home.UpdateBundle(context.Background(), BundleUpdateOptions{BundlePath: updatedDistribution})
	if err != nil {
		t.Fatal(err)
	}
	if second.Updated || second.UpdateAvailable {
		t.Fatalf("unchanged second update = %#v", second)
	}
}

func TestBundleUpdateRequiresInitializedHome(t *testing.T) {
	t.Parallel()
	home := initHome(t)
	if _, err := home.UpdateBundle(context.Background(), BundleUpdateOptions{BundlePath: testBundleSource(t)}); err == nil {
		t.Fatal("bundle update accepted an uninitialized home")
	}
}

func TestBundleUpdateApplyFailureRestoresPreviousBundle(t *testing.T) {
	t.Parallel()
	home := initHome(t)
	distribution := testBundleSource(t)
	if _, err := home.Init(InitOptions{BundlePath: distribution}); err != nil {
		t.Fatal(err)
	}
	updatedDistribution := filepath.Join(t.TempDir(), "plugins")
	if err := os.CopyFS(updatedDistribution, os.DirFS(distribution)); err != nil {
		t.Fatal(err)
	}
	changedSource := filepath.Join(updatedDistribution, "tool-edit", "go.mod")
	file, err := os.OpenFile(changedSource, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("\n// update that must roll back\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	managedSource := filepath.Join(home.Root, "bundled-plugins", "tool-edit", "go.mod")
	before, err := os.ReadFile(managedSource)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(home.BuilderConfigPath(), []byte("builder_config_version = 1\nunknown = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := home.UpdateBundle(context.Background(), BundleUpdateOptions{BundlePath: updatedDistribution, Apply: true}); err == nil {
		t.Fatal("bundle update --apply succeeded with an invalid builder config")
	}
	after, err := os.ReadFile(managedSource)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("failed bundle update did not restore the previous sources")
	}
	if _, err := os.Stat(home.LockPath()); !os.IsNotExist(err) {
		t.Fatalf("failed bundle update left plugins.lock behind: %v", err)
	}
	status, err := home.CheckBundle(context.Background(), updatedDistribution)
	if err != nil {
		t.Fatal(err)
	}
	if !status.UpdateAvailable || status.Drifted {
		t.Fatalf("restored bundle status = %#v", status)
	}
}
