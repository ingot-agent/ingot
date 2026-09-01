package builder

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/ingot-agent/ingot/internal/layout"
)

const tomlSum = "h1:mye9XuhQ6gvn5h28+VilKrrPoQVanw5PMw/TB0t5Ec4="
const tomlGoModSum = "h1:2gIqNv+qfxSVS7cM2xJQKtLSTLUE9V8t9Stt+h56mCY="

func TestLoadGraphStableManyOrderAndGenerate(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	t.Parallel()
	root := t.TempDir()
	_, sourceFile, _, _ := runtime.Caller(0)
	sdkRoot := os.Getenv("INGOT_SDK_ROOT")
	if sdkRoot == "" {
		sdkRoot = filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", "..", "sdk"))
	}
	ingotABIRoot := os.Getenv("INGOT_ABI_ROOT")
	if ingotABIRoot == "" {
		ingotABIRoot = filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", "..", "ingot-abi"))
	}
	if _, err := os.Stat(filepath.Join(ingotABIRoot, "go.mod")); err != nil {
		t.Skip("local ingot ABI checkout not found; set INGOT_ABI_ROOT to run this test")
	}
	providerA := filepath.Join(root, "provider-a")
	providerB := filepath.Join(root, "provider-b")
	consumer := filepath.Join(root, "consumer")
	writeProviderFixture(t, providerA, "example.com/provider-a", "provider_a")
	writeProviderFixture(t, providerB, "example.com/provider-b", "provider_b")
	writeConsumerFixture(t, consumer)
	rootGoMod := `module ingot.local/runtime-image

go 1.24.0

require (
	example.com/consumer v0.0.0
	example.com/provider-a v0.0.0
	example.com/provider-b v0.0.0
	github.com/ingot-agent/ingot-abi v0.1.0
	github.com/ingot-agent/sdk v0.1.6
	github.com/pelletier/go-toml/v2 v2.2.4
)

replace example.com/consumer => ./consumer
replace example.com/provider-a => ./provider-a
replace example.com/provider-b => ./provider-b
replace github.com/ingot-agent/ingot-abi => ` + filepath.ToSlash(ingotABIRoot) + `
replace github.com/ingot-agent/sdk => ` + filepath.ToSlash(sdkRoot) + "\n"
	writeTestFile(t, filepath.Join(root, "go.mod"), rootGoMod)
	writeTestFile(t, filepath.Join(root, "go.sum"), fmt.Sprintf("%s %s %s\n%s %s/go.mod %s\n", RuntimeSupportTOMLModule, RuntimeSupportTOMLVersion, tomlSum, RuntimeSupportTOMLModule, RuntimeSupportTOMLVersion, tomlGoModSum))

	lock := fixtureGraphLock(providerA, providerB, consumer)
	graph, err := LoadGraph(context.Background(), root, lock, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	wantOrder := []string{"example.com/provider-b/default", "example.com/provider-a/default", "example.com/consumer/default"}
	if len(graph.CreationOrder) != len(wantOrder) {
		t.Fatalf("creation order length = %d", len(graph.CreationOrder))
	}
	for i, want := range wantOrder {
		if graph.CreationOrder[i].ID != want {
			t.Fatalf("creation order[%d] = %s, want %s", i, graph.CreationOrder[i].ID, want)
		}
	}
	consumerComponent := graph.Components[2]
	providers := consumerComponent.DependencyList[0].Providers
	if len(providers) != 2 || providers[0].Component.PluginID != "example.com/provider-b" || providers[1].Component.PluginID != "example.com/provider-a" || !providers[0].Flatten || !providers[1].Flatten {
		t.Fatalf("MANY providers = %#v", providers)
	}
	hostDependencies := consumerComponent.DependencyList[2:]
	if len(hostDependencies) != 3 || !hostDependencies[0].Host || hostDependencies[0].HostType != "invocation" || !hostDependencies[1].Host || hostDependencies[1].HostType != "lifecycle" || !hostDependencies[2].Host || hostDependencies[2].HostType != "state" {
		t.Fatalf("host dependencies = %#v", hostDependencies)
	}
	if len(hostDependencies[0].Providers) != 0 || len(hostDependencies[1].Providers) != 0 || len(hostDependencies[2].Providers) != 0 {
		t.Fatalf("host dependencies must not participate in provider selection: %#v", hostDependencies)
	}
	if _, _, inspectedHost := inspectGraph(graph); len(inspectedHost) != 1 || len(inspectedHost["example.com/consumer/default"]) != 3 {
		t.Fatalf("inspection host dependencies = %#v", inspectedHost)
	}
	if err := Generate(root, lock, graph); err != nil {
		t.Fatal(err)
	}
	wiringData, err := os.ReadFile(filepath.Join(root, "wiring_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(wiringData), "os.MkdirAll(stateDir"); count != 1 {
		t.Fatalf("generated state directory creation count = %d, want only the state-consuming Component", count)
	}
	runtimePath := filepath.Join(root, layout.RuntimeExecutableName(runtime.GOOS))
	command := exec.Command("go", "build", "-mod=readonly", "-o", runtimePath, ".")
	command.Dir = root
	command.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local", "GOPROXY=off", "CGO_ENABLED=0")
	output, err := command.CombinedOutput()
	if err != nil {
		tidy := exec.Command("go", "mod", "tidy", "-diff")
		tidy.Dir = root
		tidy.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local", "GOPROXY=off", "CGO_ENABLED=0")
		diff, _ := tidy.CombinedOutput()
		t.Fatalf("generated runtime does not compile: %v\n%s\ngo mod tidy -diff:\n%s", err, output, diff)
	}
	configPath := filepath.Join(root, "config.toml")
	cleanupLog := filepath.Join(root, "cleanup.log")
	writeTestFile(t, configPath, fmt.Sprintf("[plugins.provider-b]\nlog=%q\n[plugins.provider-a]\nlog=%q\n[plugins.consumer]\nlog=%q\n", cleanupLog, cleanupLog, cleanupLog))
	check := exec.Command(runtimePath, "--ingot-check")
	check.Env = append(os.Environ(), "INGOT_CONFIG="+configPath, "INGOT_STATE_ROOT="+filepath.Join(root, "state"))
	if output, err := check.CombinedOutput(); err != nil {
		t.Fatalf("generated runtime check failed: %v\n%s", err, output)
	}
	cleanupBytes, err := os.ReadFile(cleanupLog)
	if err != nil {
		t.Fatal(err)
	}
	if string(cleanupBytes) != "consumer\nprovider_a\nprovider_b\n" {
		t.Fatalf("cleanup order = %q", cleanupBytes)
	}
	relativeCheck := exec.Command(runtimePath, "--ingot-check")
	relativeCheck.Dir = root
	relativeCheck.Env = replaceEnvironment(os.Environ(), map[string]string{"INGOT_CONFIG": configPath, "INGOT_STATE_ROOT": "relative-state"})
	if output, err := relativeCheck.CombinedOutput(); err != nil {
		t.Fatalf("relative state root was not normalized: %v\n%s", err, output)
	}
	if err := os.WriteFile(cleanupLog, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, configPath, fmt.Sprintf("[plugins.provider-b]\nlog=%q\n[plugins.provider-a]\nlog=%q\n[plugins.consumer]\nlog=%q\nfail=true\n", cleanupLog, cleanupLog, cleanupLog))
	failingCheck := exec.Command(runtimePath, "--ingot-check")
	failingCheck.Env = check.Env
	if err := failingCheck.Run(); err == nil || err.(*exec.ExitError).ExitCode() != 1 {
		t.Fatalf("construction failure exit = %v", err)
	}
	cleanupBytes, err = os.ReadFile(cleanupLog)
	if err != nil {
		t.Fatal(err)
	}
	if string(cleanupBytes) != "consumer\nprovider_a\nprovider_b\n" {
		t.Fatalf("failure cleanup order = %q", cleanupBytes)
	}
	if err := os.WriteFile(cleanupLog, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, configPath, fmt.Sprintf("[plugins.provider-b]\nlog=%q\nnull=true\n[plugins.provider-a]\nlog=%q\n[plugins.consumer]\nlog=%q\n", cleanupLog, cleanupLog, cleanupLog))
	nilCheck := exec.Command(runtimePath, "--ingot-check")
	nilCheck.Env = check.Env
	if err := nilCheck.Run(); err == nil || err.(*exec.ExitError).ExitCode() != 1 {
		t.Fatalf("typed-nil validation exit = %v", err)
	}
	cleanupBytes, err = os.ReadFile(cleanupLog)
	if err != nil {
		t.Fatal(err)
	}
	if string(cleanupBytes) != "provider_a\nprovider_b\n" {
		t.Fatalf("typed-nil cleanup order = %q", cleanupBytes)
	}
	usage := exec.Command(runtimePath, "--ingot-unknown")
	if err := usage.Run(); err == nil || err.(*exec.ExitError).ExitCode() != 2 {
		t.Fatalf("runtime usage exit = %v", err)
	}

	// Shutdown semantics: RequestShutdown(nil) is a normal completion, a
	// non-nil cause survives a later nil request, and both exit 0/1
	// deterministically with full reverse cleanup.
	if err := os.WriteFile(cleanupLog, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, configPath, fmt.Sprintf("[plugins.provider-b]\nlog=%q\n[plugins.provider-a]\nlog=%q\n[plugins.consumer]\nlog=%q\nshutdown=\"ok\"\n", cleanupLog, cleanupLog, cleanupLog))
	okRun := exec.Command(runtimePath)
	okRun.Env = check.Env
	if err := okRun.Run(); err != nil {
		t.Fatalf("normal shutdown run exit = %v", err)
	}
	cleanupBytes, err = os.ReadFile(cleanupLog)
	if err != nil {
		t.Fatal(err)
	}
	if string(cleanupBytes) != "consumer\nprovider_a\nprovider_b\n" {
		t.Fatalf("normal shutdown cleanup order = %q", cleanupBytes)
	}
	if err := os.WriteFile(cleanupLog, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, configPath, fmt.Sprintf("[plugins.provider-b]\nlog=%q\n[plugins.provider-a]\nlog=%q\n[plugins.consumer]\nlog=%q\nshutdown=\"fatal\"\n", cleanupLog, cleanupLog, cleanupLog))
	fatalRun := exec.Command(runtimePath)
	fatalRun.Env = check.Env
	if err := fatalRun.Run(); err == nil || err.(*exec.ExitError).ExitCode() != 1 {
		t.Fatalf("fatal shutdown run exit = %v", err)
	}
	cleanupBytes, err = os.ReadFile(cleanupLog)
	if err != nil {
		t.Fatal(err)
	}
	if string(cleanupBytes) != "consumer\nprovider_a\nprovider_b\n" {
		t.Fatalf("fatal shutdown cleanup order = %q", cleanupBytes)
	}
	if err := os.WriteFile(cleanupLog, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, configPath, fmt.Sprintf("[plugins.provider-b]\nlog=%q\n[plugins.provider-a]\nlog=%q\n[plugins.consumer]\nlog=%q\nshutdown=\"late-fatal\"\n", cleanupLog, cleanupLog, cleanupLog))
	lateFatalRun := exec.Command(runtimePath)
	lateFatalRun.Env = check.Env
	if err := lateFatalRun.Run(); err == nil || err.(*exec.ExitError).ExitCode() != 1 {
		t.Fatalf("late fatal shutdown run exit = %v", err)
	}
	cleanupBytes, err = os.ReadFile(cleanupLog)
	if err != nil {
		t.Fatal(err)
	}
	if string(cleanupBytes) != "consumer\nprovider_a\nprovider_b\n" {
		t.Fatalf("late fatal shutdown cleanup order = %q", cleanupBytes)
	}

	dependency := consumerComponent.DependencyList[0]
	originalCardinality, originalTarget := dependency.Cardinality, dependency.Target
	dependency.Cardinality, dependency.Target = CardinalityOne, dependency.Type
	if err := graph.resolve(); err == nil || !strings.Contains(err.Error(), "INGOT-GRAPH-PROVIDER-COUNT") {
		t.Fatalf("expected ONE ambiguity, got %v", err)
	}
	dependency.Cardinality, dependency.Target = originalCardinality, originalTarget
	providerComponent := graph.Components[0]
	providerComponent.DependencyList = append(providerComponent.DependencyList, &Dependency{Name: "Self", Type: dependency.Type, Target: originalTarget, Cardinality: CardinalityMany})
	if err := graph.resolve(); err == nil || !strings.Contains(err.Error(), "INGOT-GRAPH-SELF-LOOP") {
		t.Fatalf("expected self-loop error, got %v", err)
	}
}

func TestOfficialMultimodalSkeletonHasOneAssetProvider(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	root := t.TempDir()
	_, sourceFile, _, _ := runtime.Caller(0)
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", ".."))
	sdkRoot := filepath.Clean(filepath.Join(repositoryRoot, "..", "sdk"))
	abiRoot := filepath.Clean(filepath.Join(repositoryRoot, "..", "ingot-abi"))
	for _, candidate := range []string{filepath.Join(sdkRoot, "go.mod"), filepath.Join(abiRoot, "go.mod")} {
		if _, err := os.Stat(candidate); err != nil {
			t.Skipf("local multimodal contract checkout not found: %v", err)
		}
	}
	type pluginSpec struct {
		directory  string
		module     string
		name       string
		state      bool
		components []LockedComponent
	}
	plugins := []pluginSpec{
		{directory: "asset-local", module: "github.com/ingot-agent/asset-local", name: "asset.local", state: true},
		{directory: "http-default", module: "github.com/ingot-agent/http-default", name: "http.default"},
		{directory: "model-openai-compatible", module: "github.com/ingot-agent/model-openai-compatible", name: "model.openai-compatible"},
		{directory: "model-runtime", module: "github.com/ingot-agent/model-runtime", name: "model.runtime"},
		{directory: "tool-runtime", module: "github.com/ingot-agent/tool-runtime", name: "tool.runtime"},
		{directory: "prompt-default", module: "github.com/ingot-agent/prompt-default", name: "prompt.default"},
		{directory: "session-jsonl", module: "github.com/ingot-agent/session-jsonl", name: "session.jsonl", state: true},
		{directory: "agent-default", module: "github.com/ingot-agent/agent-default", name: "agent.default", components: []LockedComponent{
			{Name: "observation", Package: "./observation"},
			{Name: "default", Package: "."},
		}},
	}
	var goMod strings.Builder
	goMod.WriteString("module ingot.local/multimodal-skeleton\n\ngo 1.24.0\n\nrequire (\n")
	for _, plugin := range plugins {
		fmt.Fprintf(&goMod, "\t%s v0.0.0\n", plugin.module)
	}
	fmt.Fprintf(&goMod, "\t%s %s\n\tgithub.com/ingot-agent/sdk v0.2.3\n\t%s %s\n)\n\n", IngotABIModulePath, IngotABIVersion, RuntimeSupportTOMLModule, RuntimeSupportTOMLVersion)
	for _, plugin := range plugins {
		fmt.Fprintf(&goMod, "replace %s => %s\n", plugin.module, filepath.ToSlash(filepath.Join(repositoryRoot, "plugins", plugin.directory)))
	}
	fmt.Fprintf(&goMod, "replace %s => %s\nreplace github.com/ingot-agent/sdk => %s\n", IngotABIModulePath, filepath.ToSlash(abiRoot), filepath.ToSlash(sdkRoot))
	writeTestFile(t, filepath.Join(root, "go.mod"), goMod.String())
	var goSum strings.Builder
	for _, plugin := range plugins {
		raw, err := os.ReadFile(filepath.Join(repositoryRoot, "plugins", plugin.directory, "go.sum"))
		if err == nil {
			goSum.Write(raw)
		}
	}
	goSum.WriteString(fmt.Sprintf("%s %s %s\n%s %s/go.mod %s\n", RuntimeSupportTOMLModule, RuntimeSupportTOMLVersion, tomlSum, RuntimeSupportTOMLModule, RuntimeSupportTOMLVersion, tomlGoModSum))
	writeTestFile(t, filepath.Join(root, "go.sum"), goSum.String())

	digest := "sha256:" + strings.Repeat("0", 64)
	lock := &Lock{
		LockVersion: 3, PluginsDigest: digest, IngotVersion: "0.3.0", BuilderVersion: "0.3.0",
		Runtime:     RuntimeLock{ModulePath: IngotABIModulePath, Version: IngotABIVersion, Sum: "h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="},
		Toolchain:   ToolchainLock{Version: runtime.Version()},
		Target:      TargetLock{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, GOExperiment: []string{}, Tuning: defaultTuning(runtime.GOARCH)},
		Environment: EnvironmentLock{GOWORK: "off", GOTOOLCHAIN: "local", GOPROXY: "off", Mod: "readonly"},
		Build:       BuildLock{Trimpath: true, BuildVCS: false, Tags: []string{}, LDFlags: []string{}, GCFlags: []string{}, ASMFlags: []string{}},
		Modules: []LockedModule{
			{Path: IngotABIModulePath, Version: IngotABIVersion, Sum: "h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", GoModSum: "h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="},
			{Path: RuntimeSupportTOMLModule, Version: RuntimeSupportTOMLVersion, Sum: tomlSum, GoModSum: tomlGoModSum},
		},
	}
	for _, plugin := range plugins {
		components := plugin.components
		if len(components) == 0 {
			components = []LockedComponent{{Name: "default", Package: "."}}
		}
		locked := LockedPlugin{ID: plugin.module, Name: plugin.name, SourceKind: "dev", ManifestDigest: digest, RootPackage: ".", Components: components}
		if plugin.state {
			locked.HasState = true
			locked.StateSchemaVersion = 1
			locked.StateMinReaderVersion = 1
		}
		lock.Plugins = append(lock.Plugins, locked)
		lock.Replacements = append(lock.Replacements, Replacement{ModulePath: plugin.module, SyntheticVersion: "v0.0.0", DevPath: filepath.Join(repositoryRoot, "plugins", plugin.directory), ContentSHA256: digest})
	}
	sort.Slice(lock.Replacements, func(i, j int) bool { return lock.Replacements[i].ModulePath < lock.Replacements[j].ModulePath })
	graph, err := LoadGraph(context.Background(), root, lock, LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assetProviders := 0
	for _, component := range graph.Components {
		for _, dependency := range component.DependencyList {
			if (component.PluginName != "agent.default" || dependency.Name != "Assets") &&
				(component.PluginName != "model.openai-compatible" || dependency.Name != "Assets") {
				continue
			}
			if len(dependency.Providers) != 1 || dependency.Providers[0].Component.PluginName != "asset.local" || dependency.Providers[0].Export.Name != "Store" {
				t.Fatalf("%s Assets providers=%#v", component.PluginName, dependency.Providers)
			}
			assetProviders++
		}
	}
	if assetProviders != 2 {
		t.Fatalf("resolved %d asset consumer dependencies, want 2", assetProviders)
	}
}

func TestComponentCannotWrapOrExportHostType(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	t.Parallel()
	tests := []struct {
		name         string
		dependencies string
		exports      string
		wantCode     string
	}{
		{name: "direct export", exports: "Invocation invocation.Invocation", wantCode: "INGOT-COMPONENT-HOST-PROVIDER"},
		{name: "slice export", exports: "Invocations []invocation.Invocation", wantCode: "INGOT-COMPONENT-HOST-PROVIDER"},
		{name: "optional export", exports: "Invocation ingotabi.Optional[invocation.Invocation]", wantCode: "INGOT-COMPONENT-HOST-PROVIDER"},
		{name: "slice dependency", dependencies: "Invocations []invocation.Invocation", wantCode: "INGOT-COMPONENT-HOST-DEPENDENCY"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := loadInvalidHostComponent(t, test.dependencies, test.exports)
			if err == nil || !strings.Contains(err.Error(), test.wantCode) {
				t.Fatalf("host type error = %v, want %s", err, test.wantCode)
			}
		})
	}
}

func loadInvalidHostComponent(t *testing.T, dependencies, exports string) error {
	t.Helper()
	root := t.TempDir()
	_, sourceFile, _, _ := runtime.Caller(0)
	ingotABIRoot := os.Getenv("INGOT_ABI_ROOT")
	if ingotABIRoot == "" {
		ingotABIRoot = filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", "..", "ingot-abi"))
	}
	if _, err := os.Stat(filepath.Join(ingotABIRoot, "go.mod")); err != nil {
		t.Skip("local ingot ABI checkout not found; set INGOT_ABI_ROOT to run this test")
	}
	hostProvider := filepath.Join(root, "host-provider")
	writeTestFile(t, filepath.Join(hostProvider, "go.mod"), "module example.com/host-provider\n\ngo 1.24.0\n\nrequire github.com/ingot-agent/ingot-abi v0.1.0\n")
	writeTestFile(t, filepath.Join(hostProvider, "component.go"), `package hostprovider

import (
	"context"
	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/ingot-abi/invocation"
)

type Config struct{}
type Dependencies struct {
	`+dependencies+`
}
type Exports struct {
	`+exports+`
}
func New(context.Context, Config, Dependencies) (Exports, ingotabi.Cleanup, error) {
	return Exports{}, nil, nil
}
`)
	writeTestFile(t, filepath.Join(root, "go.mod"), `module ingot.local/runtime-image

go 1.24.0

require (
	example.com/host-provider v0.0.0
	github.com/ingot-agent/ingot-abi v0.1.0
	github.com/pelletier/go-toml/v2 v2.2.4
)

replace example.com/host-provider => ./host-provider
replace github.com/ingot-agent/ingot-abi => `+filepath.ToSlash(ingotABIRoot)+"\n")
	writeTestFile(t, filepath.Join(root, "go.sum"), fmt.Sprintf("%s %s %s\n%s %s/go.mod %s\n", RuntimeSupportTOMLModule, RuntimeSupportTOMLVersion, tomlSum, RuntimeSupportTOMLModule, RuntimeSupportTOMLVersion, tomlGoModSum))
	lock := fixtureGraphLock("/placeholder-a", "/placeholder-b", "/placeholder-c")
	lock.Plugins = []LockedPlugin{{ID: "example.com/host-provider", Name: "host-provider", SourceKind: "dev", ManifestDigest: "sha256:" + strings.Repeat("0", 64), RootPackage: ".", Components: []LockedComponent{{Name: "default", Package: "."}}}}
	lock.Runtime.Sum = ""
	lock.Modules = lock.Modules[1:]
	lock.Replacements = []Replacement{
		{ModulePath: "example.com/host-provider", SyntheticVersion: "v0.0.0", DevPath: hostProvider, ContentSHA256: "sha256:" + strings.Repeat("0", 64)},
		{ModulePath: IngotABIModulePath, SyntheticVersion: IngotABIVersion, DevPath: ingotABIRoot, ContentSHA256: "sha256:" + strings.Repeat("0", 64)},
	}
	_, err := LoadGraph(context.Background(), root, lock, LoadOptions{})
	return err
}

func writeProviderFixture(t *testing.T, root, modulePath, packageName string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module "+modulePath+"\n\ngo 1.24.0\n\nrequire github.com/ingot-agent/ingot-abi v0.1.0\n")
	writeTestFile(t, filepath.Join(root, "component.go"), `package `+packageName+`

import (
	"context"
	"fmt"
	"net/http"
	"os"
	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/httpx"
)

type Config struct {
	Log string `+"`toml:\"log\"`"+`
	Null bool `+"`toml:\"null\"`"+`
}
type Dependencies struct{}
type Exports struct { Clients []httpx.Client }
type client struct{}
func (*client) Do(context.Context, *http.Request) (*http.Response, error) { return nil, nil }
func New(_ context.Context, cfg Config, _ Dependencies) (Exports, ingotabi.Cleanup, error) {
	cleanup := func(context.Context) error { file, err := os.OpenFile(cfg.Log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); if err != nil { return err }; defer func() { _ = file.Close() }(); _, err = fmt.Fprintln(file, "`+packageName+`"); return err }
	if cfg.Null { var value *client; return Exports{Clients: []httpx.Client{value}}, cleanup, nil }
	return Exports{Clients: []httpx.Client{&client{}}}, cleanup, nil
}
`)
}

func writeConsumerFixture(t *testing.T, root string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/consumer\n\ngo 1.24.0\n\nrequire (\n\tgithub.com/ingot-agent/ingot-abi v0.1.0\n\tgithub.com/ingot-agent/sdk v0.1.6\n)\n")
	writeTestFile(t, filepath.Join(root, "component.go"), `package consumer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/ingot-abi/invocation"
	"github.com/ingot-agent/ingot-abi/lifecycle"
	"github.com/ingot-agent/ingot-abi/state"
	"github.com/ingot-agent/sdk/filesystem"
	"github.com/ingot-agent/sdk/httpx"
)

type Config struct {
	Log string `+"`toml:\"log\"`"+`
	Fail bool `+"`toml:\"fail\"`"+`
	Shutdown string `+"`toml:\"shutdown\"`"+`
}
type Dependencies struct {
	Clients []httpx.Client
	OptionalFS ingotabi.Optional[filesystem.FS]
	Invocation invocation.Invocation
	Lifecycle lifecycle.Controller
	State state.Scope
}
type Exports struct{}
func New(_ context.Context, cfg Config, deps Dependencies) (Exports, ingotabi.Cleanup, error) {
	if !filepath.IsAbs(deps.State.Dir()) { return Exports{}, nil, errors.New("state scope is not absolute") }
	cleanup := func(context.Context) error { if cfg.Shutdown == "late-fatal" { time.Sleep(100 * time.Millisecond) }; file, err := os.OpenFile(cfg.Log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); if err != nil { return err }; defer func() { _ = file.Close() }(); _, err = fmt.Fprintln(file, "consumer"); return err }
	if cfg.Shutdown != "" {
		go func() {
			time.Sleep(50 * time.Millisecond)
			if cfg.Shutdown == "fatal" {
				deps.Lifecycle.RequestShutdown(errors.New("fatal frontend failure"))
				deps.Lifecycle.RequestShutdown(nil)
				return
			}
			if cfg.Shutdown == "late-fatal" {
				deps.Lifecycle.RequestShutdown(nil)
				time.Sleep(25 * time.Millisecond)
				deps.Lifecycle.RequestShutdown(errors.New("late fatal frontend failure"))
				return
			}
			deps.Lifecycle.RequestShutdown(nil)
		}()
	}
	if cfg.Fail { return Exports{}, cleanup, errors.New("requested constructor failure") }
	return Exports{}, cleanup, nil
}
`)
}

func fixtureGraphLock(providerA, providerB, consumer string) *Lock {
	digest := "sha256:" + strings.Repeat("0", 64)
	plugin := func(id, name, path string) LockedPlugin {
		return LockedPlugin{ID: id, Name: name, SourceKind: "dev", ManifestDigest: digest, RootPackage: ".", Components: []LockedComponent{{Name: "default", Package: "."}}}
	}
	return &Lock{
		LockVersion: 3, PluginsDigest: digest, IngotVersion: "0.3.0", BuilderVersion: "0.3.0",
		Runtime:     RuntimeLock{ModulePath: IngotABIModulePath, Version: IngotABIVersion, Sum: "h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="},
		Toolchain:   ToolchainLock{Version: runtime.Version()},
		Target:      TargetLock{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, GOExperiment: []string{}, Tuning: defaultTuning(runtime.GOARCH)},
		Environment: EnvironmentLock{GOWORK: "off", GOTOOLCHAIN: "local", GOPROXY: "off", Mod: "readonly"}, Build: BuildLock{Trimpath: true, BuildVCS: false, Tags: []string{}, LDFlags: []string{}, GCFlags: []string{}, ASMFlags: []string{}},
		Plugins: []LockedPlugin{plugin("example.com/provider-b", "provider-b", providerB), plugin("example.com/provider-a", "provider-a", providerA), plugin("example.com/consumer", "consumer", consumer)},
		Modules: []LockedModule{
			{Path: IngotABIModulePath, Version: IngotABIVersion, Sum: "h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", GoModSum: "h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="},
			{Path: RuntimeSupportTOMLModule, Version: RuntimeSupportTOMLVersion, Sum: tomlSum, GoModSum: tomlGoModSum},
		},
		Replacements: []Replacement{
			{ModulePath: "example.com/consumer", SyntheticVersion: "v0.0.0", DevPath: consumer, ContentSHA256: digest},
			{ModulePath: "example.com/provider-a", SyntheticVersion: "v0.0.0", DevPath: providerA, ContentSHA256: digest},
			{ModulePath: "example.com/provider-b", SyntheticVersion: "v0.0.0", DevPath: providerB, ContentSHA256: digest},
		},
	}
}
