package builder

import (
	"context"
	"debug/buildinfo"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolvePluginDependencyVersions(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	for _, test := range []struct {
		name    string
		module  string
		version string
		remote  bool
	}{
		{name: "local", module: "example.com/provider", version: "v0.1.1"},
		{name: "local-v2", module: "example.com/provider/v2", version: "v2.1.1"},
		{name: "remote-pinned", module: "example.com/provider", version: "v0.1.1", remote: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			t.Setenv("GOSUMDB", "off")
			proxy := filepath.Join(t.TempDir(), "proxy")
			abi := t.TempDir()
			writeTestIngotABIModule(t, abi)
			writeModuleProxyVersion(t, proxy, IngotABIModulePath, IngotABIVersion, abi)
			installTomlProxy(t, proxy)

			provider, consumer := t.TempDir(), t.TempDir()
			for _, plugin := range []struct{ root, module, name string }{
				{provider, test.module, "provider"},
				{consumer, "example.com/consumer", "consumer"},
			} {
				writeTestFile(t, filepath.Join(plugin.root, "go.mod"), fmt.Sprintf("module %s\n\ngo 1.24.0\n\nrequire %s %s\n", plugin.module, IngotABIModulePath, IngotABIVersion))
				writeTestFile(t, filepath.Join(plugin.root, "ingot.plugin.toml"), fmt.Sprintf("manifest_version=1\nname=%q\ningot=\">=0.3.0 <0.4.0\"\nconfig_package=\".\"\n[[components]]\nname=\"default\"\npackage=\".\"\n", plugin.name))
			}
			writeTestFile(t, filepath.Join(provider, "contract", "greeting.go"), "package contract\ntype Greeting interface { Greet() string }\n")
			writeTestFile(t, filepath.Join(provider, "component.go"), fmt.Sprintf(`package provider
import (
    "context"
    ingotabi "github.com/ingot-agent/ingot-abi"
    contract %q
)
type greeting struct{}
func (greeting) Greet() string { return "local source" }
type Dependencies struct{}
type Exports struct { Greeting contract.Greeting }
func New(context.Context, Dependencies) (Exports, ingotabi.Cleanup, error) {
    return Exports{Greeting: greeting{}}, nil, nil
}
`, test.module+"/contract"))
			writeTestFile(t, filepath.Join(consumer, "go.mod"), fmt.Sprintf("module example.com/consumer\n\ngo 1.24.0\n\nrequire (\n%s %s\n%s %s\n)\n", IngotABIModulePath, IngotABIVersion, test.module, test.version))
			writeTestFile(t, filepath.Join(consumer, "component.go"), fmt.Sprintf(`package consumer
import (
    "context"
    "fmt"
    ingotabi "github.com/ingot-agent/ingot-abi"
    contract %q
)
type Dependencies struct { Greeting contract.Greeting }
type Exports struct{}
func New(_ context.Context, deps Dependencies) (Exports, ingotabi.Cleanup, error) {
    if deps.Greeting.Greet() != "local source" { return Exports{}, nil, fmt.Errorf("unexpected source") }
    return Exports{}, nil, nil
}
`, test.module+"/contract"))
			direct := DesiredPlugin{Module: test.module, Path: provider}
			if test.remote {
				writeModuleProxyVersion(t, proxy, test.module, "v0.1.0", provider)
				writeModuleProxyVersion(t, proxy, test.module, test.version, provider)
				direct = DesiredPlugin{Module: test.module, Version: "v0.1.0"}
			}
			home := t.TempDir()
			makeModuleCacheRemovable(t, home)
			desired := NewDesired(filepath.Join(home, "plugins.toml"), []DesiredPlugin{direct, {Module: "example.com/consumer", Path: consumer}})
			cache := filepath.Join(home, "cache", "gomod")
			lock, err := Resolve(context.Background(), desired, ResolveOptions{GOPROXY: "file://" + filepath.ToSlash(proxy), GOMODCACHE: cache})
			if test.remote {
				if err == nil || !strings.Contains(err.Error(), "INGOT-RESOLVE-DIRECT-VERSION") {
					t.Fatalf("remote version conflict = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			// No provider version exists in the proxy: the selected version is
			// graph bookkeeping and must still use the local source.
			var replacement Replacement
			for _, item := range lock.Replacements {
				if item.ModulePath == test.module {
					replacement = item
				}
			}
			if replacement.SyntheticVersion != test.version || replacement.DevPath != provider || lock.Plugins[0].SourceKind != "dev" || lock.Plugins[0].Version != "" {
				t.Fatalf("local plugin lock = %#v / %#v", replacement, lock.Plugins[0])
			}
			imageID, err := lock.ImageID()
			if err != nil {
				t.Fatal(err)
			}
			data, err := lock.MarshalTOML()
			if err != nil {
				t.Fatal(err)
			}
			lockPath := filepath.Join(home, "plugins.lock")
			writeTestFile(t, lockPath, string(data))
			lock, err = ParseLock(lockPath)
			if err != nil {
				t.Fatal(err)
			}
			if restoredID, err := lock.ImageID(); err != nil || restoredID != imageID {
				t.Fatalf("round-trip image ID = %q, %v; want %q", restoredID, err, imageID)
			}
			t.Setenv("GOPROXY", "off")
			result, err := Build(context.Background(), desired, lock, BuildOptions{Home: home, GOMODCACHE: cache})
			if err != nil {
				t.Fatal(err)
			}
			info, err := buildinfo.ReadFile(result.BinaryPath)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, dep := range info.Deps {
				if dep.Path == test.module {
					found = true
					if dep.Version != test.version || dep.Replace == nil || dep.Replace.Path != "../dev/"+devSourceDirName(test.module) {
						t.Fatalf("compiled dependency = %#v, replacement = %#v", dep, dep.Replace)
					}
				}
			}
			if !found {
				t.Fatal("provider missing from binary build info")
			}
			writeTestFile(t, filepath.Join(provider, "changed.txt"), "source drift")
			if _, err := Build(context.Background(), desired, lock, BuildOptions{Home: home, GOMODCACHE: cache}); err == nil || !strings.Contains(err.Error(), "INGOT-BUILD-DEV-DIGEST") {
				t.Fatalf("source drift error = %v", err)
			}
		})
	}
}

func TestResolveWorkspaceReplacementUsesFinalVersion(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	workspace := t.TempDir()
	t.Chdir(workspace)
	t.Setenv("GOSUMDB", "off")
	proxy := filepath.Join(t.TempDir(), "proxy")
	abi := t.TempDir()
	writeTestIngotABIModule(t, abi)
	writeModuleProxyVersion(t, proxy, IngotABIModulePath, IngotABIVersion, abi)
	installTomlProxy(t, proxy)

	contract := filepath.Join(workspace, "contract")
	writeTestFile(t, filepath.Join(contract, "go.mod"), "module example.com/contract\n\ngo 1.24.0\n")
	writeTestFile(t, filepath.Join(contract, "contract.go"), "package contract\n")
	writeModuleProxyVersion(t, proxy, "example.com/contract", "v1.0.0", contract)
	// The local replacement introduces an edge that raises its own selected
	// version only after workspace replacements have been discovered.
	writeTestFile(t, filepath.Join(contract, "go.mod"), "module example.com/contract\n\ngo 1.24.0\nrequire example.com/upgrader v1.0.0\n")
	upgrader := t.TempDir()
	writeTestFile(t, filepath.Join(upgrader, "go.mod"), "module example.com/upgrader\n\ngo 1.24.0\nrequire example.com/contract v1.1.0\n")
	writeTestFile(t, filepath.Join(upgrader, "upgrader.go"), "package upgrader\n")
	writeModuleProxyVersion(t, proxy, "example.com/upgrader", "v1.0.0", upgrader)
	writeTestFile(t, filepath.Join(workspace, "go.work"), "go 1.24.0\nreplace example.com/contract => ./contract\n")

	plugin := t.TempDir()
	writeTestRemotePlugin(t, plugin)
	writeTestFile(t, filepath.Join(plugin, "go.mod"), fmt.Sprintf("module example.com/ingot-test-plugin\n\ngo 1.24.0\nrequire (\n%s %s\nexample.com/contract v1.0.0\n)\n", IngotABIModulePath, IngotABIVersion))
	home := t.TempDir()
	makeModuleCacheRemovable(t, home)
	desired := NewDesired(filepath.Join(home, "plugins.toml"), []DesiredPlugin{{Module: "example.com/ingot-test-plugin", Path: plugin}})
	cache := filepath.Join(home, "cache", "gomod")
	lock, err := Resolve(context.Background(), desired, ResolveOptions{GOPROXY: "file://" + filepath.ToSlash(proxy), GOMODCACHE: cache})
	if err != nil {
		t.Fatal(err)
	}
	if len(lock.Replacements) != 2 || lock.Replacements[0].ModulePath != "example.com/contract" || lock.Replacements[0].SyntheticVersion != "v1.1.0" {
		t.Fatalf("workspace replacements = %#v", lock.Replacements)
	}
	root := t.TempDir()
	if err := lock.RestoreRootModule(root, nil); err != nil {
		t.Fatal(err)
	}
	output, err := runGo(context.Background(), root, lockedEnvironment(lock, cache), "list", "-mod=readonly", "-m", "-json", "all")
	if err != nil {
		t.Fatal(err)
	}
	selected, err := decodeModuleStream(output)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifySelectedGraph(lock, selected, nil); err != nil {
		t.Fatal(err)
	}
}

func TestValidateLocalReplacement(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	for _, test := range []struct {
		name        string
		replacement *resolvedModule
		valid       bool
	}{
		{name: "local", replacement: &resolvedModule{Path: directory, Dir: directory}, valid: true},
		{name: "missing"},
		{name: "wrong-directory", replacement: &resolvedModule{Path: directory + "-other", Dir: directory + "-other"}},
		{name: "remote", replacement: &resolvedModule{Path: "example.com/remote", Version: "v1.0.0", Dir: directory}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateLocalReplacement(resolvedModule{Path: "example.com/plugin", Version: "v0.1.1", Replace: test.replacement}, directory)
			if test.valid && err != nil {
				t.Fatal(err)
			}
			if !test.valid && (err == nil || !strings.Contains(err.Error(), "INGOT-RESOLVE-LOCAL-REPLACEMENT")) {
				t.Fatalf("local replacement error = %v", err)
			}
		})
	}
}
