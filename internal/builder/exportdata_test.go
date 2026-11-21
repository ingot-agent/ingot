package builder

import (
	"fmt"
	"go/types"
	"os"
	"path/filepath"
	"testing"
)

// archiveWithExportData wraps raw export data in the archive layout that
// gcexportdata.NewReader expects: an ar(1) archive whose first member is
// __.PKGDEF containing an object header, a "$$B" section header, the payload and
// an end-of-section marker.
func archiveWithExportData(t *testing.T, objectHeader string, payload []byte) string {
	t.Helper()
	section := append([]byte(objectHeader+"\n"), []byte("\n$$B\n")...)
	section = append(section, payload...)
	section = append(section, []byte("\n$$\n")...)
	header := fmt.Sprintf("%-16s%-12s%-6s%-6s%-8s%-10d`\n", "__.PKGDEF/", "0", "0", "0", "644", len(section))
	if len(header) != 60 {
		t.Fatalf("archive header is %d bytes", len(header))
	}
	path := filepath.Join(t.TempDir(), "fake.a")
	body := append([]byte("!<arch>\n"), append([]byte(header), section...)...)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestReadExportDataRejectsFutureFormat verifies that export data written by an
// unknown, newer Go release produces an error instead of a crash. The unified
// bitstream begins with a format version; a decoder that predates it must
// refuse the stream, and that refusal must not escape as a panic.
func TestReadExportDataRejectsFutureFormat(t *testing.T) {
	future := []byte{'u', 99, 0, 0, 0}
	future = append(future, make([]byte, 8192)...)
	path := archiveWithExportData(t, "go object linux amd64 go1.99", future)
	var err error
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				t.Fatalf("panic escaped readExportData: %v", recovered)
			}
		}()
		_, err = readExportData(path, nil, map[string]*types.Package{}, "example.com/plugin")
	}()
	if err == nil {
		t.Fatal("future export data version was accepted")
	}
	fmt.Println("error:", err)
}
