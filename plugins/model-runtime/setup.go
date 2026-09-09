package modelruntime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ingot-agent/ingot-abi/state"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/operation"
)

const (
	setupOperationName  = "model.runtime.config"
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
		Description:  "Default provider and model used when a request leaves them empty.",
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
		return operation.Result{}, fmt.Errorf("model.runtime.config: state scope is required: %w", ErrInvalidConfig)
	}
	current, err := loadConfig(o.scope.Dir())
	if err != nil {
		return operation.Result{}, err
	}
	response, err := request.Interaction.Request(ctx, interaction.Request{
		Name:        setupOperationName,
		Description: "Default provider and model used when a request leaves them empty.",
		Fields: []interaction.Field{
			{Name: "default_provider", Label: "Default Provider", Kind: interaction.FieldString, Required: false, Default: &interaction.Value{Kind: interaction.ValueString, String: current.DefaultProvider}},
			{Name: "default_model", Label: "Default Model", Kind: interaction.FieldString, Required: false, Default: &interaction.Value{Kind: interaction.ValueString, String: current.DefaultModel}},
		},
	})
	if err != nil {
		return operation.Result{}, err
	}
	// Start from the persisted configuration and overlay only the answers the
	// Host supplied, so an untouched field keeps its current value.
	updated := current
	if v, ok := answerString(response, "default_provider"); ok {
		updated.DefaultProvider = v
	}
	if v, ok := answerString(response, "default_model"); ok {
		updated.DefaultModel = v
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
