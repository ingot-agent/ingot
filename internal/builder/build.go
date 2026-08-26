package builder

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

type BuildOptions struct {
	Home         string
	ConfigPath   string
	GOMODCACHE   string
	CheckTimeout time.Duration
}

type BuildResult struct {
	ImageID                string
	ArtifactDigest         string
	ImageDirectory         string
	BinaryPath             string
	ComponentCreationOrder []string
	ManyOrder              map[string][]string
}

type ImageManifest struct {
	SchemaVersion          int                 `json:"schema_version"`
	ImageID                string              `json:"image_id"`
	ArtifactDigest         string              `json:"artifact_digest"`
	BuildManifest          json.RawMessage     `json:"build_manifest"`
	DirectPlugins          []string            `json:"direct_plugins"`
	ComponentCreationOrder []string            `json:"component_creation_order"`
	ManyOrder              map[string][]string `json:"many_order"`
}

func (options BuildOptions) defaults() (BuildOptions, error) {
	if options.Home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return options, err
		}
		options.Home = filepath.Join(userHome, ".ingot")
	}
	absolute, err := filepath.Abs(options.Home)
	if err != nil {
		return options, err
	}
	options.Home = filepath.Clean(absolute)
	if options.ConfigPath == "" {
		options.ConfigPath = filepath.Join(options.Home, "config.toml")
	}
	if options.GOMODCACHE == "" {
		options.GOMODCACHE = filepath.Join(options.Home, "cache", "gomod")
	}
	if options.CheckTimeout == 0 {
		options.CheckTimeout = 30 * time.Second
	}
	return options, nil
}

