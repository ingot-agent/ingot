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
	lock, err := Resolve(context.Background(), desired, ResolveOptions{SDKModule: "example.com/ingot-test-sdk", SDKVersion: "v0.1.0", Toolchain: runtime.Version(), GOPROXY: "file://" + filepath.ToSlash(proxy), GOMODCACHE: moduleCache})
	if err != nil {
		t.Fatal(err)
	}
	if lock.Plugins[0].SourceKind != "dev" || len(lock.Replacements) != 1 || lock.Replacements[0].SyntheticVersion != "v0.0.0" {
		t.Fatalf("local lock materialization = %#v / %#v", lock.Plugins[0], lock.Replacements)
	}
	result, err := Build(context.Background(), desired, lock, BuildOptions{Home: home, ConfigPath: configPath, GOMODCACHE: moduleCache})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(result.BinaryPath); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(pluginSource, "changed.txt"), "drift")
	_, err = Build(context.Background(), desired, lock, BuildOptions{Home: home, ConfigPath: configPath, GOMODCACHE: moduleCache})
	if err == nil || !strings.Contains(err.Error(), "INGOT-BUILD-DEV-DIGEST") {
		t.Fatalf("source drift error = %v", err)
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
