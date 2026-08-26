package builder

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

var (
	digestPattern    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	toolchainPattern = regexp.MustCompile(`^go1\.[0-9]+\.[0-9]+$`)
	platformPattern  = regexp.MustCompile(`^[a-z0-9]+$`)
)

var supportedTargets = map[string]bool{
	"aix/ppc64": true, "android/386": true, "android/amd64": true, "android/arm": true, "android/arm64": true,
	"darwin/amd64": true, "darwin/arm64": true, "dragonfly/amd64": true,
	"freebsd/386": true, "freebsd/amd64": true, "freebsd/arm": true, "freebsd/arm64": true,
	"illumos/amd64": true, "ios/amd64": true, "ios/arm64": true, "js/wasm": true,
	"linux/386": true, "linux/amd64": true, "linux/arm": true, "linux/arm64": true, "linux/loong64": true,
	"linux/mips": true, "linux/mips64": true, "linux/mips64le": true, "linux/mipsle": true,
	"linux/ppc64": true, "linux/ppc64le": true, "linux/riscv64": true, "linux/s390x": true,
	"netbsd/386": true, "netbsd/amd64": true, "netbsd/arm": true, "netbsd/arm64": true,
	"openbsd/386": true, "openbsd/amd64": true, "openbsd/arm": true, "openbsd/arm64": true, "openbsd/ppc64": true, "openbsd/riscv64": true,
	"plan9/386": true, "plan9/amd64": true, "plan9/arm": true, "solaris/amd64": true, "wasip1/wasm": true,
	"windows/386": true, "windows/amd64": true, "windows/arm64": true,
}

type Lock struct {
	LockVersion    int             `toml:"lock_version"`
	PluginsDigest  string          `toml:"plugins_digest"`
	IngotVersion   string          `toml:"ingot_version"`
	BuilderVersion string          `toml:"builder_version"`
	Replacements   []Replacement   `toml:"replacements"`
	SDK            SDKLock         `toml:"sdk"`
	Toolchain      ToolchainLock   `toml:"toolchain"`
	Target         TargetLock      `toml:"target"`
	Environment    EnvironmentLock `toml:"environment"`
	Build          BuildLock       `toml:"build"`
	Plugins        []LockedPlugin  `toml:"plugins"`
	Modules        []LockedModule  `toml:"modules"`
	filePath       string
}

type SDKLock struct {
	ModulePath string `toml:"module_path" json:"module_path"`
	Version    string `toml:"version" json:"version"`
}

type ToolchainLock struct {
	Version string `toml:"version"`
}

type TargetLock struct {
	GOOS         string      `toml:"goos"`
	GOARCH       string      `toml:"goarch"`
	CGOEnabled   bool        `toml:"cgo_enabled"`
	GOExperiment []string    `toml:"goexperiment"`
	Tuning       []TargetKey `toml:"tuning"`
}

type TargetKey struct {
	Key   string `toml:"key"`
	Value string `toml:"value"`
}

type EnvironmentLock struct {
	GOWORK      string `toml:"gowork" json:"gowork"`
	GOTOOLCHAIN string `toml:"gotoolchain" json:"gotoolchain"`
	GOPROXY     string `toml:"goproxy" json:"goproxy"`
	Mod         string `toml:"mod" json:"mod"`
}

type BuildLock struct {
	Trimpath bool     `toml:"trimpath" json:"trimpath"`
	BuildVCS bool     `toml:"buildvcs" json:"buildvcs"`
	Tags     []string `toml:"tags" json:"tags"`
	LDFlags  []string `toml:"ldflags" json:"ldflags"`
	GCFlags  []string `toml:"gcflags" json:"gcflags"`
	ASMFlags []string `toml:"asmflags" json:"asmflags"`
}

type LockedPlugin struct {
	ID                    string            `toml:"id"`
	Name                  string            `toml:"name"`
	SourceKind            string            `toml:"source_kind"`
	Version               string            `toml:"version,omitempty"`
	ModuleSum             string            `toml:"module_sum,omitempty"`
	ManifestDigest        string            `toml:"manifest_digest"`
	RootPackage           string            `toml:"root_package"`
	HasState              bool              `toml:"has_state"`
	StateSchemaVersion    int               `toml:"state_schema_version"`
	StateMinReaderVersion int               `toml:"state_min_reader_version"`
	Components            []LockedComponent `toml:"components"`
}

