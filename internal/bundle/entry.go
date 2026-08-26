package bundle

import (
	"os"
	"path/filepath"

	"github.com/ingot-agent/ingot/internal/builder"
	"golang.org/x/mod/modfile"
)

// readEntry parses one materialized plugin directory and returns its
// canonical module path and manifest short name. The bundle is part of the
// ingot binary, so failures here indicate a broken bundle rather than a user
// error.
func readEntry(pluginRoot string) (Entry, error) {
	goMod, err := os.ReadFile(filepath.Join(pluginRoot, "go.mod"))
	if err != nil {
		return Entry{}, err
	}
	parsed, err := modfile.Parse("go.mod", goMod, nil)
	if err != nil {
		return Entry{}, err
	}
	if parsed.Module == nil {
		return Entry{}, os.ErrInvalid
	}
	manifest, err := builder.ParseManifest(filepath.Join(pluginRoot, "ingot.plugin.toml"))
	if err != nil {
		return Entry{}, err
	}
	return Entry{Module: parsed.Module.Mod.Path, Name: manifest.Name}, nil
}
