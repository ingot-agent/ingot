package builder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"golang.org/x/mod/modfile"
)

const (
	DefaultIngotVersion   = "0.3.0"
	DefaultBuilderVersion = "0.3.0"
	DefaultSDKModule      = "github.com/ingot-agent/sdk"
	DefaultSDKVersion     = "v0.1.0"
)

type ResolveOptions struct {
	IngotVersion   string
	BuilderVersion string
	SDKModule      string
	SDKVersion     string
	Toolchain      string
	GOOS           string
	GOARCH         string
	GOExperiment   []string
	Tuning         []TargetKey
	Tags           []string
	LDFlags        []string
	GCFlags        []string
	ASMFlags       []string
	GOMODCACHE     string
	GOPROXY        string
}

func (options ResolveOptions) defaults() ResolveOptions {
	if options.IngotVersion == "" {
		options.IngotVersion = DefaultIngotVersion
	}
	if options.BuilderVersion == "" {
		options.BuilderVersion = DefaultBuilderVersion
	}
	if options.SDKModule == "" {
		options.SDKModule = DefaultSDKModule
	}
	if options.SDKVersion == "" {
		options.SDKVersion = DefaultSDKVersion
	}
	if options.Toolchain == "" {
		options.Toolchain = runtime.Version()
	}
	if options.GOOS == "" {
		options.GOOS = runtime.GOOS
	}
	if options.GOARCH == "" {
		options.GOARCH = runtime.GOARCH
	}
	if options.GOPROXY == "" {
		options.GOPROXY = os.Getenv("GOPROXY")
	}
	if options.GOPROXY == "" {
		options.GOPROXY = "https://proxy.golang.org,direct"
	}
	if options.Tuning == nil {
		options.Tuning = defaultTuning(options.GOARCH)
	}
	sort.Slice(options.Tuning, func(i, j int) bool { return options.Tuning[i].Key < options.Tuning[j].Key })
	options.GOExperiment, _ = sortedUnique(options.GOExperiment)
	options.Tags, _ = sortedUnique(options.Tags)
	return options
}

func defaultTuning(goarch string) []TargetKey {
	defaults := map[string]TargetKey{
		"386": {Key: "GO386", Value: "sse2"}, "amd64": {Key: "GOAMD64", Value: "v1"},
		"arm": {Key: "GOARM", Value: "7"}, "arm64": {Key: "GOARM64", Value: "v8.0"},
		"mips": {Key: "GOMIPS", Value: "hardfloat"}, "mipsle": {Key: "GOMIPS", Value: "hardfloat"},
		"mips64": {Key: "GOMIPS64", Value: "hardfloat"}, "mips64le": {Key: "GOMIPS64", Value: "hardfloat"},
		"ppc64": {Key: "GOPPC64", Value: "power8"}, "ppc64le": {Key: "GOPPC64", Value: "power8"},
		"riscv64": {Key: "GORISCV64", Value: "rva20u64"}, "wasm": {Key: "GOWASM", Value: "satconv,signext"},
	}
	if item, ok := defaults[goarch]; ok {
		return []TargetKey{item}
	}
	return []TargetKey{}
}

type resolvedModule struct {
	Path     string
	Version  string
	Sum      string
	GoModSum string
	Dir      string
	GoMod    string
	Main     bool
	Replace  *resolvedModule
	Error    *struct{ Err string }
}

type directSource struct {
	plugin    DesiredPlugin
	version   string
	devPath   string
	synthetic string
	digest    string
}

