package httpdefault

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/pelletier/go-toml/v2"
)

// configFileName is the Plugin-owned configuration file inside this Plugin's
// Runtime state scope. The runtime never reads or decodes it; the file name
// and its contents are this Plugin's private persistent contract.
const configFileName = "config.toml"

// loadConfig reads the Plugin-owned configuration from its Runtime state
// scope. A missing file is the normal Unconfigured state and yields the
// zero-value Config so defaults apply; any other read or decode failure is
// reported instead of silently discarding persisted state.
func loadConfig(scope string) (Config, error) {
	var config Config
	if scope == "" {
		return config, nil
	}
	data, err := os.ReadFile(filepath.Join(scope, configFileName))
	if errors.Is(err, os.ErrNotExist) {
		return config, nil
	}
	if err != nil {
		return config, fmt.Errorf("read plugin config: %w", err)
	}
	decoder := toml.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return config, fmt.Errorf("decode plugin config: %w", err)
	}
	return config, nil
}

// isNil reports whether value is a nil interface or a typed-nil pointer-like
// value, which must never be treated as an available dependency.
func isNil(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	}
	return false
}

// saveConfig atomically writes the Plugin-owned configuration into its Runtime
// state scope, creating the scope when needed. The Host never participates: the
// Operation that triggers a change calls this directly.
func saveConfig(scope string, config Config) error {
	if scope == "" {
		return fmt.Errorf("state scope is required: %w", ErrInvalidConfig)
	}
	if err := os.MkdirAll(scope, 0o700); err != nil {
		return fmt.Errorf("create state scope: %w", err)
	}
	data, err := toml.Marshal(config)
	if err != nil {
		return fmt.Errorf("encode plugin config: %w", err)
	}
	temporary, err := os.CreateTemp(scope, ".config-")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, filepath.Join(scope, configFileName))
}
