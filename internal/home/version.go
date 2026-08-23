package home

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ingot-agent/ingot/internal/builder"
	"golang.org/x/mod/module"
)

// ResolveModuleQuery resolves module@query to an exact canonical Go module
// version. Exact versions pass through after Go semantic-import validation.
func (home *Home) ResolveModuleQuery(ctx context.Context, specification string) (string, string, error) {
	index := strings.LastIndex(specification, "@")
	modulePath, query := specification, "latest"
	if index >= 0 {
		modulePath, query = specification[:index], specification[index+1:]
	}
	if err := module.CheckPath(modulePath); err != nil {
		return "", "", err
	}
	if query == "" {
		return "", "", fmt.Errorf("empty module version query")
	}
	if module.CanonicalVersion(query) == query {
		if err := module.Check(modulePath, query); err != nil {
			return "", "", err
		}
		return modulePath, query, nil
	}
	temporary, err := os.MkdirTemp(home.Root, ".version-query-")
	if err != nil {
		return "", "", err
	}
	defer func() { _ = os.RemoveAll(temporary) }()
	if err := os.WriteFile(filepath.Join(temporary, "go.mod"), []byte("module ingot.local/version-query\n\ngo 1.24.0\n"), 0o600); err != nil {
		return "", "", err
	}
	command := exec.CommandContext(ctx, "go", "list", "-m", "-json", modulePath+"@"+query)
	command.Dir = temporary
	environment := replaceEnv(os.Environ(), "GOWORK", "off")
	environment = replaceEnv(environment, "GOTOOLCHAIN", "local")
	command.Env = replaceEnv(environment, "GOMODCACHE", filepath.Join(home.Root, "cache", "gomod"))
	var output, stderr bytes.Buffer
	command.Stdout, command.Stderr = &output, &stderr
	if err := command.Run(); err != nil {
		return "", "", fmt.Errorf("resolve %s@%s: %w: %s", modulePath, query, err, strings.TrimSpace(stderr.String()))
	}
	var result struct{ Path, Version string }
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		return "", "", err
	}
	if result.Path != modulePath || module.CanonicalVersion(result.Version) != result.Version {
		return "", "", fmt.Errorf("resolver returned non-canonical module %s@%s", result.Path, result.Version)
	}
	return result.Path, result.Version, nil
}

func (home *Home) LookupPlugin(reference string) (DesiredLookup, error) {
	desired, err := builder.ParseDesired(home.DesiredPath())
	if err != nil {
		return DesiredLookup{}, err
	}
	lock, lockErr := builder.ParseLock(home.LockPath())
	if lockErr != nil {
		lock = nil
	}
	index, err := findPlugin(desired, lock, reference)
	if err != nil {
		return DesiredLookup{}, err
	}
	return DesiredLookup{Index: index, Plugin: desired.Plugins[index]}, nil
}

type DesiredLookup struct {
	Index  int
	Plugin builder.DesiredPlugin
}
