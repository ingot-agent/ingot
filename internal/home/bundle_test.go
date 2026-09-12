package home

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestBundleUpdateRefreshesManagedSourcesWithoutApply(t *testing.T) {
	home, _ := initHome(t)
	distribution := testBundleSource(t)
	if _, err := home.Init(InitOptions{BundlePath: distribution}); err != nil {
		t.Fatal(err)
	}
	updated := filepath.Join(t.TempDir(), "plugins")
	if err := os.CopyFS(updated, os.DirFS(distribution)); err != nil {
		t.Fatal(err)
	}
	changed := filepath.Join(updated, "tool-shell", "go.mod")
	file, err := os.OpenFile(changed, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString("\n// m2 bundle update\n")
	_ = file.Close()
	result, err := home.UpdateBundle(context.Background(), BundleUpdateOptions{BundlePath: updated})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Updated || result.Applied {
		t.Fatalf("result = %#v", result)
	}
	managed, err := os.ReadFile(filepath.Join(home.Root, "bundled-plugins", "tool-shell", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile(changed)
	if string(managed) != string(want) {
		t.Fatal("managed source was not refreshed")
	}
}

func TestBundleUpdateRejectsApply(t *testing.T) {
	home, _ := initHome(t)
	if _, err := home.Init(InitOptions{BundlePath: testBundleSource(t)}); err != nil {
		t.Fatal(err)
	}
	if _, err := home.UpdateBundle(context.Background(), BundleUpdateOptions{BundlePath: testBundleSource(t), Apply: true}); err == nil {
		t.Fatal("bundle update --apply was accepted")
	}
}
