package builder

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/tools/go/gcexportdata"
)

// loadedPackage is the part of a loaded package the graph analysis needs: its
// import path and the type information the compiler exported for it.
type loadedPackage struct {
	Path  string
	Name  string
	Types *types.Package
}

// listedPackage is the subset of `go list -json` output the loader consumes.
type listedPackage struct {
	ImportPath string
	Name       string
	Export     string
	DepOnly    bool
	Error      *struct{ Err string }
}

// loadPackages reads compiler export data for the requested import paths and
// their dependency closure.
//
// The Builder deliberately does not type-check plugin sources itself. go/types
// refuses a package whose go directive is newer than the release that compiled
// the running program, so in-process checking would make every Go upgrade a
// Core release: a plugin adopting a newer Go would be unbuildable by every
// existing Core binary. Instead the go command that will compile the image
// performs the checking and writes export data, and this function only decodes
// the exported type information. The plugin language version is therefore
// bounded by the toolchain on PATH, which the build already requires to satisfy
// the locked graph.
//
// The returned packages include compiler diagnostics for every package in the
// closure. Callers decide whether to report or ignore them.
func loadPackages(ctx context.Context, rootDirectory string, environment []string, buildFlags []string, imports []string) (map[string]*loadedPackage, []string, error) {
	arguments := []string{"list", "-e", "-export", "-deps", "-json"}
	arguments = append(arguments, buildFlags...)
	arguments = append(arguments, "--")
	arguments = append(arguments, imports...)
	command := exec.CommandContext(ctx, "go", arguments...)
	command.Dir = rootDirectory
	command.Env = environment
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		return nil, nil, &Error{Code: "INGOT-GO-COMMAND", Path: rootDirectory, Actual: "go " + strings.Join(arguments, " "), Err: fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))}
	}
	// go list reports compile failures of -export targets only on stderr, so the
	// text is required to explain why a package produced no export data. Drop
	// the progress lines the go command interleaves with real diagnostics.
	diagnostics := nonProgressOutput(stderr.String())
	packages := make(map[string]*loadedPackage, len(imports)+16)
	// The import map is shared by every decoded package so that references to
	// the same import path resolve to one types.Package. types.Identical only
	// reports capability identity when both sides name the same objects.
	typesByPath := make(map[string]*types.Package, len(imports)+16)
	files := token.NewFileSet()
	decoder := json.NewDecoder(&stdout)
	for {
		var listed listedPackage
		if err := decoder.Decode(&listed); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, nil, &Error{Code: "INGOT-PACKAGES-ERROR", Path: rootDirectory, Err: fmt.Errorf("decode go list output: %w", err)}
		}
		if listed.Error != nil && strings.TrimSpace(listed.Error.Err) != "" {
			diagnostics = append(diagnostics, listed.Error.Err)
			continue
		}
		if listed.Export == "" {
			continue
		}
		pkg, err := readExportData(listed.Export, files, typesByPath, listed.ImportPath)
		if err != nil {
			return nil, nil, &Error{Code: "INGOT-PACKAGES-EXPORT", Path: listed.Export, Field: listed.ImportPath, Err: err}
		}
		typesByPath[listed.ImportPath] = pkg
		packages[listed.ImportPath] = &loadedPackage{Path: listed.ImportPath, Name: listed.Name, Types: pkg}
	}
	return packages, diagnostics, nil
}

// readExportData decodes one package's compiler export data. A failure here
// does not mean the plugin is broken: the compiler already accepted it, so the
// remaining cause is that this Core's export-data reader predates the format.
func readExportData(path string, files *token.FileSet, imports map[string]*types.Package, importPath string) (*types.Package, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader, err := gcexportdata.NewReader(file)
	if err != nil {
		return nil, err
	}
	pkg, err := gcexportdata.Read(reader, files, imports, importPath)
	if err != nil {
		return nil, fmt.Errorf("this Core cannot decode export data written by the selected Go toolchain; upgrading the Core adds support for newer Go releases: %w", err)
	}
	return pkg, nil
}

// nonProgressOutput removes go command progress lines so only diagnostics from
// module resolution and compilation remain.
func nonProgressOutput(output string) []string {
	var diagnostics []string
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "go: downloading ") || strings.HasPrefix(trimmed, "go: extracting ") {
			continue
		}
		diagnostics = append(diagnostics, trimmed)
	}
	if len(diagnostics) == 0 {
		return nil
	}
	return []string{strings.Join(diagnostics, "\n")}
}
