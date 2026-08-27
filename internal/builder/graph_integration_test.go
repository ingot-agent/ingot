package builder

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ingot-agent/ingot/internal/layout"
)

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
	if _, err := os.Stat(filepath.Join(sdkRoot, "go.mod")); err != nil {
		t.Skip("local SDK checkout not found; set INGOT_SDK_ROOT to run this test")
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
	github.com/ingot-agent/sdk v0.1.0
	github.com/pelletier/go-toml/v2 v2.2.4 // indirect
)

replace example.com/consumer => ./consumer
replace example.com/provider-a => ./provider-a
replace example.com/provider-b => ./provider-b
replace github.com/ingot-agent/sdk => ` + filepath.ToSlash(sdkRoot) + "\n"
	writeTestFile(t, filepath.Join(root, "go.mod"), rootGoMod)
	sdkSums, err := os.ReadFile(filepath.Join(sdkRoot, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.sum"), sdkSums, 0o644); err != nil {
		t.Fatal(err)
	}

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
	if err := Generate(root, lock, graph); err != nil {
		t.Fatal(err)
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

func writeProviderFixture(t *testing.T, root, modulePath, packageName string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module "+modulePath+"\n\ngo 1.24.0\n\nrequire github.com/ingot-agent/sdk v0.1.0\n")
	writeTestFile(t, filepath.Join(root, "component.go"), `package `+packageName+`

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"github.com/ingot-agent/sdk"
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
func New(_ context.Context, cfg Config, _ Dependencies) (Exports, sdk.Cleanup, error) {
	cleanup := func(context.Context) error { file, err := os.OpenFile(cfg.Log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); if err != nil { return err }; defer func() { _ = file.Close() }(); _, err = fmt.Fprintln(file, "`+packageName+`"); return err }
	if cfg.Null { var value *client; return Exports{Clients: []httpx.Client{value}}, cleanup, nil }
	return Exports{Clients: []httpx.Client{&client{}}}, cleanup, nil
}
`)
}

func writeConsumerFixture(t *testing.T, root string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/consumer\n\ngo 1.24.0\n\nrequire github.com/ingot-agent/sdk v0.1.0\n")
	writeTestFile(t, filepath.Join(root, "component.go"), `package consumer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"github.com/ingot-agent/sdk"
	"github.com/ingot-agent/sdk/filesystem"
	"github.com/ingot-agent/sdk/httpx"
)

type Config struct {
	Log string `+"`toml:\"log\"`"+`
	Fail bool `+"`toml:\"fail\"`"+`
}
type Dependencies struct {
	Clients []httpx.Client
	OptionalFS sdk.Optional[filesystem.FS]
}
type Exports struct{}
func New(_ context.Context, cfg Config, _ Dependencies) (Exports, sdk.Cleanup, error) {
	cleanup := func(context.Context) error { file, err := os.OpenFile(cfg.Log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); if err != nil { return err }; defer func() { _ = file.Close() }(); _, err = fmt.Fprintln(file, "consumer"); return err }
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
		LockVersion: 2, PluginsDigest: digest, IngotVersion: "0.3.0", BuilderVersion: "0.3.0",
		SDKs: []SDKLock{{ModulePath: "github.com/ingot-agent/sdk", Version: "v0.1.0"}}, Toolchain: ToolchainLock{Version: runtime.Version()},
		Target:      TargetLock{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, GOExperiment: []string{}, Tuning: defaultTuning(runtime.GOARCH)},
		Environment: EnvironmentLock{GOWORK: "off", GOTOOLCHAIN: "local", GOPROXY: "off", Mod: "readonly"}, Build: BuildLock{Trimpath: true, BuildVCS: false, Tags: []string{}, LDFlags: []string{}, GCFlags: []string{}, ASMFlags: []string{}},
		Plugins: []LockedPlugin{plugin("example.com/provider-b", "provider-b", providerB), plugin("example.com/provider-a", "provider-a", providerA), plugin("example.com/consumer", "consumer", consumer)},
		Modules: []LockedModule{{Path: "github.com/ingot-agent/sdk", Version: "v0.1.0", Sum: "h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", GoModSum: "h1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}},
		Replacements: []Replacement{
			{ModulePath: "example.com/consumer", SyntheticVersion: "v0.0.0", DevPath: consumer, ContentSHA256: digest},
			{ModulePath: "example.com/provider-a", SyntheticVersion: "v0.0.0", DevPath: providerA, ContentSHA256: digest},
			{ModulePath: "example.com/provider-b", SyntheticVersion: "v0.0.0", DevPath: providerB, ContentSHA256: digest},
		},
	}
}
