package builder

import (
	"context"
	"go/types"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// loaderFixture writes a two-module fixture: a capability module and a plugin
// module that consumes it. The plugin's go directive is raised to the newest
// release the local toolchain accepts, which is the shape that used to be
// unloadable when the Core was built by an older Go.
func loaderFixture(t *testing.T, pluginDirective string) (root, environment string) {
	t.Helper()
	root = t.TempDir()
	capability := filepath.Join(root, "capability")
	plugin := filepath.Join(root, "plugin")
	writeTestFile(t, filepath.Join(capability, "go.mod"), "module example.com/capability\n\ngo "+pluginDirective+"\n")
	writeTestFile(t, filepath.Join(capability, "capability.go"), `package capability

type Store interface{ Load() string }

type Named struct{ Value string }
`)
	writeTestFile(t, filepath.Join(plugin, "go.mod"), "module example.com/plugin\n\ngo "+pluginDirective+"\n\nrequire example.com/capability v0.0.0\n\nreplace example.com/capability => ../capability\n")
	writeTestFile(t, filepath.Join(plugin, "plugin.go"), `package plugin

import "example.com/capability"

type Dependencies struct {
	Store capability.Store
	Named capability.Named
}

type Exports struct{ Store capability.Store }

func New(d Dependencies) Exports { return Exports{Store: d.Store} }
`)
	return plugin, "GOWORK=off"
}

func loaderEnvironment(t *testing.T) []string {
	t.Helper()
	return replaceEnvironment(os.Environ(), map[string]string{"GOWORK": "off", "GOTOOLCHAIN": "local", "GOPROXY": "off", "GOFLAGS": "-mod=mod", "GOSUMDB": "off"})
}

// TestLoadPackagesReadsCompilerExportData covers the Builder's replacement for
// in-process type checking: the compiler produces export data and the Builder
// only decodes it.
func TestLoadPackagesReadsCompilerExportData(t *testing.T) {
	t.Parallel()
	root, _ := loaderFixture(t, "1.21.0")
	loaded, diagnostics, err := loadPackages(context.Background(), root, loaderEnvironment(t), nil, []string{"example.com/plugin"})
	if err != nil {
		t.Fatal(err)
	}
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %v", diagnostics)
	}
	plugin := loaded["example.com/plugin"]
	capability := loaded["example.com/capability"]
	if plugin == nil || capability == nil {
		t.Fatalf("loaded packages = %v", len(loaded))
	}
	if plugin.Name != "plugin" || capability.Name != "capability" {
		t.Fatalf("package names = %q / %q", plugin.Name, capability.Name)
	}

	// The decoded field type must name the capability module's own object.
	// types.Identical only proves capability identity when both sides share
	// one types.Package, so this also covers the shared import map.
	structure, ok := plugin.Types.Scope().Lookup("Dependencies").Type().Underlying().(*types.Struct)
	if !ok {
		t.Fatal("Dependencies is not a struct")
	}
	if structure.NumFields() != 2 {
		t.Fatalf("field count = %d", structure.NumFields())
	}
	store, _ := capability.Types.Scope().Lookup("Store").Type().Underlying().(*types.Interface)
	if !types.Identical(structure.Field(0).Type().Underlying(), store) {
		t.Fatalf("field 0 = %s, want the capability module's Store interface", types.TypeString(structure.Field(0).Type(), packageQualifier))
	}
	if got := types.TypeString(structure.Field(1).Type(), packageQualifier); got != "example.com/capability.Named" {
		t.Fatalf("field 1 = %q", got)
	}

	// The constructor signature must be visible without parsing source.
	constructor, ok := plugin.Types.Scope().Lookup("New").Type().(*types.Signature)
	if !ok || constructor.Params().Len() != 1 || constructor.Results().Len() != 1 {
		t.Fatalf("New signature = %s", types.TypeString(constructor, packageQualifier))
	}
}

// TestLoadPackagesIgnoresBuilderGoVersion is the regression guard for the
// original defect: go/types refuses a package whose go directive is newer than
// the release that compiled the running program, so a Core built by an older Go
// could not analyze a plugin that had adopted a newer one.
//
// The fixture always declares a go directive at least as new as the running
// Core, and as new as the toolchain when the toolchain is newer still. When CI
// compiles the Core with an older Go than the toolchain, this test exercises the
// exact combination that previously failed.
func TestLoadPackagesIgnoresBuilderGoVersion(t *testing.T) {
	t.Parallel()
	toolchain, err := probeGoToolchain(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	directive := strings.TrimPrefix(toolchain, "go")
	builderVersion := runtime.Version()
	if compareGoVersions(normalizeOrZero(builderVersion), normalizeOrZero(toolchain)) > 0 {
		directive = strings.TrimPrefix(normalizeOrZero(builderVersion), "go")
	}
	if compareGoVersions(normalizeOrZero(builderVersion), normalizeOrZero(toolchain)) < 0 {
		t.Logf("Core built with %s, toolchain %s: this run covers the previously failing case", builderVersion, toolchain)
	}
	root, _ := loaderFixture(t, directive)
	loaded, diagnostics, err := loadPackages(context.Background(), root, loaderEnvironment(t), nil, []string{"example.com/plugin"})
	if err != nil {
		t.Fatal(err)
	}
	if len(diagnostics) != 0 {
		t.Fatalf("a plugin at go %s was rejected while the Core was built with %s: %v", directive, builderVersion, diagnostics)
	}
	if loaded["example.com/plugin"] == nil {
		t.Fatal("plugin package missing")
	}
}

func normalizeOrZero(value string) string {
	normalized, ok := normalizeGoVersion(value)
	if !ok {
		return MinimumGeneratedGoVersion
	}
	return normalized
}

// TestLoadPackagesReportsCompilerDiagnostics verifies plugin bugs still surface
// as build errors with the compiler's own wording.
func TestLoadPackagesReportsCompilerDiagnostics(t *testing.T) {
	t.Parallel()
	root, _ := loaderFixture(t, "1.21.0")
	writeTestFile(t, filepath.Join(root, "broken.go"), "package plugin\n\nfunc broken() { var value int = \"text\"; _ = value }\n")
	_, diagnostics, err := loadPackages(context.Background(), root, loaderEnvironment(t), nil, []string{"example.com/plugin"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(diagnostics, "\n")
	if !strings.Contains(joined, "cannot use") {
		t.Fatalf("compiler diagnostics missing: %q", joined)
	}
}

func TestReadExportDataNamesUpgradePath(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "not-export-data.a")
	if err := os.WriteFile(path, []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := readExportData(path, nil, map[string]*types.Package{}, "example.com/plugin")
	if err == nil {
		t.Fatal("malformed export data was accepted")
	}
}

func TestNonProgressOutputKeepsOnlyDiagnostics(t *testing.T) {
	t.Parallel()
	if got := nonProgressOutput("go: downloading example.com/a v1.0.0\ngo: extracting example.com/a v1.0.0\n\n"); got != nil {
		t.Fatalf("progress lines leaked into diagnostics: %v", got)
	}
	got := nonProgressOutput("go: downloading example.com/a v1.0.0\n# example.com/plugin\n./plugin.go:1:1: expected declaration\n")
	if len(got) != 1 || !strings.Contains(got[0], "expected declaration") || strings.Contains(got[0], "downloading") {
		t.Fatalf("diagnostics = %v", got)
	}
}