type LockedComponent struct {
	Name    string `toml:"name" json:"name"`
	Package string `toml:"package" json:"package"`
}

type LockedModule struct {
	Path     string `toml:"path" json:"path"`
	Version  string `toml:"version" json:"version"`
	Sum      string `toml:"sum" json:"sum"`
	GoModSum string `toml:"go_mod_sum" json:"go_mod_sum"`
}

type Replacement struct {
	ModulePath       string `toml:"module_path"`
	SyntheticVersion string `toml:"synthetic_version"`
	DevPath          string `toml:"dev_path"`
	ContentSHA256    string `toml:"content_sha256"`
}

// ParseLock strictly parses and semantically validates plugins.lock v1.
func ParseLock(filePath string) (*Lock, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, diagnostic("INGOT-LOCK-PARSE", filePath, "", err)
	}
	if err := validateLockPresence(data); err != nil {
		return nil, &Error{Code: "INGOT-LOCK-SCHEMA", Path: filePath, Err: err}
	}
	var lock Lock
	decoder := toml.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&lock); err != nil {
		return nil, diagnostic("INGOT-LOCK-PARSE", filePath, "", err)
	}
	lock.filePath = filePath
	if err := lock.Validate(); err != nil {
		if diagnosticErr, ok := err.(*Error); ok && diagnosticErr.Path == "" {
			diagnosticErr.Path = filePath
		}
		return nil, err
	}
	return &lock, nil
}

func validateLockPresence(data []byte) error {
	var document map[string]any
	if err := toml.Unmarshal(data, &document); err != nil {
		return err
	}
	for _, key := range []string{"lock_version", "plugins_digest", "ingot_version", "builder_version", "replacements", "sdk", "toolchain", "target", "environment", "build", "plugins", "modules"} {
		if _, ok := document[key]; !ok {
			return fmt.Errorf("missing required field %s", key)
		}
	}
	for table, keys := range map[string][]string{
		"sdk": {"module_path", "version"}, "toolchain": {"version"},
		"target":      {"goos", "goarch", "cgo_enabled", "goexperiment", "tuning"},
		"environment": {"gowork", "gotoolchain", "goproxy", "mod"},
		"build":       {"trimpath", "buildvcs", "tags", "ldflags", "gcflags", "asmflags"},
	} {
		values, ok := document[table].(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be a table", table)
		}
		for _, key := range keys {
			if _, ok := values[key]; !ok {
				return fmt.Errorf("missing required field %s.%s", table, key)
			}
		}
	}
	pluginTables, ok := tomlTableArray(document["plugins"])
	if !ok {
		return fmt.Errorf("plugins must be an array of tables")
	}
	for index, plugin := range pluginTables {
		for _, key := range []string{"id", "name", "source_kind", "manifest_digest", "root_package", "has_state", "state_schema_version", "state_min_reader_version", "components"} {
			if _, exists := plugin[key]; !exists {
				return fmt.Errorf("missing required field plugins[%d].%s", index, key)
			}
		}
		sourceKind, _ := plugin["source_kind"].(string)
		if sourceKind == "module" {
			for _, key := range []string{"version", "module_sum"} {
				if _, exists := plugin[key]; !exists {
					return fmt.Errorf("missing required field plugins[%d].%s", index, key)
				}
			}
		}
		components, valid := tomlTableArray(plugin["components"])
		if !valid {
			return fmt.Errorf("plugins[%d].components must be an array of tables", index)
		}
		for componentIndex, component := range components {
			for _, key := range []string{"name", "package"} {
				if _, exists := component[key]; !exists {
					return fmt.Errorf("missing required field plugins[%d].components[%d].%s", index, componentIndex, key)
				}
			}
		}
	}
	moduleTables, ok := tomlTableArray(document["modules"])
	if !ok {
		return fmt.Errorf("modules must be an array of tables")
	}
	for index, item := range moduleTables {
		for _, key := range []string{"path", "version", "sum", "go_mod_sum"} {
			if _, exists := item[key]; !exists {
				return fmt.Errorf("missing required field modules[%d].%s", index, key)
			}
		}
	}
	replacementTables, ok := tomlTableArray(document["replacements"])
	if !ok {
		return fmt.Errorf("replacements must be an array of tables")
	}
	for index, item := range replacementTables {
		for _, key := range []string{"module_path", "synthetic_version", "dev_path", "content_sha256"} {
			if _, exists := item[key]; !exists {
				return fmt.Errorf("missing required field replacements[%d].%s", index, key)
			}
		}
	}
	return nil
}