// Resolve turns a validated desired plugin set into a complete lock using the
// Go module resolver. It performs network-capable fetches but does not write the
// caller's plugins.toml or plugins.lock.
func Resolve(ctx context.Context, desired *DesiredPlugins, options ResolveOptions) (*Lock, error) {
	if err := desired.Validate(); err != nil {
		return nil, err
	}
	options = options.defaults()
	desiredDigest, err := desired.Digest()
	if err != nil {
		return nil, err
	}
	sources := make([]directSource, len(desired.Plugins))
	for i, plugin := range desired.Plugins {
		sources[i] = directSource{plugin: plugin, version: plugin.Version}
		if plugin.Path == "" {
			continue
		}
		absolute, pathErr := desired.ResolvePath(plugin.Path)
		if pathErr != nil {
			return nil, pathErr
		}
		identity, identityErr := moduleIdentity(filepath.Join(absolute, "go.mod"))
		if identityErr != nil {
			return nil, identityErr
		}
		if identity != plugin.Module {
			return nil, &Error{Code: "INGOT-PLUGINS-LOCAL-MODULE-MISMATCH", Field: fmt.Sprintf("plugins[%d].module", i), Plugin: plugin.Module, Want: plugin.Module, Actual: identity}
		}
		synthetic, versionErr := SyntheticVersion(plugin.Module)
		if versionErr != nil {
			return nil, versionErr
		}
		digest, digestErr := DevSourceDigest(absolute)
		if digestErr != nil {
			return nil, digestErr
		}
		sources[i].devPath, sources[i].synthetic, sources[i].digest, sources[i].version = absolute, synthetic, digest, synthetic
	}

	staging, err := os.MkdirTemp("", "ingot-resolve-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(staging) }()
	if err := writeResolveRoot(staging, options, sources); err != nil {
		return nil, err
	}
	environment := resolveEnvironment(options)
	downloadOutput, err := runGo(ctx, staging, environment, "mod", "download", "-json", "all")
	if err != nil {
		return nil, err
	}
	listOutput, err := runGo(ctx, staging, environment, "list", "-mod=mod", "-m", "-json", "all")
	if err != nil {
		return nil, err
	}
	selected, err := decodeModuleStream(listOutput)
	if err != nil {
		return nil, err
	}
	downloaded, err := decodeModuleStream(downloadOutput)
	if err != nil {
		return nil, err
	}
	downloadByKey := map[string]resolvedModule{}
	for _, item := range downloaded {
		downloadByKey[item.Path+"@"+item.Version] = item
	}
	selectedByPath := map[string]resolvedModule{}
	for _, item := range selected {
		selectedByPath[item.Path] = item
	}

	lockedPlugins := make([]LockedPlugin, len(sources))
	replacements := make([]Replacement, 0)
	nameOwners := map[string]string{}
	for i, source := range sources {
		selectedModule, ok := selectedByPath[source.plugin.Module]
		if !ok || selectedModule.Version != source.version {
			actual := "missing"
			if ok {
				actual = selectedModule.Version
			}
			return nil, &Error{Code: "INGOT-RESOLVE-DIRECT-VERSION", Field: fmt.Sprintf("plugins[%d].version", i), Plugin: source.plugin.Module, Want: source.version, Actual: actual}
		}
		moduleRoot := selectedModule.Dir
		if source.devPath != "" {
			moduleRoot = source.devPath
		}
		identity, identityErr := moduleIdentity(filepath.Join(moduleRoot, "go.mod"))
		if identityErr != nil {
			return nil, identityErr
		}
		if identity != source.plugin.Module {
			return nil, &Error{Code: "INGOT-RESOLVE-MODULE-IDENTITY", Plugin: source.plugin.Module, Want: source.plugin.Module, Actual: identity}
		}
		manifest, parseErr := ParseManifest(filepath.Join(moduleRoot, "ingot.plugin.toml"))
		if parseErr != nil {
			return nil, parseErr
		}
		rangeValue, _ := ParseVersionRange(manifest.Ingot)
		if !rangeValue.Contains(options.IngotVersion) {
			return nil, &Error{Code: "INGOT-MANIFEST-INCOMPATIBLE", Plugin: source.plugin.Module, Field: "ingot", Want: manifest.Ingot, Actual: options.IngotVersion}
		}
		if previous, exists := nameOwners[manifest.Name]; exists {
			return nil, &Error{Code: "INGOT-RESOLVE-DUPLICATE-NAME", Plugin: source.plugin.Module, Field: "name", Actual: manifest.Name, Want: "unique; also used by " + previous}
		}
		nameOwners[manifest.Name] = source.plugin.Module
		for _, packagePath := range append([]string{manifest.ConfigPackage}, componentPackages(manifest.Components)...) {
			if boundaryErr := validatePackageBoundary(moduleRoot, packagePath); boundaryErr != nil {
				return nil, &Error{Code: "INGOT-MANIFEST-PACKAGE-BOUNDARY", Plugin: source.plugin.Module, Field: packagePath, Err: boundaryErr}
			}
		}
		manifestDigest, digestErr := manifest.Digest()
		if digestErr != nil {
			return nil, digestErr
		}
		locked := LockedPlugin{ID: source.plugin.Module, Name: manifest.Name, ManifestDigest: manifestDigest, RootPackage: manifest.ConfigPackage, Components: make([]LockedComponent, len(manifest.Components))}
		if manifest.State != nil {
			locked.HasState = true
			locked.StateSchemaVersion = manifest.State.SchemaVersion
			locked.StateMinReaderVersion = manifest.State.MinReaderVersion
		}
		for j, component := range manifest.Components {
			locked.Components[j] = LockedComponent(component)
		}
		if source.devPath != "" {
			locked.SourceKind = "dev"
			replacements = append(replacements, Replacement{ModulePath: source.plugin.Module, SyntheticVersion: source.synthetic, DevPath: source.devPath, ContentSHA256: source.digest})
		} else {
			locked.SourceKind, locked.Version, locked.ModuleSum = "module", source.version, selectedModule.Sum
		}
		lockedPlugins[i] = locked
	}

	modules := make([]LockedModule, 0, len(selected))
	for _, selectedModule := range selected {
		if selectedModule.Main || selectedModule.Replace != nil {
			continue
		}
		download := downloadByKey[selectedModule.Path+"@"+selectedModule.Version]
		sum, goModSum := selectedModule.Sum, download.GoModSum
		if sum == "" {
			sum = download.Sum
		}
		if goModSum == "" {
			return nil, &Error{Code: "INGOT-RESOLVE-GOMOD-SUM", Plugin: selectedModule.Path, Actual: selectedModule.Version}
		}
		modules = append(modules, LockedModule{Path: selectedModule.Path, Version: selectedModule.Version, Sum: sum, GoModSum: goModSum})
	}
	sort.Slice(modules, func(i, j int) bool {
		if modules[i].Path != modules[j].Path {
			return modules[i].Path < modules[j].Path
		}
		return modules[i].Version < modules[j].Version
	})
	sort.Slice(replacements, func(i, j int) bool { return replacements[i].ModulePath < replacements[j].ModulePath })
	sdkSelected, ok := selectedByPath[options.SDKModule]
	if !ok || sdkSelected.Replace != nil {
		return nil, &Error{Code: "INGOT-RESOLVE-SDK", Want: options.SDKModule}
	}
	lock := &Lock{
		LockVersion: 1, PluginsDigest: desiredDigest, IngotVersion: options.IngotVersion, BuilderVersion: options.BuilderVersion,
		Replacements: replacements, SDK: SDKLock{ModulePath: options.SDKModule, Version: sdkSelected.Version}, Toolchain: ToolchainLock{Version: options.Toolchain},
		Target:      TargetLock{GOOS: options.GOOS, GOARCH: options.GOARCH, CGOEnabled: false, GOExperiment: options.GOExperiment, Tuning: options.Tuning},
		Environment: EnvironmentLock{GOWORK: "off", GOTOOLCHAIN: "local", GOPROXY: "off", Mod: "readonly"},
		Build:       BuildLock{Trimpath: true, BuildVCS: false, Tags: options.Tags, LDFlags: options.LDFlags, GCFlags: options.GCFlags, ASMFlags: options.ASMFlags},
		Plugins:     lockedPlugins, Modules: modules,
	}
	if err := lock.Validate(); err != nil {
		return nil, err
	}
	return lock, nil
}

func componentPackages(components []ManifestComponent) []string {
	result := make([]string, len(components))
	for i := range components {
		result[i] = components[i].Package
	}
	return result
}

func moduleIdentity(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", &Error{Code: "INGOT-MODULE-GOMOD", Path: path, Err: err}
	}
	parsed, err := modfile.Parse(path, data, nil)
	if err != nil {
		return "", &Error{Code: "INGOT-MODULE-GOMOD", Path: path, Err: err}
	}
	if parsed.Module == nil {
		return "", &Error{Code: "INGOT-MODULE-IDENTITY", Path: path, Want: "module directive"}
	}
	return parsed.Module.Mod.Path, nil
}

