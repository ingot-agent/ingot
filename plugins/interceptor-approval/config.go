package approval

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unicode/utf8"

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

// validateConfig applies the same rules at construction and in the setup
// Operation, so a persisted change cannot introduce a configuration the Plugin
// would refuse to start with.
func validateConfig(cfg Config) error {
	defaultAction := cfg.DefaultAction
	if defaultAction == "" {
		defaultAction = actionAsk
	}
	if !validAction(defaultAction) {
		return fmt.Errorf("invalid default_action %q: %w", defaultAction, ErrInvalidConfig)
	}
	display := cfg.ArgumentDisplay
	if display == "" {
		display = displayFull
	}
	if display != displayFull && display != displayNamesOnly {
		return fmt.Errorf("invalid argument_display %q: %w", display, ErrInvalidConfig)
	}
	maxDisplay := cfg.MaxDisplayBytes
	if maxDisplay == 0 {
		maxDisplay = defaultMaxDisplayBytes
	}
	if maxDisplay < 1 {
		return fmt.Errorf("max_display_bytes must be positive: %w", ErrInvalidConfig)
	}
	rules := make(map[string]string, len(cfg.Rules))
	for i, rule := range cfg.Rules {
		if rule.Tool == "" || !utf8.ValidString(rule.Tool) {
			return fmt.Errorf("rules[%d].tool must be non-empty UTF-8: %w", i, ErrInvalidConfig)
		}
		if !validAction(rule.Action) {
			return fmt.Errorf("rules[%d].action is invalid: %w", i, ErrInvalidConfig)
		}
		if _, exists := rules[rule.Tool]; exists {
			return fmt.Errorf("duplicate rule for %q: %w", rule.Tool, ErrInvalidConfig)
		}
		rules[rule.Tool] = rule.Action
	}
	return nil
}