func tomlTableArray(value any) ([]map[string]any, bool) {
	reflection := reflect.ValueOf(value)
	if !reflection.IsValid() || reflection.Kind() != reflect.Slice {
		return nil, false
	}
	result := make([]map[string]any, reflection.Len())
	for index := 0; index < reflection.Len(); index++ {
		table, ok := reflection.Index(index).Interface().(map[string]any)
		if !ok {
			return nil, false
		}
		result[index] = table
	}
	return result, true
}

func (l *Lock) Validate() error {
	if l.LockVersion != 1 {
		return &Error{Code: "INGOT-LOCK-UNSUPPORTED-VERSION", Field: "lock_version", Want: "1", Actual: strconv.Itoa(l.LockVersion)}
	}
	if !digestPattern.MatchString(l.PluginsDigest) {
		return &Error{Code: "INGOT-LOCK-PLUGINS-DIGEST", Field: "plugins_digest", Actual: l.PluginsDigest}
	}
	for field, version := range map[string]string{"ingot_version": l.IngotVersion, "builder_version": l.BuilderVersion} {
		if !semverPattern.MatchString(version) || !semver.IsValid("v"+version) {
			return &Error{Code: "INGOT-LOCK-VERSION", Field: field, Actual: version, Want: "canonical SemVer without v prefix"}
		}
	}
	if err := module.CheckPath(l.SDK.ModulePath); err != nil {
		return &Error{Code: "INGOT-LOCK-SDK-MODULE", Field: "sdk.module_path", Actual: l.SDK.ModulePath, Err: err}
	}
	if module.CanonicalVersion(l.SDK.Version) != l.SDK.Version || module.Check(l.SDK.ModulePath, l.SDK.Version) != nil {
		return &Error{Code: "INGOT-LOCK-SDK-VERSION", Field: "sdk.version", Actual: l.SDK.Version, Want: "canonical version matching sdk.module_path"}
	}
	if !toolchainPattern.MatchString(l.Toolchain.Version) {
		return &Error{Code: "INGOT-LOCK-TOOLCHAIN", Field: "toolchain.version", Actual: l.Toolchain.Version, Want: "go1.x.y"}
	}
	if !platformPattern.MatchString(l.Target.GOOS) || !platformPattern.MatchString(l.Target.GOARCH) || !supportedTargets[l.Target.GOOS+"/"+l.Target.GOARCH] {
		return &Error{Code: "INGOT-LOCK-TARGET", Field: "target", Actual: l.Target.GOOS + "/" + l.Target.GOARCH}
	}
	if l.Target.CGOEnabled {
		return &Error{Code: "INGOT-LOCK-CGO", Field: "target.cgo_enabled", Want: "false", Actual: "true"}
	}
	if err := validateSortedSet("target.goexperiment", &l.Target.GOExperiment); err != nil {
		return err
	}
	for _, experiment := range l.Target.GOExperiment {
		if !regexp.MustCompile(`^[A-Za-z0-9]+$`).MatchString(experiment) {
			return &Error{Code: "INGOT-LOCK-GOEXPERIMENT", Field: "target.goexperiment", Actual: experiment}
		}
	}
	if err := validateTuning(l.Target); err != nil {
		return err
	}
	if l.Environment != (EnvironmentLock{GOWORK: "off", GOTOOLCHAIN: "local", GOPROXY: "off", Mod: "readonly"}) {
		return &Error{Code: "INGOT-LOCK-ENVIRONMENT", Field: "environment", Want: "gowork=off, gotoolchain=local, goproxy=off, mod=readonly", Actual: fmt.Sprintf("%+v", l.Environment)}
	}
	if !l.Build.Trimpath || l.Build.BuildVCS {
		return &Error{Code: "INGOT-LOCK-BUILD-POLICY", Field: "build", Want: "trimpath=true, buildvcs=false"}
	}
	if err := validateSortedSet("build.tags", &l.Build.Tags); err != nil {
		return err
	}
	for _, tag := range l.Build.Tags {
		if !regexp.MustCompile(`^[A-Za-z0-9_.]+$`).MatchString(tag) {
			return &Error{Code: "INGOT-LOCK-BUILD-TAG", Field: "build.tags", Actual: tag}
		}
	}
	if len(l.Plugins) == 0 || len(l.Modules) == 0 {
		return &Error{Code: "INGOT-LOCK-EMPTY", Field: "plugins/modules", Want: "non-empty collections"}
	}
	if err := l.validatePluginsAndGraph(); err != nil {
		return err
	}
	return nil
}

