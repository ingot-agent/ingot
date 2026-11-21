package builder

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/mod/modfile"
)

// MinimumGeneratedGoVersion is the oldest Go language version the Builder's
// generated wiring is written for. It is only a lower bound: any real
// composition raises the generated go.mod line through the go directives of the
// modules it locks.
//
// Raise this constant when generated code starts using a newer language
// feature. Do not raise it to match the toolchain that compiled the Builder:
// the Builder's own Go version is not a property of the generated module, and
// coupling the two is what made a plugin that needs a newer Go unusable with an
// older official Core binary.
const MinimumGeneratedGoVersion = "go1.21.0"

// goDirectivePattern accepts a go.mod go directive ("1.24.2", "1.24") and a
// toolchain version ("go1.24.2"). Release candidates, beta releases, devel
// toolchains and Go 2 are rejected: the lock records only release versions.
var goDirectivePattern = regexp.MustCompile(`^(?:go)?1\.([0-9]+)(?:\.([0-9]+))?$`)

// parseGoVersion splits a release version into its minor and patch numbers. A
// missing patch defaults to zero, matching the go.mod directive rules.
func parseGoVersion(value string) (int, int, bool) {
	match := goDirectivePattern.FindStringSubmatch(strings.TrimSpace(value))
	if match == nil {
		return 0, 0, false
	}
	minor, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, 0, false
	}
	patch := 0
	if match[2] != "" {
		if patch, err = strconv.Atoi(match[2]); err != nil {
			return 0, 0, false
		}
	}
	return minor, patch, true
}

// normalizeGoVersion rewrites a go directive or toolchain version into the
// canonical go1.x.y form the lock stores.
func normalizeGoVersion(value string) (string, bool) {
	minor, patch, ok := parseGoVersion(value)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("go1.%d.%d", minor, patch), true
}

// compareGoVersions compares two canonical go1.x.y versions and returns -1, 0
// or 1. Both arguments must already be canonical: callers validate the lock
// field and the probed toolchain before comparing them.
func compareGoVersions(left, right string) int {
	leftMinor, leftPatch, _ := parseGoVersion(left)
	rightMinor, rightPatch, _ := parseGoVersion(right)
	switch {
	case leftMinor < rightMinor || (leftMinor == rightMinor && leftPatch < rightPatch):
		return -1
	case leftMinor > rightMinor || (leftMinor == rightMinor && leftPatch > rightPatch):
		return 1
	default:
		return 0
	}
}

// goRequirement is the minimum Go language version a generated module needs and
// the input that raised it. An empty Source means the Builder floor won.
type goRequirement struct {
	Version string
	Source  string
}

// requiredGoVersion returns the minimum Go version needed to build the
// generated root module. The lock records the aggregate, but it can also be
// derived from the locked modules when each one records its own go directive.
// recorded reports whether the result came from real module data rather than
// the Builder floor alone.
func requiredGoVersion(modules []LockedModule) (goRequirement, bool) {
	requirement := goRequirement{Version: MinimumGeneratedGoVersion}
	recorded := false
	for _, item := range modules {
		version, ok := normalizeGoVersion(item.GoVersion)
		if !ok {
			continue
		}
		recorded = true
		if compareGoVersions(version, requirement.Version) > 0 {
			requirement = goRequirement{Version: version, Source: item.Path + "@" + item.Version}
		}
	}
	return requirement, recorded
}

// probeGoToolchain reports the version of the go command that Builder
// subprocesses execute. The probe uses the same offline, no-auto-switch
// environment as a locked build, so it cannot report a toolchain the build
// itself would be forbidden to download. Only release toolchains are accepted.
func probeGoToolchain(ctx context.Context) (string, error) {
	environment := replaceEnvironment(os.Environ(), map[string]string{"GOWORK": "off", "GOTOOLCHAIN": "local"})
	output, err := runGo(ctx, "", environment, "env", "GOVERSION")
	if err != nil {
		return "", err
	}
	reported := strings.TrimSpace(string(output))
	version, ok := normalizeGoVersion(reported)
	if !ok {
		return "", &Error{Code: "INGOT-BUILD-TOOLCHAIN", Field: "go", Want: "a go1.x.y release toolchain on PATH", Actual: reported}
	}
	return version, nil
}

// requireGoToolchain reports whether the probed toolchain can build a module
// that declares the given language requirement.
func requireGoToolchain(toolchain string, requirement goRequirement) error {
	if compareGoVersions(toolchain, requirement.Version) >= 0 {
		return nil
	}
	want := "go >= " + requirement.Version
	if requirement.Source != "" {
		want += " raised by " + requirement.Source
	}
	return &Error{Code: "INGOT-BUILD-TOOLCHAIN", Want: want, Actual: toolchain, Err: fmt.Errorf("the selected module graph uses a newer Go language version than the toolchain on PATH")}
}

// sourceGoRequirement reads the go directive of a local module that will be
// built as part of the generated runtime.
//
// Go raises a module's own go directive to the maximum demanded by its
// dependencies, so this directive is the effective language requirement of the
// plugin as a whole, not merely its hand-written line. Reading it before the
// resolver runs means a mismatched toolchain is reported against the plugin that
// needs it instead of surfacing as a module download failure.
func sourceGoRequirement(directory, modulePath string) (goRequirement, error) {
	data, err := os.ReadFile(filepath.Join(directory, "go.mod"))
	if err != nil {
		return goRequirement{}, &Error{Code: "INGOT-MODULE-GOMOD", Path: filepath.Join(directory, "go.mod"), Err: err}
	}
	parsed, err := modfile.Parse("go.mod", data, nil)
	if err != nil {
		return goRequirement{}, &Error{Code: "INGOT-MODULE-GOMOD", Path: filepath.Join(directory, "go.mod"), Err: err}
	}
	requirement := goRequirement{Version: MinimumGeneratedGoVersion}
	if parsed.Go == nil {
		return requirement, nil
	}
	version, ok := normalizeGoVersion(parsed.Go.Version)
	if !ok {
		return goRequirement{}, &Error{Code: "INGOT-LOCK-TOOLCHAIN", Field: "go", Plugin: modulePath, Actual: parsed.Go.Version, Want: "a go.mod language version such as 1.24.0"}
	}
	if compareGoVersions(version, requirement.Version) > 0 {
		requirement = goRequirement{Version: version, Source: modulePath}
	}
	return requirement, nil
}
