package home

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ingot-agent/ingot/internal/builder"
	"github.com/ingot-agent/ingot/internal/collection"
)

func TestCollectionApplyNoopAndConflictDoNotWrite(t *testing.T) {
	t.Parallel()
	project := t.TempDir()
	recipe := filepath.Join(project, "plugins.toml")
	original := "# keep\nplugins_version=1\n[[plugins]]\nmodule='example.com/plugins/a'\nversion='v1.0.0'\n[[plugins]]\nmodule='example.com/plugins/b'\nversion='v1.0.0'\n"
	if err := os.WriteFile(recipe, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	home := &Home{Root: t.TempDir()}
	options := RecipeOptions{Use: recipe}
	noop := testLoadedCollection("a", "b")
	result, err := home.ApplyCollection(context.Background(), options, noop, collection.PlanOptions{}, builder.ResolveOptions{})
	if err != nil || result.Plan == nil || result.Plan.Changed {
		t.Fatalf("noop result=%#v err=%v", result, err)
	}
	assertFileContent(t, recipe, original)
	if _, err := os.Stat(filepath.Join(project, "plugins.lock")); !os.IsNotExist(err) {
		t.Fatalf("noop created lock: %v", err)
	}

	conflicting := testLoadedCollection("b", "a")
	result, err = home.ApplyCollection(context.Background(), options, conflicting, collection.PlanOptions{}, builder.ResolveOptions{})
	if err == nil || result.Plan == nil || result.Plan.Applicable {
		t.Fatalf("conflict result=%#v err=%v", result, err)
	}
	if _, ok := err.(*CollectionConflictError); !ok {
		t.Fatalf("conflict type = %T", err)
	}
	assertFileContent(t, recipe, original)
}

func TestCollectionApplyResolveFailureLeavesProjectUntouched(t *testing.T) {
	t.Parallel()
	project := t.TempDir()
	recipe := filepath.Join(project, "plugins.toml")
	lockPath := filepath.Join(project, "plugins.lock")
	originalRecipe := "plugins_version=1\n[[plugins]]\nmodule='example.com/plugins/a'\nversion='v1.0.0'\n"
	originalLock := "not a lock\n"
	if err := os.WriteFile(recipe, []byte(originalRecipe), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, []byte(originalLock), 0o644); err != nil {
		t.Fatal(err)
	}
	home := &Home{Root: t.TempDir()}
	loaded := testLoadedCollection("a", "b")
	_, err := home.ApplyCollection(context.Background(), RecipeOptions{Use: recipe, Lock: lockPath}, loaded, collection.PlanOptions{}, builder.ResolveOptions{GOPROXY: "off", GOMODCACHE: filepath.Join(t.TempDir(), "gomod")})
	if err == nil {
		t.Fatal("resolve unexpectedly succeeded")
	}
	assertFileContent(t, recipe, originalRecipe)
	assertFileContent(t, lockPath, originalLock)
}

func TestCollectionApplyResolvesAndCommitsPair(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	project := t.TempDir()
	homeRoot := t.TempDir()
	t.Cleanup(func() {
		_ = filepath.Walk(filepath.Join(homeRoot, "cache"), func(path string, info os.FileInfo, err error) error {
			if err == nil && info != nil {
				if info.IsDir() {
					_ = os.Chmod(path, 0o700)
				} else {
					_ = os.Chmod(path, 0o600)
				}
			}
			return nil
		})
	})
	proxy := filepath.Join(t.TempDir(), "proxy")
	writeCollectionProxyModule(t, proxy, builder.IngotABIModulePath, builder.IngotABIVersion, testABISource())
	writeCollectionProxyModule(t, proxy, builder.RuntimeSupportTOMLModule, builder.RuntimeSupportTOMLVersion, "package toml\n")
	writeCollectionProxyModule(t, proxy, "example.com/plugins/a", "v1.0.0", testPluginSource("a"))
	writeCollectionProxyModule(t, proxy, "example.com/plugins/b", "v1.0.0", testPluginSource("b"))
	recipe := filepath.Join(project, "plugins.toml")
	original := "# project note\nplugins_version=1\n\n[[plugins]]\n# plugin a\nmodule='example.com/plugins/a'\nversion='v1.0.0'\n"
	if err := os.WriteFile(recipe, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOSUMDB", "off")
	home := &Home{Root: homeRoot}
	loaded := testLoadedCollection("a", "b")
	result, err := home.ApplyCollection(context.Background(), RecipeOptions{Use: recipe}, loaded, collection.PlanOptions{}, builder.ResolveOptions{
		Toolchain: runtime.Version(), GOPROXY: "file://" + filepath.ToSlash(proxy), GOMODCACHE: filepath.Join(homeRoot, "cache", "gomod"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Plan.Changed || result.ImageID == "" {
		t.Fatalf("result = %#v", result)
	}
	data, err := os.ReadFile(recipe)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "# project note") || !strings.Contains(string(data), "# plugin a") {
		t.Fatalf("comments not preserved:\n%s", data)
	}
	desired, err := builder.ParseDesired(recipe)
	if err != nil {
		t.Fatal(err)
	}
	if len(desired.Plugins) != 2 || desired.Plugins[1].Module != "example.com/plugins/b" {
		t.Fatalf("desired = %#v", desired.Plugins)
	}
	lock, err := builder.ParseLock(filepath.Join(project, "plugins.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if lock.PluginsDigest != result.PluginsDigest || lock.PluginsDigest != result.Plan.CandidateDigest {
		t.Fatalf("digests result=%s plan=%s lock=%s", result.PluginsDigest, result.Plan.CandidateDigest, lock.PluginsDigest)
	}

	reordered := testLoadedCollection("b", "a")
	reorderResult, err := home.ApplyCollection(context.Background(), RecipeOptions{Use: recipe}, reordered, collection.PlanOptions{AcceptOrder: true}, builder.ResolveOptions{
		Toolchain: runtime.Version(), GOPROXY: "file://" + filepath.ToSlash(proxy), GOMODCACHE: filepath.Join(homeRoot, "cache", "gomod"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if reorderResult.Plan.Order.Status != "order_conflict" || !reorderResult.Plan.Order.Accepted || reorderResult.Plan.Order.ReversalCount != 1 {
		t.Fatalf("reorder plan = %#v", reorderResult.Plan.Order)
	}
	reorderedDesired, err := builder.ParseDesired(recipe)
	if err != nil {
		t.Fatal(err)
	}
	if reorderedDesired.Plugins[0].Module != "example.com/plugins/b" || reorderedDesired.Plugins[1].Module != "example.com/plugins/a" {
		t.Fatalf("reordered desired = %#v", reorderedDesired.Plugins)
	}
}

func testLoadedCollection(names ...string) collection.Loaded {
	plugins := make([]collection.Plugin, len(names))
	for index, name := range names {
		plugins[index] = collection.Plugin{Module: "example.com/plugins/" + name, Version: "v1.0.0"}
	}
	value := collection.Collection{CollectionSchema: 1, ID: "example.com/collections/test", Version: "v1.0.0", Metadata: collection.Metadata{Name: "Test"}, Plugins: plugins}
	digest, _ := value.Digest()
	return collection.Loaded{Source: "test.toml", Digest: digest, Collection: value}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("%s changed:\n%s", path, data)
	}
}

func testABISource() string {
	return `package ingotabi
import "context"
type Cleanup func(context.Context) error
type Optional[T any] struct { Value T; Valid bool }
type Named[T any] struct { Name string; Value T }
`
}

func testPluginSource(name string) string {
	return fmt.Sprintf(`package plugin
import (
    "context"
    ingotabi "github.com/ingot-agent/ingot-abi"
)
type Dependencies struct{}
type Exports struct{}
func New(context.Context, Dependencies) (Exports, ingotabi.Cleanup, error) { return Exports{}, nil, nil }
// %s
`, name)
}

func writeCollectionProxyModule(t *testing.T, proxy, modulePath, version, source string) {
	t.Helper()
	sourceRoot := t.TempDir()
	goMod := "module " + modulePath + "\n\ngo 1.24.0\n"
	if strings.HasPrefix(modulePath, "example.com/plugins/") {
		goMod += "\nrequire " + builder.IngotABIModulePath + " " + builder.IngotABIVersion + "\n"
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "source.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(modulePath, "example.com/plugins/") {
		manifest := fmt.Sprintf("manifest_version=1\nname=%q\ningot='>=0.3.0 <0.4.0'\nconfig_package='.'\n[[components]]\nname='default'\npackage='.'\n", filepath.Base(modulePath))
		if err := os.WriteFile(filepath.Join(sourceRoot, "ingot.plugin.toml"), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	directory := filepath.Join(proxy, filepath.FromSlash(modulePath), "@v")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "list"), []byte(version+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, version+".info"), []byte(fmt.Sprintf("{\"Version\":%q,\"Time\":\"2026-09-14T00:00:00Z\"}\n", version)), 0o644); err != nil {
		t.Fatal(err)
	}
	goModData, _ := os.ReadFile(filepath.Join(sourceRoot, "go.mod"))
	if err := os.WriteFile(filepath.Join(directory, version+".mod"), goModData, 0o644); err != nil {
		t.Fatal(err)
	}
	archive, err := os.Create(filepath.Join(directory, version+".zip"))
	if err != nil {
		t.Fatal(err)
	}
	zipWriter := zip.NewWriter(archive)
	err = filepath.WalkDir(sourceRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		relative, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return err
		}
		writer, err := zipWriter.Create(modulePath + "@" + version + "/" + filepath.ToSlash(relative))
		if err != nil {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(writer, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if closeErr := zipWriter.Close(); err == nil {
		err = closeErr
	}
	if closeErr := archive.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
}
