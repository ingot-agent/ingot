package builder

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
)

type generatedRuntime struct {
	graph       *Graph
	environment []string
}

func validateGeneratedRuntimeInputs(ctx context.Context, desired *DesiredPlugins, lock *Lock) error {
	if err := desired.Validate(); err != nil {
		return err
	}
	if err := lock.Validate(); err != nil {
		return err
	}
	digest, err := desired.Digest()
	if err != nil {
		return err
	}
	if digest != lock.PluginsDigest {
		return &Error{Code: "INGOT-BUILD-DESIRED-DRIFT", Field: "plugins_digest", Want: lock.PluginsDigest, Actual: digest}
	}
	if lock.Target.GOOS != runtime.GOOS || lock.Target.GOARCH != runtime.GOARCH {
		return &Error{Code: "INGOT-BUILD-CHECK-TARGET", Want: runtime.GOOS + "/" + runtime.GOARCH, Actual: lock.Target.GOOS + "/" + lock.Target.GOARCH}
	}
	// Gate on the go command that will actually compile the image, not on the
	// toolchain that compiled this Builder. Only a minimum is required: any
	// newer release toolchain can build a module with an older go directive.
	toolchain, err := probeGoToolchain(ctx)
	if err != nil {
		return err
	}
	requirement := goRequirement{Version: lock.MinimumGoVersion}
	if derived, recorded := requiredGoVersion(lock.Modules); recorded && compareGoVersions(derived.Version, requirement.Version) > 0 {
		requirement = derived
	}
	return requireGoToolchain(toolchain, requirement)
}

// prepareGeneratedRuntime restores and verifies the locked root module,
// resolves the Component Graph, and writes the generated runtime source. The
// caller owns the root and dev staging directories and decides whether to
// compile the result or publish it as source.
func prepareGeneratedRuntime(ctx context.Context, lock *Lock, rootDirectory, devStagingDirectory, moduleCache string) (*generatedRuntime, error) {
	if err := os.MkdirAll(rootDirectory, 0o700); err != nil {
		return nil, err
	}
	// Faithful copies of every local replacement are consumed through
	// relative replace locators. This keeps generated output independent of
	// the machine-specific source locations recorded in the lock.
	devLocators, devDirs, err := copyDevSources(lock, rootDirectory, devStagingDirectory)
	if err != nil {
		return nil, err
	}
	if err := lock.RestoreRootModule(rootDirectory, devLocators); err != nil {
		return nil, err
	}
	environment := lockedEnvironment(lock, moduleCache)
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
	graph, err := LoadGraph(ctx, rootDirectory, lock, LoadOptions{GOMODCACHE: moduleCache})
	if err != nil {
		return nil, err
	}
	if err := Generate(rootDirectory, lock, graph); err != nil {
		return nil, err
	}
	return &generatedRuntime{graph: graph, environment: environment}, nil
}

func cleanAbsolutePath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
}
