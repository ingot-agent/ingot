// Package contextcompact implements incremental, session-scoped model context
// compaction while preserving the original session history.
package contextcompact

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"unicode/utf8"

	"github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/contextwindow"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/session"
)

const (
	defaultAnchorTurns       = 2
	defaultRecentTurns       = 4
	defaultSummaryChunkBytes = 64 * 1024
	defaultSummaryMaxTokens  = 1024
	defaultSummaryMaxBytes   = 64 * 1024
	defaultMaxSummaryChunks  = 8
	defaultMaxSummaryPasses  = 8
)

var (
	// ErrInvalidConfig indicates invalid limits or dependencies.
	ErrInvalidConfig = errors.New("invalid context.compact config")
	// ErrInvalidRequest indicates invalid invocation data or an empty session ID.
	ErrInvalidRequest = errors.New("invalid context compaction request")
	// ErrInvalidHistory indicates a model message sequence that cannot be safely grouped into turns.
	ErrInvalidHistory = errors.New("invalid context history")
	// ErrUnsupportedCheckpointVersion indicates an unknown owned Session Entry version.
	ErrUnsupportedCheckpointVersion = errors.New("unsupported context checkpoint version")
	// ErrCorruptCheckpoint indicates malformed or inconsistent owned checkpoint data.
	ErrCorruptCheckpoint = errors.New("corrupt context checkpoint")
	// ErrCompactionFailed indicates invalid output from the summarization model.
	ErrCompactionFailed = errors.New("context compaction failed")
	// ErrContextUncompactable indicates that the configured target cannot be reached safely.
	ErrContextUncompactable = errors.New("context cannot be compacted to target")
)

// Config controls byte watermarks, preserved turns, and summary bounds.
type Config struct {
	Provider            string `toml:"provider"`
	Model               string `toml:"model"`
	TriggerRequestBytes int    `toml:"trigger_request_bytes"`
	TargetRequestBytes  int    `toml:"target_request_bytes"`
	AnchorTurns         int    `toml:"anchor_turns"`
	RecentTurns         int    `toml:"recent_turns"`
	SummaryChunkBytes   int    `toml:"summary_chunk_bytes"`
	SummaryMaxTokens    int    `toml:"summary_max_tokens"`
	SummaryMaxBytes     int    `toml:"summary_max_bytes"`
	MaxSummaryChunks    int    `toml:"max_summary_chunks"`
	MaxSummaryPasses    int    `toml:"max_summary_passes"`
}

// Dependencies contains the model chokepoint and append-oriented Session store.
type Dependencies struct {
	Model model.Runtime
	Store session.Store
}

// Exports contains the context compactor capability.
type Exports struct {
	Compactor contextwindow.Compactor
}

type normalizedConfig struct {
	provider            string
	model               string
	triggerRequestBytes int
	targetRequestBytes  int
	anchorTurns         int
	recentTurns         int
	summaryChunkBytes   int
	summaryMaxTokens    int
	summaryMaxBytes     int
	maxSummaryChunks    int
	maxSummaryPasses    int
}

type compactor struct {
	model model.Runtime
	store session.Store
	cfg   normalizedConfig
	gates *gateManager
}

// New validates configuration and creates an independent compactor instance.
func New(ctx context.Context, cfg Config, deps Dependencies) (Exports, ingotabi.Cleanup, error) {
	if ctx == nil {
		return Exports{}, nil, fmt.Errorf("construct context.compact: nil context: %w", ErrInvalidConfig)
	}
	if err := ctx.Err(); err != nil {
		return Exports{}, nil, err
	}
	if isNil(deps.Model) || isNil(deps.Store) {
		return Exports{}, nil, fmt.Errorf("model and store dependencies are required: %w", ErrInvalidConfig)
	}
	normalized, err := normalizeConfig(cfg)
	if err != nil {
		return Exports{}, nil, err
	}
	instance := &compactor{model: deps.Model, store: deps.Store, cfg: normalized, gates: newGateManager()}
	return Exports{Compactor: instance}, nil, nil
}

func normalizeConfig(cfg Config) (normalizedConfig, error) {
	if !utf8.ValidString(cfg.Provider) || !utf8.ValidString(cfg.Model) {
		return normalizedConfig{}, fmt.Errorf("provider or model is invalid UTF-8: %w", ErrInvalidConfig)
	}
	if cfg.TriggerRequestBytes <= 0 || cfg.TargetRequestBytes <= 0 || cfg.TargetRequestBytes >= cfg.TriggerRequestBytes {
		return normalizedConfig{}, fmt.Errorf("target_request_bytes must be positive and less than trigger_request_bytes: %w", ErrInvalidConfig)
	}
	anchor, err := nonnegativeDefault(cfg.AnchorTurns, defaultAnchorTurns, "anchor_turns")
	if err != nil {
		return normalizedConfig{}, err
	}
	recent, err := nonnegativeDefault(cfg.RecentTurns, defaultRecentTurns, "recent_turns")
	if err != nil {
		return normalizedConfig{}, err
	}
	chunk, err := positiveDefault(cfg.SummaryChunkBytes, defaultSummaryChunkBytes, "summary_chunk_bytes")
	if err != nil {
		return normalizedConfig{}, err
	}
	maxTokens, err := positiveDefault(cfg.SummaryMaxTokens, defaultSummaryMaxTokens, "summary_max_tokens")
	if err != nil {
		return normalizedConfig{}, err
	}
	maxBytes, err := positiveDefault(cfg.SummaryMaxBytes, defaultSummaryMaxBytes, "summary_max_bytes")
	if err != nil {
		return normalizedConfig{}, err
	}
	maxChunks, err := positiveDefault(cfg.MaxSummaryChunks, defaultMaxSummaryChunks, "max_summary_chunks")
	if err != nil {
		return normalizedConfig{}, err
	}
	maxPasses, err := positiveDefault(cfg.MaxSummaryPasses, defaultMaxSummaryPasses, "max_summary_passes")
	if err != nil {
		return normalizedConfig{}, err
	}
	return normalizedConfig{
		provider: cfg.Provider, model: cfg.Model,
		triggerRequestBytes: cfg.TriggerRequestBytes, targetRequestBytes: cfg.TargetRequestBytes,
		anchorTurns: anchor, recentTurns: recent, summaryChunkBytes: chunk,
		summaryMaxTokens: maxTokens, summaryMaxBytes: maxBytes,
		maxSummaryChunks: maxChunks, maxSummaryPasses: maxPasses,
	}, nil
}

func nonnegativeDefault(value, fallback int, field string) (int, error) {
	if value == 0 {
		return fallback, nil
	}
	if value < 0 {
		return 0, fmt.Errorf("%s must not be negative: %w", field, ErrInvalidConfig)
	}
	return value, nil
}

func positiveDefault(value, fallback int, field string) (int, error) {
	if value == 0 {
		return fallback, nil
	}
	if value < 0 {
		return 0, fmt.Errorf("%s must be positive: %w", field, ErrInvalidConfig)
	}
	return value, nil
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var _ contextwindow.Compactor = (*compactor)(nil)
