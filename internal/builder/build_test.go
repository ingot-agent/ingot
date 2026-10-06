package builder

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildOptionsHomePrecedence(t *testing.T) {
	environmentHome := filepath.Join(t.TempDir(), "environment-home")
	t.Setenv("INGOT_HOME", environmentHome)

	options, err := (BuildOptions{}).defaults()
	if err != nil {
		t.Fatal(err)
	}
	if options.Home != environmentHome {
		t.Fatalf("environment home = %q, want %q", options.Home, environmentHome)
	}

	explicitHome := filepath.Join(t.TempDir(), "explicit-home")
	options, err = (BuildOptions{Home: explicitHome}).defaults()
	if err != nil {
		t.Fatal(err)
	}
	if options.Home != explicitHome {
		t.Fatalf("explicit home = %q, want %q", options.Home, explicitHome)
	}

	t.Setenv("INGOT_HOME", "")
	userHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	options, err = (BuildOptions{}).defaults()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(userHome, ".ingot")
	if options.Home != want {
		t.Fatalf("fallback home = %q, want %q", options.Home, want)
	}
}

func TestVerifySelectedGraphRejectsLocalReplacementDrift(t *testing.T) {
	t.Parallel()
	lock := fixtureGraphLock("/dev/a", "/dev/b", "/dev/c")
	lock.Replacements[0].SyntheticVersion = "v0.1.1"
	for _, test := range []struct {
		name, version, directory string
	}{
		{name: "version", version: "v0.0.0", directory: lock.Replacements[0].DevPath},
		{name: "directory", version: "v0.1.1", directory: "/other/source"},
	} {
		t.Run(test.name, func(t *testing.T) {
			selected := []resolvedModule{{Path: "ingot.local/runtime-image", Main: true}}
			for _, replacement := range lock.Replacements {
				selected = append(selected, resolvedModule{Path: replacement.ModulePath, Version: replacement.SyntheticVersion, Replace: &resolvedModule{Dir: replacement.DevPath}})
			}
			for _, item := range lock.Modules {
				selected = append(selected, resolvedModule{Path: item.Path, Version: item.Version, Sum: item.Sum})
			}
			if err := verifySelectedGraph(lock, selected, nil); err != nil {
				t.Fatal(err)
			}
			selected[1].Version = test.version
			selected[1].Replace.Dir = test.directory
			if err := verifySelectedGraph(lock, selected, nil); err == nil || !strings.Contains(err.Error(), "INGOT-BUILD-REPLACEMENT-GRAPH") {
				t.Fatalf("replacement drift error = %v", err)
			}
		})
	}
}
