package agentdefault

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	"github.com/ingot-agent/ingot-abi/state"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/operation"
)

const (
	setupOperationName  = "agent.default.config"
	setupOperationGroup = "configuration"
)

// setupOperation asks the Host for this Plugin's configuration through a
// structured interaction request and persists the answer in its own state
// scope. temperature and max_tokens are optional overrides: leaving the answer
// empty clears them rather than forcing a value.
type setupOperation struct{ scope state.Scope }

var _ operation.Operation = (*setupOperation)(nil)

func (*setupOperation) Definition() operation.Definition {
	return operation.Definition{
		Name:         setupOperationName,
		Description:  "Review and update the agent loop defaults.",
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
		return operation.Result{}, fmt.Errorf("agent.default config: state scope is required: %w", ErrInvalidConfig)
	}
	current, err := loadConfig(o.scope.Dir())
	if err != nil {
		return operation.Result{}, err
	}
	maxRoundsDefault := int64(current.MaxRounds)
	if maxRoundsDefault == 0 {
		maxRoundsDefault = defaultMaxRounds
	}
	response, err := request.Interaction.Request(ctx, interaction.Request{
		Name:        setupOperationName,
		Description: "Provider, model and generation limits used when a request leaves them empty.",
		Fields: []interaction.Field{
			{Name: "provider", Label: "Provider", Description: "Empty uses the model.runtime default.", Kind: interaction.FieldString, Default: optionalString(current.Provider)},
			{Name: "model", Label: "Model", Description: "Empty uses the model.runtime default.", Kind: interaction.FieldString, Default: optionalString(current.Model)},
			{Name: "temperature", Label: "Temperature", Description: "Between 0 and 2; empty clears the override.", Kind: interaction.FieldNumber, Default: optionalFloat(current.Temperature)},
			{Name: "max_tokens", Label: "Max tokens", Description: "Empty clears the override.", Kind: interaction.FieldInteger, Default: optionalInt(current.MaxTokens)},
			{Name: "max_rounds", Label: "Max rounds", Kind: interaction.FieldInteger, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: maxRoundsDefault}},
		},
	})
	if err != nil {
		return operation.Result{}, err
	}
	updated := current
	if v, ok := answerString(response, "provider"); ok {
		updated.Provider = v
	}
	if v, ok := answerString(response, "model"); ok {
		updated.Model = v
	}
	// An absent or explicitly empty numeric answer clears the override.
	updated.Temperature = answerOptionalFloat(response, "temperature")
	updated.MaxTokens = answerOptionalInt(response, "max_tokens")
	if v, ok := answerInteger(response, "max_rounds"); ok {
		updated.MaxRounds = int(v)
	}
	if err := validateConfig(updated); err != nil {
		return operation.Result{}, err
	}
	if err := saveConfig(o.scope.Dir(), updated); err != nil {
		return operation.Result{}, err
	}
	output, err := json.Marshal(map[string]any{"restart_required": true})
	if err != nil {
		return operation.Result{}, err
	}
	return operation.Result{Output: output}, nil
}

// validateConfig applies the same generation limits at construction and in the
// setup Operation.
func validateConfig(cfg Config) error {
	if cfg.Temperature != nil && (math.IsNaN(*cfg.Temperature) || math.IsInf(*cfg.Temperature, 0) || *cfg.Temperature < 0 || *cfg.Temperature > 2) {
		return fmt.Errorf("temperature must be in [0,2]: %w", ErrInvalidConfig)
	}
	if cfg.MaxTokens != nil && *cfg.MaxTokens < 1 {
		return fmt.Errorf("max_tokens must be positive: %w", ErrInvalidConfig)
	}
	maxRounds := cfg.MaxRounds
	if maxRounds == 0 {
		maxRounds = defaultMaxRounds
	}
	if maxRounds < 1 {
		return fmt.Errorf("max_rounds must be positive: %w", ErrInvalidConfig)
	}
	return nil
}

func optionalString(value string) *interaction.Value {
	if value == "" {
		return nil
	}
	result := interaction.StringValue(value)
	return &result
}

func optionalFloat(value *float64) *interaction.Value {
	if value == nil {
		return nil
	}
	result := interaction.NumberValue(*value)
	return &result
}

func optionalInt(value *int) *interaction.Value {
	if value == nil {
		return nil
	}
	result := interaction.IntegerValue(int64(*value))
	return &result
}

func answerString(response interaction.Response, name string) (string, bool) {
	for _, answer := range response.Values {
		if answer.Name == name && answer.Value.Kind == interaction.ValueString {
			return answer.Value.String, true
		}
	}
	return "", false
}

func answerInteger(response interaction.Response, name string) (int64, bool) {
	for _, answer := range response.Values {
		if answer.Name == name && answer.Value.Kind == interaction.ValueInteger {
			return answer.Value.Integer, true
		}
	}
	return 0, false
}

// answerOptionalFloat returns nil when the Host omitted the field or supplied an
// empty string, which is how a cleared override is expressed.
func answerOptionalFloat(response interaction.Response, name string) *float64 {
	for _, answer := range response.Values {
		if answer.Name != name {
			continue
		}
		switch answer.Value.Kind {
		case interaction.ValueNumber:
			value := answer.Value.Number
			return &value
		case interaction.ValueString:
			if answer.Value.String == "" {
				return nil
			}
		}
	}
	return nil
}

func answerOptionalInt(response interaction.Response, name string) *int {
	for _, answer := range response.Values {
		if answer.Name != name {
			continue
		}
		switch answer.Value.Kind {
		case interaction.ValueInteger:
			value := int(answer.Value.Integer)
			return &value
		case interaction.ValueString:
			if answer.Value.String == "" {
				return nil
			}
		}
	}
	return nil
}
