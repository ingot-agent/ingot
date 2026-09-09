// Package appbackend defines the shared configuration and Web protocol for the
// app.backend composite plugin.
package appbackend

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// configFileName is the Plugin-owned configuration file inside this Plugin's
// Runtime state scope. Both app.backend components share one Plugin identity
// and therefore one scope. The runtime never reads or decodes this file.
const configFileName = "config.toml"

// LoadConfig reads the Plugin-owned configuration from its Runtime state
// scope. A missing file is the normal Unconfigured state and yields the
// zero-value Config so defaults apply; any other read or decode failure is
// reported instead of silently discarding persisted state.
func LoadConfig(scope string) (Config, error) {
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

// SaveConfig writes the Plugin-owned configuration atomically into its Runtime
// state scope, creating the scope when needed.
func SaveConfig(scope string, config Config) error {
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

const (
	defaultAddress          = "127.0.0.1:7316"
	defaultReplayCapacity   = 1024
	defaultSubscriberBuffer = 64
	defaultHeartbeat        = 15 * time.Second
)

// ErrInvalidConfig indicates invalid app.backend configuration.
var ErrInvalidConfig = errors.New("invalid app.backend config")

// Config is shared by all app.backend components.
type Config struct {
	Backend BackendConfig `toml:"backend"`
}

// BackendConfig controls the HTTP server and transient event delivery.
type BackendConfig struct {
	Address                  string `toml:"address"`
	ReplayCapacity           int    `toml:"replay_capacity"`
	SubscriberBuffer         int    `toml:"subscriber_buffer"`
	HeartbeatIntervalSeconds int    `toml:"heartbeat_interval_seconds"`
	OperationRetention       int    `toml:"operation_retention"`
	MaxAssetBytes            int64  `toml:"max_asset_bytes"`
}

// NormalizedBackendConfig contains validated configuration with defaults.
type NormalizedBackendConfig struct {
	Address            string
	ReplayCapacity     int
	SubscriberBuffer   int
	Heartbeat          time.Duration
	OperationRetention int
	MaxAssetBytes      int64
}

// Normalize validates the backend configuration and materializes defaults.
func (c Config) Normalize() (NormalizedBackendConfig, error) {
	cfg := NormalizedBackendConfig{
		Address:            c.Backend.Address,
		ReplayCapacity:     c.Backend.ReplayCapacity,
		SubscriberBuffer:   c.Backend.SubscriberBuffer,
		OperationRetention: c.Backend.OperationRetention,
		MaxAssetBytes:      c.Backend.MaxAssetBytes,
	}
	if cfg.Address == "" {
		cfg.Address = defaultAddress
	}
	if cfg.ReplayCapacity == 0 {
		cfg.ReplayCapacity = defaultReplayCapacity
	}
	if cfg.SubscriberBuffer == 0 {
		cfg.SubscriberBuffer = defaultSubscriberBuffer
	}
	if cfg.OperationRetention == 0 {
		cfg.OperationRetention = 128
	}
	if cfg.MaxAssetBytes == 0 {
		cfg.MaxAssetBytes = 64 << 20
	}
	if cfg.OperationRetention < 1 || cfg.MaxAssetBytes < 1 {
		return NormalizedBackendConfig{}, fmt.Errorf("operation_retention and max_asset_bytes must be positive: %w", ErrInvalidConfig)
	}
	if cfg.ReplayCapacity < 1 {
		return NormalizedBackendConfig{}, fmt.Errorf("replay_capacity must be positive: %w", ErrInvalidConfig)
	}
	if cfg.SubscriberBuffer < 1 {
		return NormalizedBackendConfig{}, fmt.Errorf("subscriber_buffer must be positive: %w", ErrInvalidConfig)
	}
	if c.Backend.HeartbeatIntervalSeconds < 0 || uint64(c.Backend.HeartbeatIntervalSeconds) > uint64((1<<63-1)/time.Second) {
		return NormalizedBackendConfig{}, fmt.Errorf("heartbeat_interval_seconds is outside the supported duration range: %w", ErrInvalidConfig)
	}
	cfg.Heartbeat = time.Duration(c.Backend.HeartbeatIntervalSeconds) * time.Second
	if cfg.Heartbeat == 0 {
		cfg.Heartbeat = defaultHeartbeat
	}
	return cfg, nil
}