func validateSortedSet(field string, values *[]string) error {
	normalized, _ := sortedUnique(*values)
	if strings.Join(normalized, "\x00") != strings.Join(*values, "\x00") {
		return &Error{Code: "INGOT-LOCK-NONCANONICAL-SET", Field: field, Want: "sorted unique values"}
	}
	return nil
}

func validateTuning(target TargetLock) error {
	keyByArch := map[string]string{
		"amd64": "GOAMD64", "386": "GO386", "arm": "GOARM", "arm64": "GOARM64", "mips": "GOMIPS", "mipsle": "GOMIPS",
		"mips64": "GOMIPS64", "mips64le": "GOMIPS64", "ppc64": "GOPPC64", "ppc64le": "GOPPC64", "riscv64": "GORISCV64", "wasm": "GOWASM",
	}
	expectedKey := keyByArch[target.GOARCH]
	if expectedKey == "" && len(target.Tuning) != 0 {
		return &Error{Code: "INGOT-LOCK-TUNING", Field: "target.tuning", Want: "empty for " + target.GOARCH}
	}
	if expectedKey != "" && (len(target.Tuning) != 1 || target.Tuning[0].Key != expectedKey) {
		return &Error{Code: "INGOT-LOCK-TUNING", Field: "target.tuning", Want: "one materialized " + expectedKey + " entry"}
	}
	seen := map[string]bool{}
	previous := ""
	for _, item := range target.Tuning {
		if item.Key <= previous || seen[item.Key] || item.Key != expectedKey || !validTuningValue(item.Key, item.Value) {
			return &Error{Code: "INGOT-LOCK-TUNING", Field: "target.tuning", Actual: item.Key + "=" + item.Value, Want: "sorted applicable unique tuning key"}
		}
		seen[item.Key] = true
		previous = item.Key
	}
	return nil
}

func validTuningValue(key, value string) bool {
	switch key {
	case "GOAMD64":
		return regexp.MustCompile(`^v[1-4]$`).MatchString(value)
	case "GO386":
		return value == "sse2" || value == "softfloat"
	case "GOARM":
		return regexp.MustCompile(`^[567](?:,softfloat)?$`).MatchString(value)
	case "GOARM64":
		return regexp.MustCompile(`^v(?:8\.[0-9]|9\.[0-5])(?:,(?:lse|crypto))*$`).MatchString(value)
	case "GOMIPS", "GOMIPS64":
		return value == "hardfloat" || value == "softfloat"
	case "GOPPC64":
		return value == "power8" || value == "power9" || value == "power10"
	case "GORISCV64":
		return value == "rva20u64" || value == "rva22u64" || value == "rva23u64"
	case "GOWASM":
		return value == "satconv" || value == "signext" || value == "satconv,signext" || value == "signext,satconv"
	default:
		return false
	}
}

