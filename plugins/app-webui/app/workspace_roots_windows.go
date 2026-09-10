//go:build windows

package appcomponent

import (
	"fmt"
	"path/filepath"
	"strings"
	"syscall"

	appbackend "github.com/ingot-agent/app-webui"
)

var getLogicalDrives = syscall.NewLazyDLL("kernel32.dll").NewProc("GetLogicalDrives")

func workspaceRootEntries(current string) []appbackend.WorkspaceBrowseEntry {
	mask, _, _ := getLogicalDrives.Call()
	if mask == 0 {
		return nil
	}
	return logicalDriveRoots(uint32(mask), filepath.VolumeName(current))
}

func logicalDriveRoots(mask uint32, currentVolume string) []appbackend.WorkspaceBrowseEntry {
	var roots []appbackend.WorkspaceBrowseEntry
	for index := 0; index < 26; index++ {
		if mask&(1<<index) == 0 {
			continue
		}
		root := fmt.Sprintf("%c:%c", 'A'+index, filepath.Separator)
		if strings.EqualFold(filepath.VolumeName(root), currentVolume) {
			continue
		}
		roots = append(roots, appbackend.WorkspaceBrowseEntry{Name: root, Path: root})
	}
	return roots
}
