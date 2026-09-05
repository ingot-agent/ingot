// Package appbackend defines the shared configuration and Web protocol for the
// app.backend composite plugin.
package appbackend

import (
	"errors"
	"fmt"
	"time"
)

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