// Build performs the normal offline, readonly locked build, runs the generated
// runtime's pre-switch check, and atomically commits an immutable image. It does
// not change the current pointer.
func Build(ctx context.Context, desired *DesiredPlugins, lock *Lock, options BuildOptions) (*BuildResult, error) {
	if err := desired.Validate(); err != nil {
		return nil, err
	}
	if err := lock.Validate(); err != nil {
		return nil, err
	}
	digest, err := desired.Digest()
	if err != nil {
		return nil, err
	}
	if digest != lock.PluginsDigest {
		return nil, &Error{Code: "INGOT-BUILD-DESIRED-DRIFT", Field: "plugins_digest", Want: lock.PluginsDigest, Actual: digest}
	}
	options, err = options.defaults()
	if err != nil {
		return nil, err
	}
	if lock.Target.GOOS != runtime.GOOS || lock.Target.GOARCH != runtime.GOARCH {
		return nil, &Error{Code: "INGOT-BUILD-CHECK-TARGET", Want: runtime.GOOS + "/" + runtime.GOARCH, Actual: lock.Target.GOOS + "/" + lock.Target.GOARCH}
	}
	if runtime.Version() != lock.Toolchain.Version {
		return nil, &Error{Code: "INGOT-BUILD-TOOLCHAIN", Want: lock.Toolchain.Version, Actual: runtime.Version()}
	}
	imageID, err := lock.ImageID()
	if err != nil {
		return nil, err
	}
	imagesDirectory := filepath.Join(options.Home, "images")
	if err := os.MkdirAll(imagesDirectory, 0o700); err != nil {
		return nil, err
	}
	finalDirectory := filepath.Join(imagesDirectory, imageID)
	expectedBuildManifest, err := lock.CanonicalBuildManifest()
	if err != nil {
		return nil, err
	}
	staging, err := os.MkdirTemp(imagesDirectory, ".staging-")
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(staging)
		} else {
			_ = os.RemoveAll(staging)
		}
	}()
	rootDirectory := filepath.Join(staging, "root")
	imageDirectory := filepath.Join(staging, "image")
	if err := os.MkdirAll(rootDirectory, 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(imageDirectory, 0o700); err != nil {
		return nil, err
	}
	// Faithful copies of every dev plugin are compiled from inside the
	// staging area through relative replace locators. This keeps the
	// artifact free of machine-specific absolute source paths, so identical
	// content yields identical binaries regardless of where the dev sources
	// live (or whether two homes share the same plugin set).
	devLocators, devDirs, err := copyDevSources(lock, rootDirectory, staging)
	if err != nil {
		return nil, err
	}
	if err := lock.RestoreRootModule(rootDirectory, devLocators); err != nil {
		return nil, err
	}
	environment := lockedEnvironment(lock, options.GOMODCACHE)
	if _, err := runGo(ctx, rootDirectory, environment, "mod", "download", "all"); err != nil {
		return nil, err
	}
	if _, err := runGo(ctx, rootDirectory, environment, "mod", "verify"); err != nil {
		return nil, err
	}
	listOutput, err := runGo(ctx, rootDirectory, environment, "list", "-mod=readonly", "-m", "-json", "all")
	if err != nil {
		return nil, err
	}
	selected, err := decodeModuleStream(listOutput)
	if err != nil {
		return nil, err
	}
	if err := verifySelectedGraph(lock, selected, devDirs); err != nil {
		return nil, err
	}
	if err := verifyLockedSources(lock, selected); err != nil {
		return nil, err
	}
	graph, err := LoadGraph(ctx, rootDirectory, lock, LoadOptions{GOMODCACHE: options.GOMODCACHE})
	if err != nil {
		return nil, err
	}
	if err := Generate(rootDirectory, lock, graph); err != nil {
		return nil, err
	}
	binaryPath := filepath.Join(imageDirectory, "ingot-runtime")
	arguments := []string{"build", "-mod=readonly", "-trimpath", "-buildvcs=false", "-o", binaryPath}
	if len(lock.Build.Tags) > 0 {
		arguments = append(arguments, "-tags="+strings.Join(lock.Build.Tags, ","))
	}
	if len(lock.Build.LDFlags) > 0 {
		arguments = append(arguments, "-ldflags="+strings.Join(lock.Build.LDFlags, " "))
	}
	if len(lock.Build.GCFlags) > 0 {
		arguments = append(arguments, "-gcflags="+strings.Join(lock.Build.GCFlags, " "))
	}
	if len(lock.Build.ASMFlags) > 0 {
		arguments = append(arguments, "-asmflags="+strings.Join(lock.Build.ASMFlags, " "))
	}
	if _, err := runGo(ctx, rootDirectory, environment, arguments...); err != nil {
		return nil, err
	}
	artifactDigest, err := fileDigest(binaryPath)
	if err != nil {
		return nil, err
	}
	buildManifest := expectedBuildManifest
	creationOrder, manyOrder := inspectGraph(graph)
	directPlugins := make([]string, len(lock.Plugins))
	for i, plugin := range lock.Plugins {
		directPlugins[i] = plugin.ID
	}
	imageManifest := ImageManifest{SchemaVersion: 1, ImageID: imageID, ArtifactDigest: artifactDigest, BuildManifest: buildManifest, DirectPlugins: directPlugins, ComponentCreationOrder: creationOrder, ManyOrder: manyOrder}
	manifestData, err := json.MarshalIndent(imageManifest, "", "  ")
	if err != nil {
		return nil, err
	}
	manifestData = append(manifestData, '\n')
	if err := os.WriteFile(filepath.Join(imageDirectory, "manifest.json"), manifestData, 0o644); err != nil {
		return nil, err
	}
	stateRoot, err := os.MkdirTemp(staging, "check-state-")
	if err != nil {
		return nil, err
	}
	checkContext, cancel := context.WithTimeout(ctx, options.CheckTimeout)
	defer cancel()
	checkEnvironment := replaceEnvironment(os.Environ(), map[string]string{"INGOT_HOME": options.Home, "INGOT_CONFIG": options.ConfigPath, "INGOT_STATE_ROOT": stateRoot})
	if err := runRuntimeCheck(checkContext, binaryPath, checkEnvironment); err != nil {
		return nil, err
	}
	if err := syncFile(binaryPath); err != nil {
		return nil, err
	}
	if err := syncFile(filepath.Join(imageDirectory, "manifest.json")); err != nil {
		return nil, err
	}
	if err := syncDirectory(imageDirectory); err != nil {
		return nil, err
	}
	if err := os.Rename(imageDirectory, finalDirectory); err != nil {
		if existing, existingErr := readExistingImage(finalDirectory, imageID, expectedBuildManifest); existingErr == nil {
			if existing.ArtifactDigest != artifactDigest {
				return nil, &Error{Code: "INGOT-BUILD-REPRODUCIBILITY", Path: finalDirectory, Want: existing.ArtifactDigest, Actual: artifactDigest}
			}
			committed = true
			return existing, nil
		}
		return nil, err
	}
	committed = true
	_ = syncDirectory(imagesDirectory)
	return &BuildResult{ImageID: imageID, ArtifactDigest: artifactDigest, ImageDirectory: finalDirectory, BinaryPath: filepath.Join(finalDirectory, "ingot-runtime"), ComponentCreationOrder: creationOrder, ManyOrder: manyOrder}, nil
}

