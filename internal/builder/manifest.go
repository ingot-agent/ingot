package builder

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/mod/semver"
)

var (
	shortNamePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)
	semverPattern    = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)
)

// Manifest is the semantic form of ingot.plugin.toml v1.
type Manifest struct {
	ManifestVersion int                 `toml:"manifest_version"`
	Name            string              `toml:"name"`
	Ingot           string              `toml:"ingot"`
	ConfigPackage   string              `toml:"config_package"`
	Components      []ManifestComponent `toml:"components"`
	State           *ManifestState      `toml:"state,omitempty"`
	Meta            *ManifestMeta       `toml:"meta,omitempty" json:"-"`
	filePath        string
}

type ManifestComponent struct {
	Name    string `toml:"name" json:"name"`
	Package string `toml:"package" json:"package"`
}

type ManifestState struct {
	SchemaVersion    int `toml:"schema_version"`
	MinReaderVersion int `toml:"min_reader_version"`
}

type ManifestMeta struct {
	DisplayName string `toml:"display_name,omitempty"`
	Description string `toml:"description,omitempty"`
	Homepage    string `toml:"homepage,omitempty"`
	Repository  string `toml:"repository,omitempty"`
	License     string `toml:"license,omitempty"`
}

// ParseManifest strictly parses an ingot.plugin.toml file and validates all
// rules that do not require loading Go packages.
func ParseManifest(filePath string) (*Manifest, error) {
	var manifest Manifest
	if err := decodeExactFile(filePath, &manifest, "INGOT-MANIFEST-PARSE"); err != nil {
		return nil, err
	}
	manifest.filePath = filePath
	if err := manifest.Validate(); err != nil {
		if diagnosticErr, ok := err.(*Error); ok && diagnosticErr.Path == "" {
			diagnosticErr.Path = filePath
		}
		return nil, err
	}
	return &manifest, nil
}

func (m *Manifest) Validate() error {
	if m.ManifestVersion != 1 {
		return &Error{Code: "INGOT-MANIFEST-UNSUPPORTED-VERSION", Field: "manifest_version", Want: "1", Actual: fmt.Sprint(m.ManifestVersion)}
	}
	if err := validateShortName(m.Name); err != nil {
		return &Error{Code: "INGOT-MANIFEST-NAME", Field: "name", Actual: m.Name, Err: err}
	}
	rangeValue, err := ParseVersionRange(m.Ingot)
	if err != nil {
		return &Error{Code: "INGOT-MANIFEST-INGOT-RANGE", Field: "ingot", Actual: m.Ingot, Err: err}
	}
	m.Ingot = rangeValue.String()
	if err := validatePackagePath(m.ConfigPackage); err != nil {
		return &Error{Code: "INGOT-MANIFEST-PACKAGE", Field: "config_package", Actual: m.ConfigPackage, Err: err}
	}
	if len(m.Components) == 0 {
		return &Error{Code: "INGOT-MANIFEST-COMPONENTS", Field: "components", Want: "at least one component", Actual: "empty"}
	}
	names := make(map[string]int, len(m.Components))
	packages := make(map[string]int, len(m.Components))
	for i := range m.Components {
		component := &m.Components[i]
		field := fmt.Sprintf("components[%d]", i)
		if err := validateShortName(component.Name); err != nil {
			return &Error{Code: "INGOT-MANIFEST-COMPONENT-NAME", Field: field + ".name", Actual: component.Name, Err: err}
		}
		if previous, ok := names[component.Name]; ok {
			return &Error{Code: "INGOT-MANIFEST-DUPLICATE-COMPONENT", Field: field + ".name", Actual: component.Name, Want: fmt.Sprintf("unique (first at components[%d])", previous)}
		}
		names[component.Name] = i
		if err := validatePackagePath(component.Package); err != nil {
			return &Error{Code: "INGOT-MANIFEST-COMPONENT-PACKAGE", Field: field + ".package", Actual: component.Package, Err: err}
		}
		if previous, ok := packages[component.Package]; ok {
			return &Error{Code: "INGOT-MANIFEST-DUPLICATE-PACKAGE", Field: field + ".package", Actual: component.Package, Want: fmt.Sprintf("unique (first at components[%d])", previous)}
		}
		packages[component.Package] = i
	}
	if m.State != nil {
		if m.State.SchemaVersion <= 0 || m.State.MinReaderVersion <= 0 {
			return &Error{Code: "INGOT-MANIFEST-STATE", Field: "state", Want: "positive schema_version and min_reader_version"}
		}
		if m.State.MinReaderVersion > m.State.SchemaVersion {
			return &Error{Code: "INGOT-MANIFEST-STATE-WINDOW", Field: "state.min_reader_version", Want: "<= state.schema_version", Actual: strconv.Itoa(m.State.MinReaderVersion)}
		}
	}
	return nil
}