// ModuleIdentity reads the canonical module path from a go.mod file.
func ModuleIdentity(path string) (string, error) { return moduleIdentity(path) }

func writeResolveRoot(directory string, options ResolveOptions, sources []directSource) error {
	var content strings.Builder
	content.WriteString("module ingot.local/runtime-image\n\ngo ")
	content.WriteString(strings.TrimPrefix(options.Toolchain, "go"))
	content.WriteString("\n\nrequire (\n")
	seen := map[string]bool{}
	for _, source := range sources {
		_, _ = fmt.Fprintf(&content, "\t%s %s\n", source.plugin.Module, source.version)
		seen[source.plugin.Module] = true
	}
	if !seen[options.SDKModule] {
		_, _ = fmt.Fprintf(&content, "\t%s %s\n", options.SDKModule, options.SDKVersion)
	}
	content.WriteString(")\n")
	for _, source := range sources {
		if source.devPath != "" {
			_, _ = fmt.Fprintf(&content, "\nreplace %s => %s\n", source.plugin.Module, goModQuote(filepath.ToSlash(source.devPath)))
		}
	}
	parsed, err := modfile.Parse("go.mod", []byte(content.String()), nil)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(directory, "go.mod"), modfile.Format(parsed.Syntax), 0o644)
}

func resolveEnvironment(options ResolveOptions) []string {
	environment := append([]string{}, os.Environ()...)
	return replaceEnvironment(environment, map[string]string{"GOWORK": "off", "GOTOOLCHAIN": "local", "GOPROXY": options.GOPROXY, "GOMODCACHE": options.GOMODCACHE, "CGO_ENABLED": "0", "GOOS": options.GOOS, "GOARCH": options.GOARCH})
}