func (l *Lock) validatePluginsAndGraph() error {
	pluginIDs := map[string]bool{}
	pluginNames := map[string]bool{}
	devPlugins := map[string]bool{}
	modules := map[string]LockedModule{}
	previousModule := ""
	for i, item := range l.Modules {
		key := item.Path + "\x00" + item.Version
		if key <= previousModule || modules[item.Path].Path != "" {
			return &Error{Code: "INGOT-LOCK-MODULE-ORDER", Field: fmt.Sprintf("modules[%d]", i), Actual: item.Path + "@" + item.Version, Want: "unique (path, version) sorted graph"}
		}
		if err := module.Check(item.Path, item.Version); err != nil || module.CanonicalVersion(item.Version) != item.Version || !validModuleSum(item.Sum, true) || !validModuleSum(item.GoModSum, false) {
			return &Error{Code: "INGOT-LOCK-MODULE", Field: fmt.Sprintf("modules[%d]", i), Actual: item.Path + "@" + item.Version, Err: err}
		}
		modules[item.Path] = item
		previousModule = key
	}
	if sdkModule, ok := modules[l.SDK.ModulePath]; !ok || sdkModule.Version != l.SDK.Version {
		return &Error{Code: "INGOT-LOCK-SDK-GRAPH", Field: "sdk", Want: l.SDK.ModulePath + "@" + l.SDK.Version}
	}
	for i, plugin := range l.Plugins {
		field := fmt.Sprintf("plugins[%d]", i)
		if err := module.CheckPath(plugin.ID); err != nil {
			return &Error{Code: "INGOT-LOCK-PLUGIN-ID", Field: field + ".id", Actual: plugin.ID, Err: err}
		}
		if pluginIDs[plugin.ID] || pluginNames[plugin.Name] {
			return &Error{Code: "INGOT-LOCK-DUPLICATE-PLUGIN", Field: field, Actual: plugin.ID + "/" + plugin.Name}
		}
		pluginIDs[plugin.ID], pluginNames[plugin.Name] = true, true
		if err := validateShortName(plugin.Name); err != nil {
			return &Error{Code: "INGOT-LOCK-PLUGIN-NAME", Field: field + ".name", Actual: plugin.Name, Err: err}
		}
		if !digestPattern.MatchString(plugin.ManifestDigest) || validatePackagePath(plugin.RootPackage) != nil {
			return &Error{Code: "INGOT-LOCK-PLUGIN-MANIFEST", Field: field, Actual: plugin.ManifestDigest + "/" + plugin.RootPackage}
		}
		if len(plugin.Components) == 0 {
			return &Error{Code: "INGOT-LOCK-PLUGIN-COMPONENTS", Field: field + ".components", Want: "non-empty"}
		}
		componentNames, componentPackages := map[string]bool{}, map[string]bool{}
		for j, component := range plugin.Components {
			if validateShortName(component.Name) != nil || validatePackagePath(component.Package) != nil || componentNames[component.Name] || componentPackages[component.Package] {
				return &Error{Code: "INGOT-LOCK-COMPONENT", Field: fmt.Sprintf("%s.components[%d]", field, j), Actual: component.Name + "/" + component.Package}
			}
			componentNames[component.Name], componentPackages[component.Package] = true, true
		}
		stateValid := (!plugin.HasState && plugin.StateSchemaVersion == 0 && plugin.StateMinReaderVersion == 0) ||
			(plugin.HasState && plugin.StateSchemaVersion > 0 && plugin.StateMinReaderVersion > 0 && plugin.StateMinReaderVersion <= plugin.StateSchemaVersion)
		if !stateValid {
			return &Error{Code: "INGOT-LOCK-STATE", Field: field + ".state"}
		}
		switch plugin.SourceKind {
		case "module":
			selected, ok := modules[plugin.ID]
			if module.Check(plugin.ID, plugin.Version) != nil || module.CanonicalVersion(plugin.Version) != plugin.Version || !validModuleSum(plugin.ModuleSum, false) || !ok || selected.Version != plugin.Version || selected.Sum != plugin.ModuleSum {
				return &Error{Code: "INGOT-LOCK-REMOTE-SOURCE", Field: field, Actual: plugin.ID + "@" + plugin.Version}
			}
		case "dev":
			if plugin.Version != "" || plugin.ModuleSum != "" {
				return &Error{Code: "INGOT-LOCK-DEV-SOURCE", Field: field, Want: "version and module_sum absent"}
			}
			devPlugins[plugin.ID] = true
		default:
			return &Error{Code: "INGOT-LOCK-SOURCE-KIND", Field: field + ".source_kind", Actual: plugin.SourceKind}
		}
	}
	replacements := map[string]bool{}
	previousReplacement := ""
	for i, replacement := range l.Replacements {
		if replacement.ModulePath <= previousReplacement || replacements[replacement.ModulePath] || !devPlugins[replacement.ModulePath] || !digestPattern.MatchString(replacement.ContentSHA256) || !filepath.IsAbs(replacement.DevPath) || filepath.Clean(replacement.DevPath) != replacement.DevPath {
			return &Error{Code: "INGOT-LOCK-REPLACEMENT", Field: fmt.Sprintf("replacements[%d]", i), Actual: replacement.ModulePath}
		}
		expected, err := SyntheticVersion(replacement.ModulePath)
		if err != nil || expected != replacement.SyntheticVersion {
			return &Error{Code: "INGOT-LOCK-SYNTHETIC-VERSION", Field: fmt.Sprintf("replacements[%d].synthetic_version", i), Want: expected, Actual: replacement.SyntheticVersion, Err: err}
		}
		replacements[replacement.ModulePath] = true
		previousReplacement = replacement.ModulePath
	}
	for plugin := range devPlugins {
		if !replacements[plugin] {
			return &Error{Code: "INGOT-LOCK-MISSING-REPLACEMENT", Plugin: plugin}
		}
	}
	return nil
}

