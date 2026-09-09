package approval

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ingot-agent/ingot-abi/state"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/operation"
)

const (
	setupOperationName  = "interceptor.approval.config"
	setupOperationGroup = "configuration"
)

// setupOperation asks the Host for this Plugin's configuration through a
// structured interaction request and persists the answer in its own state
// scope. rules is a repeated object, which is why the interaction protocol
// needs nested field kinds.
type setupOperation struct{ scope state.Scope }

var _ operation.Operation = (*setupOperation)(nil)

func (*setupOperation) Definition() operation.Definition {
	return operation.Definition{
		Name:         setupOperationName,
		Description:  "Review and update the approval interceptor policy.",
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
		return operation.Result{}, fmt.Errorf("interceptor.approval config: state scope is required: %w", ErrInvalidConfig)
	}
	current, err := loadConfig(o.scope.Dir())
	if err != nil {
		return operation.Result{}, err
	}
	actionDefault := current.DefaultAction
	if actionDefault == "" {
		actionDefault = actionAsk
	}
	displayDefault := current.ArgumentDisplay
	if displayDefault == "" {
		displayDefault = displayFull
	}
	displayDefaultBytes := int64(current.MaxDisplayBytes)
	if displayDefaultBytes == 0 {
		displayDefaultBytes = defaultMaxDisplayBytes
	}
	actionOptions := []interaction.Option{
		{Value: actionAllow, Label: "Allow", Description: "Run the call without asking."},
		{Value: actionAsk, Label: "Ask", Description: "Ask the user before running the call."},
		{Value: actionDeny, Label: "Deny", Description: "Reject the call."},
	}
	displayOptions := []interaction.Option{
		{Value: displayFull, Label: "Full", Description: "Show the full arguments."},
		{Value: displayNamesOnly, Label: "Names only", Description: "Show argument names only."},
	}
	response, err := request.Interaction.Request(ctx, interaction.Request{
		Name:        setupOperationName,
		Description: "Default approval action, argument display and per-tool rules.",
		Fields: []interaction.Field{
			{Name: "default_action", Label: "Default action", Kind: interaction.FieldChoice, Required: true, Options: actionOptions, Default: stringValue(actionDefault)},
			{Name: "argument_display", Label: "Argument display", Kind: interaction.FieldChoice, Required: true, Options: displayOptions, Default: stringValue(displayDefault)},
			{Name: "max_display_bytes", Label: "Max display bytes", Kind: interaction.FieldInteger, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: displayDefaultBytes}},
			{Name: "rules", Label: "Rules", Description: "Per-tool overrides of the default action.", Kind: interaction.FieldList, Element: &interaction.Field{Name: "rule", Kind: interaction.FieldObject, Fields: []interaction.Field{
				{Name: "tool", Label: "Tool", Kind: interaction.FieldString, Required: true},
				{Name: "action", Label: "Action", Kind: interaction.FieldChoice, Required: true, Options: actionOptions},
			}}},
		},
	})
	if err != nil {
		return operation.Result{}, err
	}
	updated := current
	if v, ok := answerString(response, "default_action"); ok {
		updated.DefaultAction = v
	}
	if v, ok := answerString(response, "argument_display"); ok {
		updated.ArgumentDisplay = v
	}
	if v, ok := answerInteger(response, "max_display_bytes"); ok {
		updated.MaxDisplayBytes = int(v)
	}
	if v, ok := answerList(response, "rules"); ok {
		rules := make([]Rule, 0, len(v))
		for i, item := range v {
			entries := map[string]interaction.Value{}
			for _, entry := range item.Entries {
				entries[entry.Name] = entry.Value
			}
			tool, ok := entries["tool"]
			if !ok || tool.Kind != interaction.ValueString || tool.String == "" {
				return operation.Result{}, fmt.Errorf("rules[%d].tool must be non-empty: %w", i, ErrInvalidConfig)
			}
			action, ok := entries["action"]
			if !ok || action.Kind != interaction.ValueString || !validAction(action.String) {
				return operation.Result{}, fmt.Errorf("rules[%d].action is invalid: %w", i, ErrInvalidConfig)
			}
			rules = append(rules, Rule{Tool: tool.String, Action: action.String})
		}
		updated.Rules = rules
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

func stringValue(value string) *interaction.Value {
	result := interaction.StringValue(value)
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

func answerList(response interaction.Response, name string) ([]interaction.Value, bool) {
	for _, answer := range response.Values {
		if answer.Name == name && answer.Value.Kind == interaction.ValueList {
			return answer.Value.Items, true
		}
	}
	return nil, false
}
