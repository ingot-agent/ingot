package builder

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestInspectExportDestination(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	if exists, _, err := inspectExportDestination(missing); err != nil || exists {
		t.Fatalf("missing destination = %t, %v", exists, err)
	}

	empty := filepath.Join(root, "empty")
	if err := os.Mkdir(empty, 0o750); err != nil {
		t.Fatal(err)
	}
	if exists, mode, err := inspectExportDestination(empty); err != nil || !exists || mode != 0o750 {
		t.Fatalf("empty destination = %t, %o, %v", exists, mode, err)
	}

	writeTestFile(t, filepath.Join(empty, "keep.txt"), "keep")
	if _, _, err := inspectExportDestination(empty); err == nil || !hasBuilderErrorCode(err, "INGOT-GENERATE-OUTPUT-EXISTS") {
		t.Fatalf("non-empty destination error = %v", err)
	}

	file := filepath.Join(root, "file")
	writeTestFile(t, file, "value")
	if _, _, err := inspectExportDestination(file); err == nil || !hasBuilderErrorCode(err, "INGOT-GENERATE-OUTPUT-EXISTS") {
		t.Fatalf("file destination error = %v", err)
	}
}

func TestValidateExportOverlapRejectsReplacementSource(t *testing.T) {
	source := t.TempDir()
	lock := &Lock{Replacements: []Replacement{{ModulePath: "example.com/plugin", DevPath: source}}}
	output := filepath.Join(source, "generated", "runtime")
	if err := validateExportOverlap(output, lock); err == nil || !hasBuilderErrorCode(err, "INGOT-GENERATE-OUTPUT-OVERLAP") {
		t.Fatalf("overlap error = %v", err)
	}
	if err := validateExportOverlap(filepath.Join(t.TempDir(), "runtime"), lock); err != nil {
		t.Fatalf("non-overlap rejected: %v", err)
	}
}

func TestPublishExportDirectoryReplacesEmptyDestination(t *testing.T) {
	root := t.TempDir()
	staging := filepath.Join(root, "staging")
	destination := filepath.Join(root, "runtime")
	if err := os.Mkdir(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(destination, 0o750); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(staging, "main.go"), "package main\n")
	if err := publishExportDirectory(staging, destination); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(destination, "main.go")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Fatalf("staging still exists: %v", err)
	}
}

func hasBuilderErrorCode(err error, code string) bool {
	var builderErr *Error
	return errors.As(err, &builderErr) && builderErr.Code == code
}