func validModuleSum(value string, emptyAllowed bool) bool {
	if value == "" {
		return emptyAllowed
	}
	if !strings.HasPrefix(value, "h1:") {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, "h1:"))
	return err == nil && len(decoded) == sha256.Size
}

type buildManifest struct {
	SchemaVersion  int                    `json:"schema_version"`
	IngotVersion   string                 `json:"ingot_version"`
	BuilderVersion string                 `json:"builder_version"`
	SDK            SDKLock                `json:"sdk"`
	Toolchain      buildManifestToolchain `json:"toolchain"`
	Target         buildManifestTarget    `json:"target"`
	Environment    EnvironmentLock        `json:"environment"`
	Build          BuildLock              `json:"build"`
	Plugins        []buildManifestPlugin  `json:"plugins"`
	Modules        []LockedModule         `json:"modules"`
	Replacements   []buildManifestReplace `json:"replacements"`
	Bindings       []any                  `json:"bindings"`
}

type buildManifestToolchain struct {
	GoVersion string `json:"go_version"`
}
type buildManifestTarget struct {
	GOOS         string            `json:"goos"`
	GOARCH       string            `json:"goarch"`
	Tuning       map[string]string `json:"tuning"`
	GOExperiment []string          `json:"goexperiment"`
	CGOEnabled   bool              `json:"cgo_enabled"`
}
type buildManifestPlugin struct {
	ID             string              `json:"id"`
	Name           string              `json:"name"`
	Source         buildManifestSource `json:"source"`
	ManifestDigest string              `json:"manifest_digest"`
	RootPackage    string              `json:"root_package"`
	State          manifestStateRecord `json:"state"`
	Components     []LockedComponent   `json:"components"`
}
type buildManifestSource struct {
	Kind          string `json:"kind"`
	Version       string `json:"version,omitempty"`
	ModuleSum     string `json:"module_sum,omitempty"`
	ContentSHA256 string `json:"content_sha256,omitempty"`
}
type buildManifestReplace struct {
	ModulePath    string `json:"module_path"`
	Kind          string `json:"kind"`
	ContentSHA256 string `json:"content_sha256"`
}

func (l *Lock) CanonicalBuildManifest() ([]byte, error) {
	if err := l.Validate(); err != nil {
		return nil, err
	}
	tuning := make(map[string]string, len(l.Target.Tuning))
	for _, item := range l.Target.Tuning {
		tuning[item.Key] = item.Value
	}
	manifest := buildManifest{
		SchemaVersion: 1, IngotVersion: l.IngotVersion, BuilderVersion: l.BuilderVersion, SDK: l.SDK,
		Toolchain:   buildManifestToolchain{GoVersion: l.Toolchain.Version},
		Target:      buildManifestTarget{GOOS: l.Target.GOOS, GOARCH: l.Target.GOARCH, Tuning: tuning, GOExperiment: append([]string{}, l.Target.GOExperiment...), CGOEnabled: l.Target.CGOEnabled},
		Environment: l.Environment,
		// Empty build flags are normalized to non-nil slices so the canonical
		// JSON is identical whether the lock was just created in memory
		// (nil) or parsed back from the TOML file (empty, non-nil). Without
		// this, ImageID is not stable across a lock round-trip.
		Build:   BuildLock{Trimpath: l.Build.Trimpath, BuildVCS: l.Build.BuildVCS, Tags: append([]string{}, l.Build.Tags...), LDFlags: append([]string{}, l.Build.LDFlags...), GCFlags: append([]string{}, l.Build.GCFlags...), ASMFlags: append([]string{}, l.Build.ASMFlags...)},
		Modules: append([]LockedModule{}, l.Modules...), Bindings: []any{},
		Plugins: make([]buildManifestPlugin, len(l.Plugins)), Replacements: make([]buildManifestReplace, len(l.Replacements)),
	}
	replacementByModule := map[string]Replacement{}
	for i, replacement := range l.Replacements {
		replacementByModule[replacement.ModulePath] = replacement
		manifest.Replacements[i] = buildManifestReplace{ModulePath: replacement.ModulePath, Kind: "dev", ContentSHA256: replacement.ContentSHA256}
	}
	for i, plugin := range l.Plugins {
		source := buildManifestSource{Kind: "module", Version: plugin.Version, ModuleSum: plugin.ModuleSum}
		if plugin.SourceKind == "dev" {
			source = buildManifestSource{Kind: "dev", ContentSHA256: replacementByModule[plugin.ID].ContentSHA256}
		}
		manifest.Plugins[i] = buildManifestPlugin{ID: plugin.ID, Name: plugin.Name, Source: source, ManifestDigest: plugin.ManifestDigest, RootPackage: plugin.RootPackage,
			State: manifestStateRecord{Present: plugin.HasState, SchemaVersion: plugin.StateSchemaVersion, MinReaderVersion: plugin.StateMinReaderVersion}, Components: append([]LockedComponent{}, plugin.Components...)}
	}
	return canonicalJSON(manifest)
}