func replaceEnvironment(environment []string, replacements map[string]string) []string {
	result := make([]string, 0, len(environment)+len(replacements))
	seen := map[string]bool{}
	for _, item := range environment {
		key, _, _ := strings.Cut(item, "=")
		if value, ok := replacements[key]; ok {
			if !seen[key] && value != "" {
				result = append(result, key+"="+value)
				seen[key] = true
			}
			continue
		}
		result = append(result, item)
	}
	for key, value := range replacements {
		if !seen[key] && value != "" {
			result = append(result, key+"="+value)
		}
	}
	return result
}

func runGo(ctx context.Context, directory string, environment []string, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "go", arguments...)
	command.Dir = directory
	command.Env = environment
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		return nil, &Error{Code: "INGOT-GO-COMMAND", Path: directory, Actual: "go " + strings.Join(arguments, " "), Err: fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))}
	}
	return stdout.Bytes(), nil
}

func decodeModuleStream(data []byte) ([]resolvedModule, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var modules []resolvedModule
	for decoder.More() {
		var item resolvedModule
		if err := decoder.Decode(&item); err != nil {
			return nil, err
		}
		if item.Error != nil {
			return nil, fmt.Errorf("module %s: %s", item.Path, item.Error.Err)
		}
		modules = append(modules, item)
	}
	return modules, nil
}