func verifySelectedGraph(lock *Lock, selected []resolvedModule, devDirs map[string]string) error {
	expected := map[string]LockedModule{}
	for _, item := range lock.Modules {
		expected[item.Path] = item
	}
	replacements := map[string]Replacement{}
	for _, item := range lock.Replacements {
		replacements[item.ModulePath] = item
	}
	seenModules, seenReplacements, mainSeen := map[string]bool{}, map[string]bool{}, false
	for _, item := range selected {
		if item.Main {
			if item.Path != "ingot.local/runtime-image" {
				return &Error{Code: "INGOT-BUILD-ROOT-MODULE", Actual: item.Path}
			}
			mainSeen = true
			continue
		}
		if item.Replace != nil {
			replacement, ok := replacements[item.Path]
			wantDirectory := replacement.DevPath
			if staged, ok := devDirs[item.Path]; ok {
				wantDirectory = staged
			}
			if !ok || item.Version != replacement.SyntheticVersion || filepath.Clean(item.Replace.Dir) != wantDirectory {
				return &Error{Code: "INGOT-BUILD-REPLACEMENT-GRAPH", Plugin: item.Path, Want: replacement.SyntheticVersion + " => " + wantDirectory, Actual: item.Version + " => " + item.Replace.Dir}
			}
			seenReplacements[item.Path] = true
			continue
		}
		locked, ok := expected[item.Path]
		if !ok || locked.Version != item.Version || (item.Sum != "" && locked.Sum != item.Sum) {
			return &Error{Code: "INGOT-BUILD-MODULE-GRAPH", Plugin: item.Path, Want: locked.Version + " " + locked.Sum, Actual: item.Version + " " + item.Sum}
		}
		seenModules[item.Path] = true
	}
	if !mainSeen || len(seenModules) != len(expected) || len(seenReplacements) != len(replacements) {
		return &Error{Code: "INGOT-BUILD-MODULE-GRAPH-INCOMPLETE", Want: fmt.Sprintf("%d modules, %d replacements", len(expected), len(replacements)), Actual: fmt.Sprintf("%d modules, %d replacements", len(seenModules), len(seenReplacements))}
	}
	return nil
}

func verifyLockedSources(lock *Lock, selected []resolvedModule) error {
	selectedByPath := map[string]resolvedModule{}
	for _, item := range selected {
		selectedByPath[item.Path] = item
	}
	replacementByPath := map[string]Replacement{}
	for _, item := range lock.Replacements {
		replacementByPath[item.ModulePath] = item
	}
	for _, plugin := range lock.Plugins {
		item := selectedByPath[plugin.ID]
		root := item.Dir
		if item.Replace != nil {
			root = item.Replace.Dir
			digest, err := DevSourceDigest(root)
			if err != nil {
				return err
			}
			if digest != replacementByPath[plugin.ID].ContentSHA256 {
				return &Error{Code: "INGOT-BUILD-DEV-DIGEST", Plugin: plugin.ID, Want: replacementByPath[plugin.ID].ContentSHA256, Actual: digest}
			}
		}
		identity, err := moduleIdentity(filepath.Join(root, "go.mod"))
		if err != nil {
			return err
		}
		if identity != plugin.ID {
			return &Error{Code: "INGOT-BUILD-MODULE-IDENTITY", Plugin: plugin.ID, Want: plugin.ID, Actual: identity}
		}
		manifest, err := ParseManifest(filepath.Join(root, "ingot.plugin.toml"))
		if err != nil {
			return err
		}
		digest, err := manifest.Digest()
		if err != nil {
			return err
		}
		compatibility, rangeErr := ParseVersionRange(manifest.Ingot)
		if rangeErr != nil || !compatibility.Contains(lock.IngotVersion) {
			return &Error{Code: "INGOT-BUILD-MANIFEST-INCOMPATIBLE", Plugin: plugin.ID, Want: manifest.Ingot, Actual: lock.IngotVersion, Err: rangeErr}
		}
		if digest != plugin.ManifestDigest || manifest.Name != plugin.Name || manifest.ConfigPackage != plugin.RootPackage || !sameComponents(manifest.Components, plugin.Components) || manifest.stateRecord() != (manifestStateRecord{Present: plugin.HasState, SchemaVersion: plugin.StateSchemaVersion, MinReaderVersion: plugin.StateMinReaderVersion}) {
			return &Error{Code: "INGOT-BUILD-MANIFEST-DRIFT", Plugin: plugin.ID, Want: plugin.ManifestDigest, Actual: digest}
		}
	}
	return nil
}