func (l *Lock) ImageID() (string, error) {
	data, err := l.CanonicalBuildManifest()
	if err != nil {
		return "", err
	}
	return digestBytes(data), nil
}

// RestoreRootModule writes the Builder-owned root go.mod and go.sum.
//
// devTargets optionally maps a replaced module path to the locator written
// into its replace directive (an absolute path, or a relative path into the
// build staging area). Modules absent from the map keep their locked
// DevPath. Relative locators keep the compiled artifact free of
// machine-specific absolute paths.
func (l *Lock) RestoreRootModule(directory string, devTargets map[string]string) error {
	if err := l.Validate(); err != nil {
		return err
	}
	var goMod strings.Builder
	goMod.WriteString("module ingot.local/runtime-image\n\ngo ")
	goMod.WriteString(strings.TrimPrefix(l.Toolchain.Version, "go"))
	goMod.WriteString("\n\nrequire (\n")
	seen := map[string]bool{}
	replacements := map[string]Replacement{}
	for _, item := range l.Replacements {
		replacements[item.ModulePath] = item
	}
	for _, plugin := range l.Plugins {
		version := plugin.Version
		if plugin.SourceKind == "dev" {
			version = replacements[plugin.ID].SyntheticVersion
		}
		_, _ = fmt.Fprintf(&goMod, "\t%s %s\n", plugin.ID, version)
		seen[plugin.ID] = true
	}
	if !seen[l.SDK.ModulePath] {
		_, _ = fmt.Fprintf(&goMod, "\t%s %s\n", l.SDK.ModulePath, l.SDK.Version)
		seen[l.SDK.ModulePath] = true
	}
	// Go's pruned module graph requires the root to retain selected transitive
	// modules explicitly. Materializing every immutable locked node also makes
	// -mod=readonly enforce the exact selected graph before package loading.
	for _, item := range l.Modules {
		if !seen[item.Path] {
			_, _ = fmt.Fprintf(&goMod, "\t%s %s // indirect\n", item.Path, item.Version)
			seen[item.Path] = true
		}
	}
	goMod.WriteString(")\n")
	for _, replacement := range l.Replacements {
		locator := filepath.ToSlash(replacement.DevPath)
		if relative, ok := devTargets[replacement.ModulePath]; ok {
			locator = relative
		}
		_, _ = fmt.Fprintf(&goMod, "\nreplace %s => %s\n", replacement.ModulePath, goModQuote(locator))
	}
	parsed, err := modfile.Parse("go.mod", []byte(goMod.String()), nil)
	if err != nil {
		return fmt.Errorf("parse root go.mod: %w", err)
	}
	formatted := modfile.Format(parsed.Syntax)
	if err := os.WriteFile(filepath.Join(directory, "go.mod"), formatted, 0o644); err != nil {
		return err
	}
	var sums []string
	for _, item := range l.Modules {
		if item.Sum != "" {
			sums = append(sums, fmt.Sprintf("%s %s %s", item.Path, item.Version, item.Sum))
		}
		sums = append(sums, fmt.Sprintf("%s %s/go.mod %s", item.Path, item.Version, item.GoModSum))
	}
	sort.Strings(sums)
	return os.WriteFile(filepath.Join(directory, "go.sum"), []byte(strings.Join(sums, "\n")+"\n"), 0o644)
}

func goModQuote(value string) string { return strconv.Quote(value) }

func (l *Lock) MarshalTOML() ([]byte, error) {
	if err := l.Validate(); err != nil {
		return nil, err
	}
	return writeTOML(l)
}
