package builder

import (
	"os"
	"path/filepath"
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
