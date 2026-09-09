package tooledit

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ingot-agent/ingot-abi/state"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/operation"
)

// setupOperationName is the stable protocol identity of this Plugin's
// configuration Operation. The Host discovers it like any other Operation and
// never special-cases it.
const setupOperationName = "tool.edit.config"

// setupOperationGroup is a presentation hint so a host can organize
// configuration Operations together. The SDK only carries the slot.
const setupOperationGroup = "configuration"

// setupOperation implements Operation + Interaction: it asks the Host for the
// current values through a structured request, then persists the answer into
// this Plugin's own state scope. The Plugin owns validation, persistence and
// the decision of whether a change needs a restart.
type setupOperation struct{ scope state.Scope }

var _ operation.Operation = (*setupOperation)(nil)

func (*setupOperation) Definition() operation.Definition {
	return operation.Definition{
		Name:        setupOperationName,
		Description: "Review and update the tool.edit file size limits.",
		Group:       setupOperationGroup,
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`),
		OutputSchema: json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["max_file_bytes", "max_scan_bytes", "restart_required"],
  "properties": {
    "max_file_bytes": {"type": "integer"},
    "max_scan_bytes": {"type": "integer"},
    "restart_required": {"type": "boolean"}
  }
}`),
	}
}

func (o *setupOperation) Invoke(ctx context.Context, request operation.Request) (operation.Result, error) {
	if err := ctx.Err(); err != nil {
		return operation.Result{}, err
	}
	if o.scope == nil || o.scope.Dir() == "" {
		return operation.Result{}, fmt.Errorf("tool.edit config: %w", ErrInvalidConfig)
	}
	current, err := loadConfig(o.scope.Dir())
	if err != nil {
		return operation.Result{}, err
	}
	// Present the effective values as form defaults: an Unconfigured Plugin
	// still shows the defaults it would actually use.
	fileDefault := int64(current.MaxFileBytes)
	if fileDefault == 0 {
		fileDefault = defaultMaxFileBytes
	}
	scanDefault := int64(current.MaxScanBytes)
	if scanDefault == 0 {
		scanDefault = defaultMaxScanBytes
	}
	response, err := request.Interaction.Request(ctx, interaction.Request{
		Name:        setupOperationName,
		Description: "File size limits for edit_file, read_file and search.",
		Fields: []interaction.Field{
			{Name: "max_file_bytes", Label: "Max file bytes", Description: "Largest single file the tools read or edit.", Kind: interaction.FieldInteger, Required: true, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: fileDefault}},
			{Name: "max_scan_bytes", Label: "Max scan bytes", Description: "Total bytes search may scan.", Kind: interaction.FieldInteger, Required: true, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: scanDefault}},
		},
	})
	if err != nil {
		return operation.Result{}, err
	}
	fileValue, ok := answerInteger(response, "max_file_bytes")
	if !ok {
		return operation.Result{}, fmt.Errorf("tool.edit config: missing max_file_bytes: %w", ErrInvalidConfig)
	}
	scanValue, ok := answerInteger(response, "max_scan_bytes")
	if !ok {
		return operation.Result{}, fmt.Errorf("tool.edit config: missing max_scan_bytes: %w", ErrInvalidConfig)
	}
	updated := Config{MaxFileBytes: int(fileValue), MaxScanBytes: int(scanValue)}
	// The Operation receives explicit answers, so a non-positive value is an
	// invalid request rather than an omitted field that should default.
	if updated.MaxFileBytes < 1 || updated.MaxScanBytes < 1 {
		return operation.Result{}, fmt.Errorf("limits must be positive: %w", ErrInvalidConfig)
	}
	if _, err := normalizeConfig(updated); err != nil {
		return operation.Result{}, err
	}
	if err := saveConfig(o.scope.Dir(), updated); err != nil {
		return operation.Result{}, err
	}
	// The limits are captured at construction, so a change only takes effect
	// after the runtime restarts.
	output, err := json.Marshal(map[string]any{
		"max_file_bytes":   updated.MaxFileBytes,
		"max_scan_bytes":   updated.MaxScanBytes,
		"restart_required": updated != current,
	})
	if err != nil {
		return operation.Result{}, err
	}
	return operation.Result{Output: output}, nil
}

// answerInteger reads one integer answer by field name.
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
