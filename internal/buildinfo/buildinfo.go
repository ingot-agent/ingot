// Package buildinfo exposes the identity of the current core binary.
package buildinfo

import (
	"runtime"
	"runtime/debug"
	"strconv"
)

// These values are overridden by the release workflow through -ldflags -X.
var (
	CoreVersion = "0.3.2-dev"
	Official    = "false"
	Revision    = ""
	Modified    = ""
)

// Info is the structured identity reported by `ingot version`.
type Info struct {
	CoreVersion string `json:"core_version"`
	Official    bool   `json:"official"`
	Revision    string `json:"revision,omitempty"`
	Modified    bool   `json:"modified"`
	GoVersion   string `json:"go_version"`
	Target      string `json:"target"`
}

// Current returns release metadata together with VCS facts embedded by Go.
func Current() Info {
	revision, modified := Revision, parseBool(Modified)
	if build, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range build.Settings {
			switch setting.Key {
			case "vcs.revision":
				if revision == "" {
					revision = setting.Value
				}
			case "vcs.modified":
				if Modified == "" {
					modified = parseBool(setting.Value)
				}
			}
		}
	}
	return Info{
		CoreVersion: CoreVersion,
		Official:    parseBool(Official),
		Revision:    revision,
		Modified:    modified,
		GoVersion:   runtime.Version(),
		Target:      runtime.GOOS + "/" + runtime.GOARCH,
	}
}

func parseBool(value string) bool {
	result, _ := strconv.ParseBool(value)
	return result
}
