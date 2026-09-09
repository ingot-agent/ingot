package contextcompact

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ingot-agent/ingot-abi/state"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/operation"
)

const (
	setupOperationName  = "context.compact.config"
	setupOperationGroup = "configuration"
)

// setupOperation asks the Host for this Plugin's configuration through a
// structured interaction request and persists the answer in its own state
// scope. The Plugin owns validation and persistence; the Host never decodes
// plugin configuration.
type setupOperation struct{ scope state.Scope }

var _ operation.Operation = (*setupOperation)(nil)

func (*setupOperation) Definition() operation.Definition {
	return operation.Definition{
		Name:         setupOperationName,
		Description:  "Context compaction thresholds and summarization limits.",
		Group:        setupOperationGroup,
		InputSchema:  json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`),
		OutputSchema: json.RawMessage(`{"type":"object"}`),
	}
}

func (o *setupOperation) Invoke(ctx context.Context, request operation.Request) (operation.Result, error) {
	if err := ctx.Err(); err != nil {
		return operation.Result{}, err
	}
	if o.scope == nil || o.scope.Dir() == "" {
		return operation.Result{}, fmt.Errorf("context.compact.config: state scope is required: %w", ErrInvalidConfig)
	}
	current, err := loadConfig(o.scope.Dir())
	if err != nil {
		return operation.Result{}, err
	}
	response, err := request.Interaction.Request(ctx, interaction.Request{
		Name:        setupOperationName,
		Description: "Context compaction thresholds and summarization limits.",
		Fields: []interaction.Field{
			{Name: "provider", Label: "Provider", Kind: interaction.FieldString, Required: false, Default: &interaction.Value{Kind: interaction.ValueString, String: current.Provider}},
			{Name: "model", Label: "Model", Kind: interaction.FieldString, Required: false, Default: &interaction.Value{Kind: interaction.ValueString, String: current.Model}},
			{Name: "trigger_request_bytes", Label: "Trigger Request Bytes", Kind: interaction.FieldInteger, Required: false, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: int64(current.TriggerRequestBytes)}},
			{Name: "target_request_bytes", Label: "Target Request Bytes", Kind: interaction.FieldInteger, Required: false, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: int64(current.TargetRequestBytes)}},
			{Name: "anchor_turns", Label: "Anchor Turns", Kind: interaction.FieldInteger, Required: false, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: int64(current.AnchorTurns)}},
			{Name: "recent_turns", Label: "Recent Turns", Kind: interaction.FieldInteger, Required: false, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: int64(current.RecentTurns)}},
			{Name: "summary_chunk_bytes", Label: "Summary Chunk Bytes", Kind: interaction.FieldInteger, Required: false, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: int64(current.SummaryChunkBytes)}},
			{Name: "summary_max_tokens", Label: "Summary Max Tokens", Kind: interaction.FieldInteger, Required: false, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: int64(current.SummaryMaxTokens)}},
			{Name: "summary_max_bytes", Label: "Summary Max Bytes", Kind: interaction.FieldInteger, Required: false, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: int64(current.SummaryMaxBytes)}},
			{Name: "max_summary_chunks", Label: "Max Summary Chunks", Kind: interaction.FieldInteger, Required: false, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: int64(current.MaxSummaryChunks)}},
			{Name: "max_summary_passes", Label: "Max Summary Passes", Kind: interaction.FieldInteger, Required: false, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: int64(current.MaxSummaryPasses)}},
		},
	})
	if err != nil {
		return operation.Result{}, err
	}
	// Start from the persisted configuration and overlay only the answers the
	// Host supplied, so an untouched field keeps its current value.
	updated := current
	if v, ok := answerString(response, "provider"); ok {
		updated.Provider = v
	}
	if v, ok := answerString(response, "model"); ok {
		updated.Model = v
	}
	if v, ok := answerInteger(response, "trigger_request_bytes"); ok {
		updated.TriggerRequestBytes = int(v)
	}
	if v, ok := answerInteger(response, "target_request_bytes"); ok {
		updated.TargetRequestBytes = int(v)
	}
	if v, ok := answerInteger(response, "anchor_turns"); ok {
		updated.AnchorTurns = int(v)
	}
	if v, ok := answerInteger(response, "recent_turns"); ok {
		updated.RecentTurns = int(v)
	}
	if v, ok := answerInteger(response, "summary_chunk_bytes"); ok {
		updated.SummaryChunkBytes = int(v)
	}
	if v, ok := answerInteger(response, "summary_max_tokens"); ok {
		updated.SummaryMaxTokens = int(v)
	}
	if v, ok := answerInteger(response, "summary_max_bytes"); ok {
		updated.SummaryMaxBytes = int(v)
	}
	if v, ok := answerInteger(response, "max_summary_chunks"); ok {
		updated.MaxSummaryChunks = int(v)
	}
	if v, ok := answerInteger(response, "max_summary_passes"); ok {
		updated.MaxSummaryPasses = int(v)
	}
	if err := saveConfig(o.scope.Dir(), updated); err != nil {
		return operation.Result{}, err
	}
	output, err := json.Marshal(map[string]any{"restart_required": updated != current})
	if err != nil {
		return operation.Result{}, err
	}
	return operation.Result{Output: output}, nil
}

func answerInteger(response interaction.Response, name string) (int64, bool) {
	for _, answer := range response.Values {
		if answer.Name != name {
			continue
		}
		if answer.Value.Kind != interaction.ValueInteger {
			return 0, false
		}
		return answer.Value.Integer, true
	}
	return 0, false
}

func answerString(response interaction.Response, name string) (string, bool) {
	for _, answer := range response.Values {
		if answer.Name != name {
			continue
		}
		if answer.Value.Kind != interaction.ValueString {
			return "", false
		}
		return answer.Value.String, true
	}
	return "", false
}

func answerNumber(response interaction.Response, name string) (float64, bool) {
	for _, answer := range response.Values {
		if answer.Name != name {
			continue
		}
		if answer.Value.Kind != interaction.ValueNumber {
			return 0, false
		}
		return answer.Value.Number, true
	}
	return 0, false
}

func answerBoolean(response interaction.Response, name string) (bool, bool) {
	for _, answer := range response.Values {
		if answer.Name != name {
			continue
		}
		if answer.Value.Kind != interaction.ValueBoolean {
			return false, false
		}
		return answer.Value.Boolean, true
	}
	return false, false
}
