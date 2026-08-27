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
	"strconv"
	"strings"

	"golang.org/x/mod/modfile"
)

const (
	DefaultIngotVersion   = "0.3.0"
	DefaultBuilderVersion = "0.3.1"
)

type ResolveOptions struct {
	IngotVersion   string
	BuilderVersion string
	SDKs           []SDKConfig
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

func (options ResolveOptions) defaults() (ResolveOptions, error) {
	if options.IngotVersion == "" {
		options.IngotVersion = DefaultIngotVersion
	}
	if options.BuilderVersion == "" {
		options.BuilderVersion = DefaultBuilderVersion
	}
	config := BuilderConfig{BuilderConfigVersion: 1, SDKs: options.SDKs}
	if err := config.Validate(); err != nil {
		return options, err
	}
	options.SDKs = append([]SDKConfig(nil), config.SDKs...)
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
	return options, nil
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

type resolvedSDKConfig struct {
	SDKConfig
	replacement *Replacement
}

// Resolve turns a validated desired plugin set into a complete lock using the
// Go module resolver. It performs network-capable fetches but does not write the
// caller's plugins.toml or plugins.lock.
func Resolve(ctx context.Context, desired *DesiredPlugins, options ResolveOptions) (*Lock, error) {
	if err := desired.Validate(); err != nil {
		return nil, err
	}
	var err error
	options, err = options.defaults()
	if err != nil {
		return nil, err
	}
	sdks := make([]resolvedSDKConfig, len(options.SDKs))
	for index, configured := range options.SDKs {
		sdks[index].SDKConfig = configured
		if sdks[index].Path == "" {
			sdks[index].Path = workspaceModuleReplacement(configured.Module)
		}
		if sdks[index].Path == "" {
			continue
		}
		absolute, pathErr := filepath.Abs(sdks[index].Path)
		if pathErr != nil {
			return nil, pathErr
		}
		absolute = filepath.Clean(absolute)
		identity, identityErr := moduleIdentity(filepath.Join(absolute, "go.mod"))
		if identityErr != nil {
			return nil, identityErr
		}
		if identity != configured.Module {
			return nil, &Error{Code: "INGOT-RESOLVE-SDK-LOCAL-MODULE-MISMATCH", Path: absolute, Want: configured.Module, Actual: identity}
		}
		digest, digestErr := ModuleSourceDigest(absolute)
		if digestErr != nil {
			return nil, digestErr
		}
		sdks[index].Path = absolute
		sdks[index].replacement = &Replacement{ModulePath: configured.Module, SyntheticVersion: configured.Version, DevPath: absolute, ContentSHA256: digest}
	}
	for pluginIndex, plugin := range desired.Plugins {
		for sdkIndex, sdk := range sdks {
			if plugin.Module == sdk.Module {
				return nil, &Error{Code: "INGOT-RESOLVE-PLUGIN-SDK-CONFLICT", Field: fmt.Sprintf("plugins[%d].module", pluginIndex), Actual: plugin.Module, Want: fmt.Sprintf("distinct from sdks[%d].module", sdkIndex)}
			}
		}
	}
	for index := range sdks {
		options.SDKs[index] = sdks[index].SDKConfig
	}
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
	if err := writeResolveRoot(staging, options, sources, nil); err != nil {
		return nil, err
	}
	environment := resolveEnvironment(options)
	// Go 1.17+ pruned module graphs only include the requirement edges of
	// directly rooted edges. The staged root starts with just the direct
	// plugins and the SDK, so the first-pass `all` misses transitive modules
	// that a lock-rooted build would need; their go.mod files, sums and zips
	// must already be present in the module cache for the offline build.
	// Rewriting the staged go.mod to require every selected module and
	// re-listing converges on exactly the graph RestoreRootModule
	// materializes.
	var selected []resolvedModule
	var downloadOutput []byte
	for pass := 0; pass < resolveGraphPasses; pass++ {
		downloadOutput, err = runGo(ctx, staging, environment, "mod", "download", "-json", "all")
		if err != nil {
			return nil, err
		}
		listOutput, err := runGo(ctx, staging, environment, "list", "-mod=mod", "-m", "-json", "all")
		if err != nil {
			return nil, err
		}
		next, err := decodeModuleStream(listOutput)
		if err != nil {
			return nil, err
		}
		if selectedGraphEqual(selected, next) {
			selected = next
			break
		}
		selected = next
		if err := writeResolveRoot(staging, options, sources, selected); err != nil {
			return nil, err
		}
		if pass == resolveGraphPasses-1 {
			return nil, &Error{Code: "INGOT-RESOLVE-GRAPH-UNSTABLE", Want: "stable module graph", Actual: fmt.Sprintf("%d passes did not converge", resolveGraphPasses)}
		}
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
	for _, sdk := range sdks {
		if sdk.replacement != nil {
			replacements = append(replacements, *sdk.replacement)
		}
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
	lockedSDKs := make([]SDKLock, len(sdks))
	for sdkIndex, sdk := range sdks {
		selectedSDK, ok := selectedByPath[sdk.Module]
		if !ok || (sdk.replacement == nil && selectedSDK.Replace != nil) || (sdk.replacement != nil && (selectedSDK.Replace == nil || filepath.Clean(selectedSDK.Replace.Dir) != sdk.Path)) {
			return nil, &Error{Code: "INGOT-RESOLVE-SDK", Field: fmt.Sprintf("sdks[%d]", sdkIndex), Want: sdk.Module}
		}
		lockedSDKs[sdkIndex] = SDKLock{ModulePath: sdk.Module, Version: selectedSDK.Version}
		if sdk.replacement != nil {
			for replacementIndex := range replacements {
				if replacements[replacementIndex].ModulePath == sdk.Module {
					replacements[replacementIndex].SyntheticVersion = selectedSDK.Version
					break
				}
			}
		}
	}
	sort.Slice(replacements, func(i, j int) bool { return replacements[i].ModulePath < replacements[j].ModulePath })
	lock := &Lock{
		LockVersion: 2, PluginsDigest: desiredDigest, IngotVersion: options.IngotVersion, BuilderVersion: options.BuilderVersion,
		Replacements: replacements, SDKs: lockedSDKs, Toolchain: ToolchainLock{Version: options.Toolchain},
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

const resolveGraphPasses = 32

func selectedGraphEqual(previous, next []resolvedModule) bool {
	if len(previous) != len(next) {
		return false
	}
	keys := func(modules []resolvedModule) []string {
		result := make([]string, len(modules))
		for i, item := range modules {
			result[i] = item.Path + "@" + item.Version + "@" + strconv.FormatBool(item.Main)
		}
		return result
	}
	previousKeys, nextKeys := keys(previous), keys(next)
	sort.Strings(previousKeys)
	sort.Strings(nextKeys)
	for i := range previousKeys {
		if previousKeys[i] != nextKeys[i] {
			return false
		}
	}
	return true
}

func writeResolveRoot(directory string, options ResolveOptions, sources []directSource, transitive []resolvedModule) error {
	var content strings.Builder
	content.WriteString("module ingot.local/runtime-image\n\ngo ")
	content.WriteString(strings.TrimPrefix(options.Toolchain, "go"))
	content.WriteString("\n\nrequire (\n")
	seen := map[string]bool{}
	for _, source := range sources {
		_, _ = fmt.Fprintf(&content, "\t%s %s\n", source.plugin.Module, source.version)
		seen[source.plugin.Module] = true
	}
	for _, sdk := range options.SDKs {
		if !seen[sdk.Module] {
			_, _ = fmt.Fprintf(&content, "\t%s %s\n", sdk.Module, sdk.Version)
			seen[sdk.Module] = true
		}
	}
	for _, item := range transitive {
		if item.Main || seen[item.Path] || item.Version == "" {
			continue
		}
		_, _ = fmt.Fprintf(&content, "\t%s %s // indirect\n", item.Path, item.Version)
		seen[item.Path] = true
	}
	content.WriteString(")\n")
	for _, source := range sources {
		if source.devPath != "" {
			_, _ = fmt.Fprintf(&content, "\nreplace %s => %s\n", source.plugin.Module, goModQuote(filepath.ToSlash(source.devPath)))
		}
	}
	for _, sdk := range options.SDKs {
		if sdk.Path != "" {
			_, _ = fmt.Fprintf(&content, "\nreplace %s => %s\n", sdk.Module, goModQuote(filepath.ToSlash(sdk.Path)))
		}
	}
	parsed, err := modfile.Parse("go.mod", []byte(content.String()), nil)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(directory, "go.mod"), modfile.Format(parsed.Syntax), 0o644)
}

func workspaceModuleReplacement(modulePath string) string {
	directory, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		workPath := filepath.Join(directory, "go.work")
		data, readErr := os.ReadFile(workPath)
		if readErr == nil {
			work, parseErr := modfile.ParseWork(workPath, data, nil)
			if parseErr != nil {
				return ""
			}
			for _, replacement := range work.Replace {
				if replacement.Old.Path != modulePath || replacement.Old.Version != "" || replacement.New.Version != "" {
					continue
				}
				path := replacement.New.Path
				if !filepath.IsAbs(path) {
					path = filepath.Join(directory, filepath.FromSlash(path))
				}
				return filepath.Clean(path)
			}
			return ""
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return ""
		}
		directory = parent
	}
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