func validateShortName(value string) error {
	if len(value) < 1 || len(value) > 64 || !shortNamePattern.MatchString(value) {
		return fmt.Errorf("must match [a-z][a-z0-9]*(?:[._-][a-z0-9]+)* and contain 1-64 bytes")
	}
	return nil
}

func validatePackagePath(value string) error {
	if value == "." {
		return nil
	}
	if !strings.HasPrefix(value, "./") || strings.HasSuffix(value, "/") || strings.Contains(value, "//") || strings.Contains(value, `\`) {
		return fmt.Errorf("must be '.' or a canonical './segment' path")
	}
	for _, segment := range strings.Split(strings.TrimPrefix(value, "./"), "/") {
		if segment == "" || segment == "." || segment == ".." || segment == "internal" {
			return fmt.Errorf("invalid path segment %q", segment)
		}
	}
	return nil
}

func validatePackageBoundary(moduleRoot, relative string) error {
	joined := moduleRoot
	if relative != "." {
		joined = filepath.Join(moduleRoot, filepath.FromSlash(strings.TrimPrefix(relative, "./")))
	}
	resolvedRoot, err := filepath.EvalSymlinks(moduleRoot)
	if err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(resolvedRoot, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("package resolves outside module root")
	}
	return nil
}

type manifestProjection struct {
	SchemaVersion   int                 `json:"schema_version"`
	ManifestVersion int                 `json:"manifest_version"`
	Name            string              `json:"name"`
	Ingot           string              `json:"ingot"`
	ConfigPackage   string              `json:"config_package"`
	State           manifestStateRecord `json:"state"`
	Components      []ManifestComponent `json:"components"`
}

type manifestStateRecord struct {
	Present          bool `json:"present"`
	SchemaVersion    int  `json:"schema_version"`
	MinReaderVersion int  `json:"min_reader_version"`
}

func (m *Manifest) stateRecord() manifestStateRecord {
	if m.State == nil {
		return manifestStateRecord{}
	}
	return manifestStateRecord{Present: true, SchemaVersion: m.State.SchemaVersion, MinReaderVersion: m.State.MinReaderVersion}
}

// CanonicalJSON returns ManifestBuildProjectionV1 in RFC 8785 form.
func (m *Manifest) CanonicalJSON() ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return canonicalJSON(manifestProjection{
		SchemaVersion: 1, ManifestVersion: 1, Name: m.Name, Ingot: m.Ingot,
		ConfigPackage: m.ConfigPackage, State: m.stateRecord(),
		Components: append([]ManifestComponent(nil), m.Components...),
	})
}

func (m *Manifest) Digest() (string, error) {
	data, err := m.CanonicalJSON()
	if err != nil {
		return "", err
	}
	return digestBytes(data), nil
}

// VersionRange implements the v0.1 space-separated AND comparator grammar.
type VersionRange struct{ comparators []versionComparator }

type versionComparator struct {
	op      string
	version string
}

var comparatorRank = map[string]int{"=": 0, ">": 1, ">=": 2, "<": 3, "<=": 4}

func ParseVersionRange(value string) (VersionRange, error) {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return VersionRange{}, fmt.Errorf("range is empty")
	}
	seen := make(map[string]bool, len(fields))
	comparators := make([]versionComparator, 0, len(fields))
	for _, field := range fields {
		op := "="
		version := field
		for _, candidate := range []string{">=", "<=", ">", "<", "="} {
			if strings.HasPrefix(field, candidate) {
				op = candidate
				version = strings.TrimPrefix(field, candidate)
				break
			}
		}
		if !semverPattern.MatchString(version) || !semver.IsValid("v"+version) {
			return VersionRange{}, fmt.Errorf("invalid canonical SemVer %q", version)
		}
		key := op + version
		if !seen[key] {
			comparators = append(comparators, versionComparator{op: op, version: version})
			seen[key] = true
		}
	}
	sort.Slice(comparators, func(i, j int) bool {
		if comparatorRank[comparators[i].op] != comparatorRank[comparators[j].op] {
			return comparatorRank[comparators[i].op] < comparatorRank[comparators[j].op]
		}
		return comparators[i].version < comparators[j].version
	})
	return VersionRange{comparators: comparators}, nil
}

func (r VersionRange) String() string {
	parts := make([]string, len(r.comparators))
	for i, comparator := range r.comparators {
		parts[i] = comparator.op + comparator.version
	}
	return strings.Join(parts, " ")
}

func (r VersionRange) Contains(version string) bool {
	version = strings.TrimPrefix(version, "v")
	if !semver.IsValid("v" + version) {
		return false
	}
	for _, comparator := range r.comparators {
		comparison := semver.Compare("v"+version, "v"+comparator.version)
		matches := comparison == 0
		switch comparator.op {
		case ">":
			matches = comparison > 0
		case ">=":
			matches = comparison >= 0
		case "<":
			matches = comparison < 0
		case "<=":
			matches = comparison <= 0
		}
		if !matches {
			return false
		}
	}
	return true
}
