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
	proxy := filepath.Join(t.TempDir(), "proxy")
	sdkSource := filepath.Join(t.TempDir(), "sdk")
	pluginSource := filepath.Join(t.TempDir(), "plugin")
	writeTestSDKModule(t, sdkSource)
	writeTestRemotePlugin(t, pluginSource)
	writeModuleProxyVersion(t, proxy, "example.com/ingot-test-sdk", "v0.1.0", sdkSource)
	writeModuleProxyVersion(t, proxy, "example.com/ingot-test-plugin", "v1.0.0", pluginSource)
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
	writeTestFile(t, filepath.Join(home, "config.toml"), "# the test SDK config decoder accepts the empty document\n")
	t.Setenv("GOSUMDB", "off")
	desired, err := ParseDesired(desiredPath)
	if err != nil {
		t.Fatal(err)
	}
	moduleCache := filepath.Join(home, "cache", "gomod")
	lock, err := Resolve(context.Background(), desired, ResolveOptions{
		SDKModule: "example.com/ingot-test-sdk", SDKVersion: "v0.1.0", Toolchain: runtime.Version(),
		GOPROXY: "file://" + filepath.ToSlash(proxy), GOMODCACHE: moduleCache,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(lock.Modules) != 2 {
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
	proxy := filepath.Join(t.TempDir(), "proxy")
	sdkSource := filepath.Join(t.TempDir(), "sdk")
	pluginSource := filepath.Join(t.TempDir(), "local plugin")
	writeTestSDKModule(t, sdkSource)
	writeTestRemotePlugin(t, pluginSource)
	writeModuleProxyVersion(t, proxy, "example.com/ingot-test-sdk", "v0.1.0", sdkSource)
	home := t.TempDir()
	makeModuleCacheRemovable(t, home)
	desiredPath := filepath.Join(home, "plugins.toml")
	writeTestFile(t, desiredPath, fmt.Sprintf("plugins_version=1\n[[plugins]]\nmodule=%q\npath=%q\n", "example.com/ingot-test-plugin", filepath.ToSlash(pluginSource)))
	configPath := filepath.Join(home, "config.toml")
	writeTestFile(t, configPath, "")
	t.Setenv("GOSUMDB", "off")
	desired, err := ParseDesired(desiredPath)
	if err != nil {
		t.Fatal(err)
	}
	moduleCache := filepath.Join(home, "cache", "gomod")
	lock, err := Resolve(context.Background(), desired, ResolveOptions{SDKModule: "example.com/ingot-test-sdk", SDKVersion: "v0.1.0", SDKPath: sdkSource, Toolchain: runtime.Version(), GOPROXY: "file://" + filepath.ToSlash(proxy), GOMODCACHE: moduleCache})
	if err != nil {
		t.Fatal(err)
	}
	if lock.Plugins[0].SourceKind != "dev" || len(lock.Replacements) != 2 || lock.SDK.Version != "v0.1.0" {
		t.Fatalf("local lock materialization = %#v / %#v", lock.Plugins[0], lock.Replacements)
	}
	result, err := Build(context.Background(), desired, lock, BuildOptions{Home: home, ConfigPath: configPath, GOMODCACHE: moduleCache})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(result.BinaryPath); err != nil {
		t.Fatal(err)
	}
	sdkDriftPath := filepath.Join(sdkSource, "changed.txt")
	writeTestFile(t, sdkDriftPath, "drift")
	_, err = Build(context.Background(), desired, lock, BuildOptions{Home: home, ConfigPath: configPath, GOMODCACHE: moduleCache})
	if err == nil || !strings.Contains(err.Error(), "INGOT-BUILD-DEV-DIGEST") || !strings.Contains(err.Error(), "example.com/ingot-test-sdk") {
		t.Fatalf("SDK source drift error = %v", err)
	}
	if err := os.Remove(sdkDriftPath); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(pluginSource, "changed.txt"), "drift")
	_, err = Build(context.Background(), desired, lock, BuildOptions{Home: home, ConfigPath: configPath, GOMODCACHE: moduleCache})
	if err == nil || !strings.Contains(err.Error(), "INGOT-BUILD-DEV-DIGEST") {
		t.Fatalf("source drift error = %v", err)
	}
}

func TestResolveMaterializesPrunedTransitiveGraph(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	proxy := filepath.Join(t.TempDir(), "proxy")
	sdkSource := filepath.Join(t.TempDir(), "sdk")
	depOneSource := filepath.Join(t.TempDir(), "dep-one")
	depTwoSource := filepath.Join(t.TempDir(), "dep-two")
	pluginSource := filepath.Join(t.TempDir(), "plugin")

	// dep-two is a dependency of dep-one, which is only a transitive
	// requirement of the SDK. Under Go 1.17+ pruned module graphs its go.mod
	// edge is invisible until the staged root explicitly requires dep-one.
	writeTestFile(t, filepath.Join(depTwoSource, "go.mod"), "module example.com/ingot-dep-two\n\ngo 1.24.0\n")
	writeTestFile(t, filepath.Join(depTwoSource, "dep.go"), "package deptwo\n")
	writeModuleProxyVersion(t, proxy, "example.com/ingot-dep-two", "v1.0.0", depTwoSource)

	writeTestFile(t, filepath.Join(depOneSource, "go.mod"), "module example.com/ingot-dep-one\n\ngo 1.24.0\n\nrequire example.com/ingot-dep-two v1.0.0\n")
	writeTestFile(t, filepath.Join(depOneSource, "dep.go"), "package depone\n")
	writeModuleProxyVersion(t, proxy, "example.com/ingot-dep-one", "v1.0.0", depOneSource)

	sdkGoMod := "module example.com/ingot-test-sdk\n\ngo 1.24.0\n\nrequire example.com/ingot-dep-one v1.0.0\n"
	writeTestFile(t, filepath.Join(sdkSource, "go.mod"), sdkGoMod)
	writeTestFile(t, filepath.Join(sdkSource, "sdk.go"), `package sdk
import "context"
type Cleanup func(context.Context) error
type Optional[T any] struct { Value T; Valid bool }
func None[T any]() Optional[T] { return Optional[T]{} }
func Some[T any](value T) Optional[T] { return Optional[T]{Value:value, Valid:true} }
type Named[T any] struct { Name string; Value T }
`)
	writeTestFile(t, filepath.Join(sdkSource, "config", "config.go"), `package config
import (
	"context"
	"errors"
)
type PluginReference struct { ID, Name string }
func ResolveTables(data []byte, refs []PluginReference) (map[string][]byte, error) { if string(data) == "fail\n" { return nil, errors.New("requested config failure") }; result := map[string][]byte{}; for _, ref := range refs { result[ref.ID] = []byte{} }; return result, nil }
func Decode[T any]([]byte) (T, error) { var result T; return result, nil }
type stateKey struct{}
func WithStateDir(ctx context.Context, path string) context.Context { return context.WithValue(ctx, stateKey{}, path) }
`)
	writeTestApplicationPackage(t, sdkSource)
	writeModuleProxyVersion(t, proxy, "example.com/ingot-test-sdk", "v0.1.0", sdkSource)

	writeTestRemotePlugin(t, pluginSource)
	writeModuleProxyVersion(t, proxy, "example.com/ingot-test-plugin", "v1.0.0", pluginSource)

	home := t.TempDir()
	makeModuleCacheRemovable(t, home)
	desiredPath := filepath.Join(home, "plugins.toml")
	writeTestFile(t, desiredPath, "plugins_version = 1\n\n[[plugins]]\nmodule = \"example.com/ingot-test-plugin\"\nversion = \"v1.0.0\"\n")
	writeTestFile(t, filepath.Join(home, "config.toml"), "")
	t.Setenv("GOSUMDB", "off")
	desired, err := ParseDesired(desiredPath)
	if err != nil {
		t.Fatal(err)
	}
	moduleCache := filepath.Join(home, "cache", "gomod")
	lock, err := Resolve(context.Background(), desired, ResolveOptions{SDKModule: "example.com/ingot-test-sdk", SDKVersion: "v0.1.0", Toolchain: runtime.Version(), GOPROXY: "file://" + filepath.ToSlash(proxy), GOMODCACHE: moduleCache})
	if err != nil {
		t.Fatal(err)
	}
	selected := map[string]string{}
	for _, item := range lock.Modules {
		selected[item.Path] = item.Version
	}
	if selected["example.com/ingot-dep-one"] != "v1.0.0" {
		t.Fatalf("direct SDK dependency missing from lock: %#v", selected)
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
	proxy := filepath.Join(t.TempDir(), "proxy")
	sdkSource := filepath.Join(t.TempDir(), "sdk")
	writeTestSDKModule(t, sdkSource)
	writeModuleProxyVersion(t, proxy, "example.com/ingot-test-sdk", "v0.1.0", sdkSource)

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
		writeTestFile(t, configPath, "")
		t.Setenv("GOSUMDB", "off")
		desired, err := ParseDesired(desiredPath)
		if err != nil {
			t.Fatal(err)
		}
		moduleCache := filepath.Join(home, "cache", "gomod")
		lock, err := Resolve(context.Background(), desired, ResolveOptions{SDKModule: "example.com/ingot-test-sdk", SDKVersion: "v0.1.0", Toolchain: runtime.Version(), GOPROXY: "file://" + filepath.ToSlash(proxy), GOMODCACHE: moduleCache})
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

func writeTestSDKModule(t *testing.T, root string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/ingot-test-sdk\n\ngo 1.24.0\n")
	writeTestFile(t, filepath.Join(root, "sdk.go"), `package sdk
import "context"
type Cleanup func(context.Context) error
type Optional[T any] struct { Value T; Valid bool }
func None[T any]() Optional[T] { return Optional[T]{} }
func Some[T any](value T) Optional[T] { return Optional[T]{Value:value, Valid:true} }
type Named[T any] struct { Name string; Value T }
`)
	writeTestFile(t, filepath.Join(root, "config", "config.go"), `package config
import (
	"context"
	"errors"
)
type PluginReference struct { ID, Name string }
func ResolveTables(data []byte, refs []PluginReference) (map[string][]byte, error) { if string(data) == "fail\n" { return nil, errors.New("requested config failure") }; result := map[string][]byte{}; for _, ref := range refs { result[ref.ID] = []byte{} }; return result, nil }
func Decode[T any]([]byte) (T, error) { var result T; return result, nil }
type stateKey struct{}
func WithStateDir(ctx context.Context, path string) context.Context { return context.WithValue(ctx, stateKey{}, path) }
`)
	writeTestApplicationPackage(t, root)
}

func writeTestApplicationPackage(t *testing.T, root string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "application", "application.go"), `package application
import "context"
type Process interface { Arguments() []string; Check() bool; Shutdown(error) }
type processKey struct{}
func WithProcess(ctx context.Context, process Process) context.Context { return context.WithValue(ctx, processKey{}, process) }
func FromContext(ctx context.Context) (Process, bool) { process, ok := ctx.Value(processKey{}).(Process); return process, ok }
`)
}

func writeTestRemotePlugin(t *testing.T, root string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/ingot-test-plugin\n\ngo 1.24.0\n\nrequire example.com/ingot-test-sdk v0.1.0\n")
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
	"example.com/ingot-test-sdk"
)
type Config struct{}
type Dependencies struct{}
type Exports struct{}
func New(context.Context, Config, Dependencies) (Exports, sdk.Cleanup, error) { return Exports{}, nil, nil }
`)
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
