package httpdefault_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	httpdefault "github.com/ingot-agent/http-default"

	"github.com/pelletier/go-toml/v2"
)

// testStateScope is a Plugin-owned Runtime state scope rooted at a test
// directory. Tests persist this Plugin's own configuration there exactly as a
// real runtime hands the scope to the Plugin.
type testStateScope struct{ dir string }

func (s testStateScope) Dir() string { return s.dir }

// withState attaches a Runtime state scope carrying cfg to the supplied
// Dependencies. The Host never decodes or injects plugin configuration; each
// Plugin loads its own configuration from its scope. A Dependencies value that
// already carries a scope (a test exercising scope handling directly) is left
// untouched.
func withState(t *testing.T, cfg httpdefault.Config, deps httpdefault.Dependencies) httpdefault.Dependencies {
	t.Helper()
	dir := writeTestConfig(t, cfg)
	if deps.State != nil {
		// A test supplied its own scope; persist cfg into it instead of
		// replacing the scope the test is exercising.
		persistTestConfig(t, deps.State.Dir(), cfg)
		return deps
	}
	deps.State = testStateScope{dir: dir}
	return deps
}

// writeTestConfig persists cfg as this Plugin's own configuration file and
// returns the state scope directory holding it. A zero Config means
// Unconfigured, so no file is written and the Plugin must apply defaults.
func writeTestConfig(t *testing.T, cfg httpdefault.Config) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	persistTestConfig(t, dir, cfg)
	return dir
}

// persistTestConfig writes cfg as this Plugin's own configuration file inside
// dir. A zero Config means Unconfigured, so no file is written and the Plugin
// must apply its defaults.
func persistTestConfig(t *testing.T, dir string, cfg httpdefault.Config) {
	t.Helper()
	if reflect.ValueOf(cfg).IsZero() {
		return
	}
	data, err := toml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}
