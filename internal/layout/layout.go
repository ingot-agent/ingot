// Package layout defines the on-disk names shared by the image builder and
// the user-facing home package.
package layout

import "strings"

const runtimeBaseName = "ingot-runtime"

// RuntimeExecutableName returns the platform-native runtime filename.
func RuntimeExecutableName(goos string) string {
	if goos == "windows" {
		return runtimeBaseName + ".exe"
	}
	return runtimeBaseName
}

// ImageDirectoryName maps a logical image ID to a platform-safe directory
// name. Windows forbids ':' in a path component, so its on-disk spelling uses
// a dash while manifests and current pointers retain the canonical image ID.
func ImageDirectoryName(imageID, goos string) string {
	if goos == "windows" && strings.HasPrefix(imageID, "sha256:") {
		return "sha256-" + strings.TrimPrefix(imageID, "sha256:")
	}
	return imageID
}

// ImageIDFromDirectoryName reverses ImageDirectoryName for directory scans.
func ImageIDFromDirectoryName(name, goos string) string {
	if goos == "windows" && strings.HasPrefix(name, "sha256-") {
		return "sha256:" + strings.TrimPrefix(name, "sha256-")
	}
	return name
}
