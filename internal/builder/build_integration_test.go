package builder

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
)

func TestResolveAndBuildRemoteVerticalSlice(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	t.Chdir(t.TempDir())
	proxy := filepath.Join(t.TempDir(), "proxy")
	ingotABISource := filepath.Join(t.TempDir(), "ingot-abi")
	pluginSource := filepath.Join(t.TempDir(), "plugin")
	writeTestIngotABIModule(t, ingotABISource)
	writeTestRemotePlugin(t, pluginSource)
	writeModuleProxyVersion(t, proxy, IngotABIModulePath, IngotABIVersion, ingotABISource)
	writeModuleProxyVersion(t, proxy, "example.com/ingot-test-plugin", "v1.0.0", pluginSource)
	installTomlProxy(t, proxy)
	home := t.TempDir()
	t.Cleanup(func() {
		_ = filepath.Walk(filepath.Join(home, "cache"), func(path string, info os.FileInfo, err error) error {
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
	desiredPath := filepath.Join(home, "plugins.toml")
	writeTestFile(t, desiredPath, `plugins_version = 1

[[plugins]]
module = "example.com/ingot-test-plugin"
version = "v1.0.0"
`)
	writeTestFile(t, filepath.Join(home, "config.toml"), "[plugins.\"example.com/ingot-test-plugin\"]\n")
	t.Setenv("GOSUMDB", "off")
	desired, err := ParseDesired(desiredPath)
	if err != nil {
		t.Fatal(err)
	}
	moduleCache := filepath.Join(home, "cache", "gomod")
	lock, err := Resolve(context.Background(), desired, ResolveOptions{
		Toolchain: runtime.Version(),
		GOPROXY:   "file://" + filepath.ToSlash(proxy), GOMODCACHE: moduleCache,
	})
	if err != nil {
		t.Fatal(err)
	}
	if lock.Runtime.ModulePath != IngotABIModulePath || lock.Runtime.Version != IngotABIVersion || lock.Runtime.Sum == "" {
		t.Fatalf("runtime lock = %#v", lock.Runtime)
	}
	if len(lock.Modules) != 3 {
		t.Fatalf("resolved module graph has %d nodes: %#v", len(lock.Modules), lock.Modules)
	}
	result, err := Build(context.Background(), desired, lock, BuildOptions{Home: home, ConfigPath: filepath.Join(home, "config.toml"), GOMODCACHE: moduleCache})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result.ImageID, "sha256:") || !strings.HasPrefix(result.ArtifactDigest, "sha256:") {
		t.Fatalf("bad build identities: %#v", result)
	}
	if _, err := os.Stat(result.BinaryPath); err != nil {
		t.Fatal(err)
	}
	if result.ComponentCreationOrder[0] != "example.com/ingot-test-plugin/default" {
		t.Fatalf("creation order = %#v", result.ComponentCreationOrder)
	}
	second, err := Build(context.Background(), desired, lock, BuildOptions{Home: home, ConfigPath: filepath.Join(home, "config.toml"), GOMODCACHE: moduleCache})
	if err != nil {
		t.Fatal(err)
	}
	if second.ImageID != result.ImageID || second.ArtifactDigest != result.ArtifactDigest {
		t.Fatal("identical locked build did not reuse the immutable image")
	}
	writeTestFile(t, filepath.Join(home, "config.toml"), "fail\n")
	if _, err := Build(context.Background(), desired, lock, BuildOptions{Home: home, ConfigPath: filepath.Join(home, "config.toml"), GOMODCACHE: moduleCache}); err == nil || !strings.Contains(err.Error(), "INGOT-BUILD-CHECK") {
		t.Fatalf("pre-switch check failure = %v", err)
	}
	manifest, _ := lock.CanonicalBuildManifest()
	if _, err := VerifyImage(result.ImageDirectory, result.ImageID, manifest); err != nil {
		t.Fatalf("failed rebuild damaged prior image: %v", err)
	}
}

func TestResolveAndBuildLocalDevVerticalSlice(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	t.Chdir(t.TempDir())
	proxy := filepath.Join(t.TempDir(), "proxy")
	ingotABISource := filepath.Join(t.TempDir(), "ingot-abi")
	pluginSource := filepath.Join(t.TempDir(), "local plugin")
	writeTestIngotABIModule(t, ingotABISource)
	writeTestRemotePlugin(t, pluginSource)
	writeModuleProxyVersion(t, proxy, IngotABIModulePath, IngotABIVersion, ingotABISource)
	installTomlProxy(t, proxy)
	home := t.TempDir()
	makeModuleCacheRemovable(t, home)
	desiredPath := filepath.Join(home, "plugins.toml")
	writeTestFile(t, desiredPath, fmt.Sprintf("plugins_version=1\n[[plugins]]\nmodule=%q\npath=%q\n", "example.com/ingot-test-plugin", filepath.ToSlash(pluginSource)))
	configPath := filepath.Join(home, "config.toml")
	writeTestFile(t, configPath, "[plugins.\"example.com/ingot-test-plugin\"]\n")
	t.Setenv("GOSUMDB", "off")
	desired, err := ParseDesired(desiredPath)
	if err != nil {
		t.Fatal(err)
	}
	moduleCache := filepath.Join(home, "cache", "gomod")
	lock, err := Resolve(context.Background(), desired, ResolveOptions{Toolchain: runtime.Version(), GOPROXY: "file://" + filepath.ToSlash(proxy), GOMODCACHE: moduleCache})
	if err != nil {
		t.Fatal(err)
	}
	if lock.Plugins[0].SourceKind != "dev" || len(lock.Replacements) != 1 || lock.Runtime.Sum == "" {
		t.Fatalf("local lock materialization = %#v / %#v / %#v", lock.Plugins[0], lock.Replacements, lock.Runtime)
	}
	result, err := Build(context.Background(), desired, lock, BuildOptions{Home: home, ConfigPath: configPath, GOMODCACHE: moduleCache})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(result.BinaryPath); err != nil {
		t.Fatal(err)
	}
	pluginDriftPath := filepath.Join(pluginSource, "changed.txt")
	writeTestFile(t, pluginDriftPath, "drift")
	_, err = Build(context.Background(), desired, lock, BuildOptions{Home: home, ConfigPath: configPath, GOMODCACHE: moduleCache})
	if err == nil || !strings.Contains(err.Error(), "INGOT-BUILD-DEV-DIGEST") {
		t.Fatalf("source drift error = %v", err)
	}
}

func TestResolveAndBuildWithDomainContractModules(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	t.Chdir(t.TempDir())
	proxy := filepath.Join(t.TempDir(), "proxy")
	ingotABISource := filepath.Join(t.TempDir(), "ingot-abi")
	secondarySDKSource := filepath.Join(t.TempDir(), "secondary-sdk")
	providerSource := filepath.Join(t.TempDir(), "provider")
	consumerSource := filepath.Join(t.TempDir(), "consumer")
	writeTestIngotABIModule(t, ingotABISource)
	writeModuleProxyVersion(t, proxy, IngotABIModulePath, IngotABIVersion, ingotABISource)
	installTomlProxy(t, proxy)

	// A domain contract module is an ordinary Go module imported by
	// plugin go.mod files. It needs no Builder configuration.
	writeTestFile(t, filepath.Join(secondarySDKSource, "go.mod"), "module example.com/secondary-sdk\n\ngo 1.24.0\n")
	writeTestFile(t, filepath.Join(secondarySDKSource, "capability", "capability.go"), "package capability\ntype Value struct{ Name string }\n")
	writeModuleProxyVersion(t, proxy, "example.com/secondary-sdk", "v0.1.0", secondarySDKSource)

	writeTestFile(t, filepath.Join(providerSource, "go.mod"), `module example.com/provider

go 1.24.0

require (
	github.com/ingot-agent/ingot-abi v0.1.0
	example.com/secondary-sdk v0.1.0
)
`)
	writeTestFile(t, filepath.Join(providerSource, "ingot.plugin.toml"), `manifest_version=1
name="provider"
ingot=">=0.3.0 <0.4.0"
config_package="."
[[components]]
name="default"
package="."
`)
	writeTestFile(t, filepath.Join(providerSource, "component.go"), `package provider
import (
	"context"
	ingotabi "github.com/ingot-agent/ingot-abi"
	"example.com/secondary-sdk/capability"
)
type Config struct{}
type Dependencies struct{}
type Exports struct { Value capability.Value }
func New(context.Context, Config, Dependencies) (Exports, ingotabi.Cleanup, error) {
	return Exports{Value: capability.Value{Name: "provided"}}, nil, nil
}
`)

	writeTestFile(t, filepath.Join(consumerSource, "go.mod"), `module example.com/consumer

go 1.24.0

require (
	github.com/ingot-agent/ingot-abi v0.1.0
	example.com/secondary-sdk v0.1.0
)
`)
	writeTestFile(t, filepath.Join(consumerSource, "ingot.plugin.toml"), `manifest_version=1
name="consumer"
ingot=">=0.3.0 <0.4.0"
config_package="."
[[components]]
name="default"
package="."
`)
	writeTestFile(t, filepath.Join(consumerSource, "component.go"), `package consumer
import (
	"context"
	ingotabi "github.com/ingot-agent/ingot-abi"
	"example.com/secondary-sdk/capability"
)
type Config struct{}
type Dependencies struct { Value ingotabi.Optional[capability.Value] }
type Exports struct{}
func New(context.Context, Config, Dependencies) (Exports, ingotabi.Cleanup, error) {
	return Exports{}, nil, nil
}
`)

	home := t.TempDir()
	makeModuleCacheRemovable(t, home)
	desiredPath := filepath.Join(home, "plugins.toml")
	writeTestFile(t, desiredPath, fmt.Sprintf(`plugins_version=1
[[plugins]]
module="example.com/provider"
path=%q
[[plugins]]
module="example.com/consumer"
path=%q
`, filepath.ToSlash(providerSource), filepath.ToSlash(consumerSource)))
	configPath := filepath.Join(home, "config.toml")
	writeTestFile(t, configPath, "[plugins.\"example.com/provider\"]\n[plugins.\"example.com/consumer\"]\n")
	desired, err := ParseDesired(desiredPath)
	if err != nil {
		t.Fatal(err)
	}
	moduleCache := filepath.Join(home, "cache", "gomod")
	t.Setenv("GOSUMDB", "off")
	lock, err := Resolve(context.Background(), desired, ResolveOptions{Toolchain: runtime.Version(), GOPROXY: "file://" + filepath.ToSlash(proxy), GOMODCACHE: moduleCache})
	if err != nil {
		t.Fatal(err)
	}
	if lock.Runtime.ModulePath != IngotABIModulePath {
		t.Fatalf("runtime = %#v", lock.Runtime)
	}
	if len(lock.Replacements) != 2 {
		t.Fatalf("replacements = %#v", lock.Replacements)
	}
	result, err := Build(context.Background(), desired, lock, BuildOptions{Home: home, ConfigPath: configPath, GOMODCACHE: moduleCache})
	if err != nil {
		t.Fatal(err)
	}
	wantOrder := []string{"example.com/provider/default", "example.com/consumer/default"}
	if fmt.Sprint(result.ComponentCreationOrder) != fmt.Sprint(wantOrder) {
		t.Fatalf("creation order = %#v, want %#v", result.ComponentCreationOrder, wantOrder)
	}
}

func TestResolveRejectsMVSUpgradedIngotABI(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	t.Chdir(t.TempDir())
	proxy := filepath.Join(t.TempDir(), "proxy")
	oldSDKSource := filepath.Join(t.TempDir(), "old-ingot-abi")
	newSDKSource := filepath.Join(t.TempDir(), "new-ingot-abi")
	pluginSource := filepath.Join(t.TempDir(), "plugin")
	writeTestIngotABIModule(t, oldSDKSource)
	writeTestIngotABIModuleNamed(t, newSDKSource, "v0.1.1", "ingot-abi-new")
	writeModuleProxyVersion(t, proxy, IngotABIModulePath, IngotABIVersion, oldSDKSource)
	writeModuleProxyVersion(t, proxy, IngotABIModulePath, "v0.1.1", newSDKSource)
	installTomlProxy(t, proxy)

	writeTestFile(t, filepath.Join(pluginSource, "go.mod"), `module example.com/ingot-test-plugin

go 1.24.0

require github.com/ingot-agent/ingot-abi v0.1.1
`)
	writeTestFile(t, filepath.Join(pluginSource, "ingot.plugin.toml"), `manifest_version = 1
name = "test-plugin"
ingot = ">=0.3.0 <0.4.0"
config_package = "."
[[components]]
name = "default"
package = "."
`)
	writeTestFile(t, filepath.Join(pluginSource, "component.go"), `package testplugin
import (
	"context"
	ingotabi "github.com/ingot-agent/ingot-abi"
)
type Config struct{}
type Dependencies struct{}
type Exports struct{}
func New(context.Context, Config, Dependencies) (Exports, ingotabi.Cleanup, error) { return Exports{}, nil, nil }
`)
	writeModuleProxyVersion(t, proxy, "example.com/ingot-test-plugin", "v1.0.0", pluginSource)
	home := t.TempDir()
	makeModuleCacheRemovable(t, home)
	desiredPath := filepath.Join(home, "plugins.toml")
	writeTestFile(t, desiredPath, `plugins_version=1
[[plugins]]
module="example.com/ingot-test-plugin"
version="v1.0.0"
`)
	desired, err := ParseDesired(desiredPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOSUMDB", "off")
	_, err = Resolve(context.Background(), desired, ResolveOptions{Toolchain: runtime.Version(), GOPROXY: "file://" + filepath.ToSlash(proxy), GOMODCACHE: filepath.Join(home, "cache", "gomod")})
	if err == nil || !strings.Contains(err.Error(), "INGOT-RESOLVE-RUNTIME-VERSION") {
		t.Fatalf("MVS upgraded ingot ABI error = %v", err)
	}
}

func TestResolveMaterializesPrunedTransitiveGraph(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	t.Chdir(t.TempDir())
	proxy := filepath.Join(t.TempDir(), "proxy")
	ingotABISource := filepath.Join(t.TempDir(), "ingot-abi")
	depOneSource := filepath.Join(t.TempDir(), "dep-one")
	depTwoSource := filepath.Join(t.TempDir(), "dep-two")
	pluginSource := filepath.Join(t.TempDir(), "plugin")

	// dep-two is a dependency of dep-one, which is only a transitive
	// requirement of the ingot ABI. Under Go 1.17+ pruned module graphs
	// its go.mod edge is invisible until the staged root explicitly requires
	// dep-one.
	writeTestFile(t, filepath.Join(depTwoSource, "go.mod"), "module example.com/ingot-dep-two\n\ngo 1.24.0\n")
	writeTestFile(t, filepath.Join(depTwoSource, "dep.go"), "package deptwo\n")
	writeModuleProxyVersion(t, proxy, "example.com/ingot-dep-two", "v1.0.0", depTwoSource)

	writeTestFile(t, filepath.Join(depOneSource, "go.mod"), "module example.com/ingot-dep-one\n\ngo 1.24.0\n\nrequire example.com/ingot-dep-two v1.0.0\n")
	writeTestFile(t, filepath.Join(depOneSource, "dep.go"), "package depone\n")
	writeModuleProxyVersion(t, proxy, "example.com/ingot-dep-one", "v1.0.0", depOneSource)

	writeTestIngotABIModule(t, ingotABISource)
	writeTestFile(t, filepath.Join(ingotABISource, "go.mod"), "module "+IngotABIModulePath+"\n\ngo 1.24.0\n\nrequire example.com/ingot-dep-one v1.0.0\n")
	writeModuleProxyVersion(t, proxy, IngotABIModulePath, IngotABIVersion, ingotABISource)
	installTomlProxy(t, proxy)

	writeTestRemotePlugin(t, pluginSource)
	writeModuleProxyVersion(t, proxy, "example.com/ingot-test-plugin", "v1.0.0", pluginSource)

	home := t.TempDir()
	makeModuleCacheRemovable(t, home)
	desiredPath := filepath.Join(home, "plugins.toml")
	writeTestFile(t, desiredPath, "plugins_version = 1\n\n[[plugins]]\nmodule = \"example.com/ingot-test-plugin\"\nversion = \"v1.0.0\"\n")
	writeTestFile(t, filepath.Join(home, "config.toml"), "[plugins.\"example.com/ingot-test-plugin\"]\n")
	t.Setenv("GOSUMDB", "off")
	desired, err := ParseDesired(desiredPath)
	if err != nil {
		t.Fatal(err)
	}
	moduleCache := filepath.Join(home, "cache", "gomod")
	lock, err := Resolve(context.Background(), desired, ResolveOptions{Toolchain: runtime.Version(), GOPROXY: "file://" + filepath.ToSlash(proxy), GOMODCACHE: moduleCache})
	if err != nil {
		t.Fatal(err)
	}
	selected := map[string]string{}
	for _, item := range lock.Modules {
		selected[item.Path] = item.Version
	}
	if selected["example.com/ingot-dep-one"] != "v1.0.0" {
		t.Fatalf("direct ingot ABI dependency missing from lock: %#v", selected)
	}
	if selected["example.com/ingot-dep-two"] != "v1.0.0" {
		t.Fatalf("pruned transitive dependency missing from lock: %#v", selected)
	}
	// The offline build must succeed with no network: every module of the
	// committed graph is already in the module cache.
	if _, err := Build(context.Background(), desired, lock, BuildOptions{Home: home, ConfigPath: filepath.Join(home, "config.toml"), GOMODCACHE: moduleCache}); err != nil {
		t.Fatal(err)
	}
}

// TestDevSourceLocationDoesNotAffectArtifact verifies that compiling the
// same dev plugin content from two different directories yields the exact
// same artifact (no machine-specific source paths may leak into the binary),
// and that rebuilding the same ImageID therefore succeeds instead of failing
// with INGOT-BUILD-REPRODUCIBILITY.
func TestDevSourceLocationDoesNotAffectArtifact(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	t.Chdir(t.TempDir())
	proxy := filepath.Join(t.TempDir(), "proxy")
	ingotABISource := filepath.Join(t.TempDir(), "ingot-abi")
	writeTestIngotABIModule(t, ingotABISource)
	writeModuleProxyVersion(t, proxy, IngotABIModulePath, IngotABIVersion, ingotABISource)
	installTomlProxy(t, proxy)

	pluginA := filepath.Join(t.TempDir(), "plugin a")
	pluginB := filepath.Join(t.TempDir(), "plugin b")
	writeTestRemotePlugin(t, pluginA)
	writeTestRemotePlugin(t, pluginB)

	build := func(pluginSource string) *BuildResult {
		home := t.TempDir()
		makeModuleCacheRemovable(t, home)
		desiredPath := filepath.Join(home, "plugins.toml")
		writeTestFile(t, desiredPath, fmt.Sprintf("plugins_version=1\n[[plugins]]\nmodule=%q\npath=%q\n", "example.com/ingot-test-plugin", filepath.ToSlash(pluginSource)))
		configPath := filepath.Join(home, "config.toml")
		writeTestFile(t, configPath, "[plugins.\"example.com/ingot-test-plugin\"]\n")
		t.Setenv("GOSUMDB", "off")
		desired, err := ParseDesired(desiredPath)
		if err != nil {
			t.Fatal(err)
		}
		moduleCache := filepath.Join(home, "cache", "gomod")
		lock, err := Resolve(context.Background(), desired, ResolveOptions{Toolchain: runtime.Version(), GOPROXY: "file://" + filepath.ToSlash(proxy), GOMODCACHE: moduleCache})
		if err != nil {
			t.Fatal(err)
		}
		result, err := Build(context.Background(), desired, lock, BuildOptions{Home: home, ConfigPath: configPath, GOMODCACHE: moduleCache})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}

	first := build(pluginA)
	second := build(pluginB)
	if first.ImageID != second.ImageID {
		t.Fatalf("identical dev content must lock the same ImageID: %s != %s", first.ImageID, second.ImageID)
	}
	if first.ArtifactDigest != second.ArtifactDigest {
		t.Fatalf("identical dev content from different directories must produce one artifact: %s != %s", first.ArtifactDigest, second.ArtifactDigest)
	}
}

func makeModuleCacheRemovable(t *testing.T, home string) {
	t.Helper()
	t.Cleanup(func() {
		_ = filepath.Walk(filepath.Join(home, "cache"), func(path string, info os.FileInfo, err error) error {
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
}

func writeTestIngotABIModule(t *testing.T, root string) {
	t.Helper()
	writeTestIngotABIModuleNamed(t, root, IngotABIVersion, "ingotabi")
}

func writeTestIngotABIModuleNamed(t *testing.T, root, version, packageName string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module "+IngotABIModulePath+"\n\ngo 1.24.0\n")
	writeTestFile(t, filepath.Join(root, "ingotabi.go"), `package `+packageName+`
import "context"
type Cleanup func(context.Context) error
type Optional[T any] struct { Value T; Valid bool }
func None[T any]() Optional[T] { return Optional[T]{} }
func Some[T any](value T) Optional[T] { return Optional[T]{Value:value, Valid:true} }
type Named[T any] struct { Name string; Value T }
func CheckUniqueNames[T any](items []Named[T]) error { return nil }
`)
	writeTestFile(t, filepath.Join(root, "invocation", "invocation.go"), `package invocation
type Mode uint8
const (
	ModeRun Mode = iota + 1
	ModeCheck
)
type Invocation interface { Arguments() []string; Mode() Mode }
`)
	writeTestFile(t, filepath.Join(root, "lifecycle", "lifecycle.go"), `package lifecycle
type Controller interface { RequestShutdown(error) }
`)
	writeTestFile(t, filepath.Join(root, "state", "state.go"), `package state
type Scope interface { Dir() string }
`)
}

func writeTestRemotePlugin(t *testing.T, root string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/ingot-test-plugin\n\ngo 1.24.0\n\nrequire github.com/ingot-agent/ingot-abi v0.1.0\n")
	writeTestFile(t, filepath.Join(root, "ingot.plugin.toml"), `manifest_version = 1
name = "test-plugin"
ingot = ">=0.3.0 <0.4.0"
config_package = "."
[[components]]
name = "default"
package = "."
`)
	writeTestFile(t, filepath.Join(root, "component.go"), `package testplugin
import (
	"context"
	ingotabi "github.com/ingot-agent/ingot-abi"
)
type Config struct{}
type Dependencies struct{}
type Exports struct{}
func New(context.Context, Config, Dependencies) (Exports, ingotabi.Cleanup, error) { return Exports{}, nil, nil }
`)
}

// installTomlProxy publishes the pinned TOML decoder module into the test
// proxy from the local Go module cache so the generated config support can
// be resolved and built offline.
func installTomlProxy(t *testing.T, proxy string) {
	t.Helper()
	moduleCache := os.Getenv("GOMODCACHE")
	if moduleCache == "" {
		userHome, _ := os.UserHomeDir()
		goPath := os.Getenv("GOPATH")
		if goPath == "" {
			goPath = filepath.Join(userHome, "go")
		}
		moduleCache = filepath.Join(strings.Split(goPath, string(os.PathListSeparator))[0], "pkg", "mod")
	}
	source := filepath.Join(moduleCache, "github.com", "pelletier", "go-toml", "v2@v2.2.4")
	if _, err := os.Stat(source); err != nil {
		t.Skipf("TOML module %s not found in module cache: %v", source, err)
	}
	writeModuleProxyVersion(t, proxy, RuntimeSupportTOMLModule, RuntimeSupportTOMLVersion, source)
}

func writeModuleProxyVersion(t *testing.T, proxy, modulePath, version, sourceRoot string) {
	t.Helper()
	directory := filepath.Join(proxy, filepath.FromSlash(modulePath), "@v")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	goMod, err := os.ReadFile(filepath.Join(sourceRoot, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(directory, "list"), version+"\n")
	writeTestFile(t, filepath.Join(directory, version+".info"), fmt.Sprintf("{\"Version\":%q,\"Time\":\"2026-08-22T00:00:00Z\"}\n", version))
	if err := os.WriteFile(filepath.Join(directory, version+".mod"), goMod, 0o644); err != nil {
		t.Fatal(err)
	}
	archive, err := os.Create(filepath.Join(directory, version+".zip"))
	if err != nil {
		t.Fatal(err)
	}
	zipWriter := zip.NewWriter(archive)
	prefix := modulePath + "@" + version + "/"
	err = filepath.WalkDir(sourceRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return err
		}
		header, err := zip.FileInfoHeader(mustInfo(entry))
		if err != nil {
			return err
		}
		header.Name = prefix + filepath.ToSlash(relative)
		header.Method = zip.Deflate
		writer, err := zipWriter.CreateHeader(header)
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
	if err == nil {
		err = zipWriter.Close()
	} else {
		_ = zipWriter.Close()
	}
	if closeErr := archive.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
}

func mustInfo(entry os.DirEntry) os.FileInfo {
	info, err := entry.Info()
	if err != nil {
		panic(err)
	}
	return info
}