func sameComponents(left []ManifestComponent, right []LockedComponent) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].Name != right[i].Name || left[i].Package != right[i].Package {
			return false
		}
	}
	return true
}

func inspectGraph(graph *Graph) ([]string, map[string][]string) {
	creation := make([]string, len(graph.CreationOrder))
	for i, component := range graph.CreationOrder {
		creation[i] = component.ID
	}
	many := map[string][]string{}
	for _, component := range graph.Components {
		for _, dependency := range component.DependencyList {
			if dependency.Cardinality != CardinalityMany {
				continue
			}
			key := component.ID + ".Dependencies." + dependency.Name
			for _, provider := range dependency.Providers {
				many[key] = append(many[key], provider.Component.ID+".Exports."+provider.Export.Name)
			}
			if many[key] == nil {
				many[key] = []string{}
			}
		}
	}
	return creation, many
}

func fileDigest(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
func runRuntimeCheck(ctx context.Context, binary string, environment []string) error {
	command := exec.CommandContext(ctx, binary, "--ingot-check")
	command.Env = environment
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Run(); err != nil {
		return &Error{Code: "INGOT-BUILD-CHECK", Path: binary, Err: fmt.Errorf("%w: %s", err, strings.TrimSpace(output.String()))}
	}
	return nil
}
func syncFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	return file.Sync()
}
func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return directory.Sync()
}

func readExistingImage(directory, imageID string, expectedBuildManifest []byte) (*BuildResult, error) {
	data, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		return nil, err
	}
	var manifest ImageManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}
	if manifest.SchemaVersion != 1 || manifest.ImageID != imageID {
		return nil, &Error{Code: "INGOT-IMAGE-ID", Path: directory, Want: imageID, Actual: manifest.ImageID}
	}
	canonicalStored, err := canonicalJSON(manifest.BuildManifest)
	if err != nil {
		return nil, err
	}
	if digestBytes(canonicalStored) != imageID {
		return nil, &Error{Code: "INGOT-IMAGE-BUILD-IDENTITY", Path: directory, Want: imageID, Actual: digestBytes(canonicalStored)}
	}
	if len(expectedBuildManifest) > 0 && !bytes.Equal(canonicalStored, expectedBuildManifest) {
		return nil, &Error{Code: "INGOT-IMAGE-BUILD-MANIFEST", Path: directory, Want: digestBytes(expectedBuildManifest), Actual: digestBytes(canonicalStored)}
	}
	digest, err := fileDigest(filepath.Join(directory, "ingot-runtime"))
	if err != nil {
		return nil, err
	}
	if digest != manifest.ArtifactDigest {
		return nil, &Error{Code: "INGOT-IMAGE-ARTIFACT", Path: directory, Want: manifest.ArtifactDigest, Actual: digest}
	}
	return &BuildResult{ImageID: imageID, ArtifactDigest: digest, ImageDirectory: directory, BinaryPath: filepath.Join(directory, "ingot-runtime"), ComponentCreationOrder: manifest.ComponentCreationOrder, ManyOrder: manifest.ManyOrder}, nil
}

// VerifyImage verifies an immutable image's identity, provenance, and binary
// artifact digest. expectedBuildManifest may be nil when only self-consistency
// is available (for example, rollback to an older image).
func VerifyImage(directory, imageID string, expectedBuildManifest []byte) (*BuildResult, error) {
	return readExistingImage(directory, imageID, expectedBuildManifest)
}

func sortedKeys(values map[string][]string) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}
