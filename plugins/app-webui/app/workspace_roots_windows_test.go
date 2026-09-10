//go:build windows

package appcomponent

import (
	"reflect"
	"testing"

	appbackend "github.com/ingot-agent/app-webui"
)

func TestLogicalDriveRootsOffersOtherWindowsVolumes(t *testing.T) {
	mask := uint32(1<<('C'-'A') | 1<<('D'-'A') | 1<<('Z'-'A'))
	want := []appbackend.WorkspaceBrowseEntry{
		{Name: `D:\`, Path: `D:\`},
		{Name: `Z:\`, Path: `Z:\`},
	}
	if got := logicalDriveRoots(mask, "c:"); !reflect.DeepEqual(got, want) {
		t.Fatalf("logicalDriveRoots() = %#v, want %#v", got, want)
	}
}
