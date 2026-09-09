package script

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ingot-agent/ingot-abi/state"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/operation"
)

const (
	setupOperationName  = "interceptor.script.config"
	setupOperationGroup = "configuration"
)

// setupOperation asks the Host for this Plugin's hook declarations through a
// structured interaction request and persists the answer in its own state
// scope. hooks is a repeated object containing a repeated string field and a
// map, which is exactly the shape the nested interaction kinds exist for.
type setupOperation struct{ scope state.Scope }

var _ operation.Operation = (*setupOperation)(nil)

func (*setupOperation) Definition() operation.Definition {
	return operation.Definition{
		Name:         setupOperationName,
		Description:  "Review and update the script hook declarations.",
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
		return operation.Result{}, fmt.Errorf("interceptor.script config: state scope is required: %w", ErrInvalidConfig)
	}
	if _, err := loadConfig(o.scope.Dir()); err != nil {
		return operation.Result{}, err
	}
	response, err := request.Interaction.Request(ctx, interaction.Request{
		Name:        setupOperationName,
		Description: "One entry per hook; executable must be an absolute path.",
		Fields: []interaction.Field{
			{Name: "hooks", Label: "Hooks", Kind: interaction.FieldList, Element: &interaction.Field{Name: "hook", Kind: interaction.FieldObject, Fields: []interaction.Field{
				{Name: "name", Label: "Name", Kind: interaction.FieldString, Required: true},
				{Name: "target", Label: "Target", Kind: interaction.FieldChoice, Required: true, Options: []interaction.Option{
					{Value: "tool", Label: "Tool"},
					{Value: "model", Label: "Model"},
					{Value: "model-stream", Label: "Model stream"},
					{Value: "agent", Label: "Agent"},
				}},
				{Name: "executable", Label: "Executable", Description: "Absolute path to a regular file.", Kind: interaction.FieldString, Required: true},
				{Name: "args", Label: "Arguments", Kind: interaction.FieldList, Element: &interaction.Field{Name: "arg", Kind: interaction.FieldString}},
				{Name: "timeout_seconds", Label: "Timeout seconds", Kind: interaction.FieldInteger},
				{Name: "max_output_bytes", Label: "Max output bytes", Kind: interaction.FieldInteger},
			}}},
		},
	})
	if err != nil {
		return operation.Result{}, err
	}
	items, ok := answerList(response, "hooks")
	if !ok {
		return operation.Result{}, fmt.Errorf("interceptor.script config: hooks are required: %w", ErrInvalidConfig)
	}
	hooks := make([]Hook, 0, len(items))
	for i, item := range items {
		entries := map[string]interaction.Value{}
		for _, entry := range item.Entries {
			entries[entry.Name] = entry.Value
		}
		name, ok := stringEntry(entries, "name")
		if !ok || name == "" {
			return operation.Result{}, fmt.Errorf("hooks[%d].name is required: %w", i, ErrInvalidConfig)
		}
		target, ok := stringEntry(entries, "target")
		if !ok || target == "" {
			return operation.Result{}, fmt.Errorf("hooks[%d].target is required: %w", i, ErrInvalidConfig)
		}
		executable, ok := stringEntry(entries, "executable")
		if !ok || executable == "" {
			return operation.Result{}, fmt.Errorf("hooks[%d].executable is required: %w", i, ErrInvalidConfig)
		}
		hook := Hook{Name: name, Target: target, Executable: executable}
		if args, present := listEntry(entries, "args"); present {
			for _, value := range args {
				if value.Kind != interaction.ValueString {
					return operation.Result{}, fmt.Errorf("hooks[%d].args must be strings: %w", i, ErrInvalidConfig)
				}
				hook.Args = append(hook.Args, value.String)
			}
		}
		if v, present := integerEntry(entries, "timeout_seconds"); present {
			hook.TimeoutSeconds = int(v)
		}
		if v, present := integerEntry(entries, "max_output_bytes"); present {
			hook.MaxOutputBytes = int(v)
		}
		// normalizeHook enforces the same rules as construction, including the
		// executable existence check.
		if _, err := normalizeHook(hook); err != nil {
			return operation.Result{}, fmt.Errorf("hooks[%d]: %w", i, err)
		}
		hooks = append(hooks, hook)
	}
	updated := Config{Hooks: hooks}
	if err := validateHooks(updated); err != nil {
		return operation.Result{}, err
	}
	if err := saveConfig(o.scope.Dir(), updated); err != nil {
		return operation.Result{}, err
	}
	output, err := json.Marshal(map[string]any{"hooks": len(hooks), "restart_required": true})
	if err != nil {
		return operation.Result{}, err
	}
	return operation.Result{Output: output}, nil
}

// validateHooks checks duplicate names, mirroring construction.
func validateHooks(cfg Config) error {
	seen := make(map[string]struct{}, len(cfg.Hooks))
	for i, candidate := range cfg.Hooks {
		hook, err := normalizeHook(candidate)
		if err != nil {
			return fmt.Errorf("hooks[%d]: %w", i, err)
		}
		if _, exists := seen[hook.name]; exists {
			return fmt.Errorf("hooks[%d] duplicate name %q: %w", i, hook.name, ErrInvalidConfig)
		}
		seen[hook.name] = struct{}{}
	}
	return nil
}

func stringEntry(entries map[string]interaction.Value, name string) (string, bool) {
	value, ok := entries[name]
	if !ok || value.Kind != interaction.ValueString {
		return "", false
	}
	return value.String, true
}

func integerEntry(entries map[string]interaction.Value, name string) (int64, bool) {
	value, ok := entries[name]
	if !ok || value.Kind != interaction.ValueInteger {
		return 0, false
	}
	return value.Integer, true
}

func listEntry(entries map[string]interaction.Value, name string) ([]interaction.Value, bool) {
	value, ok := entries[name]
	if !ok || value.Kind != interaction.ValueList {
		return nil, false
	}
	return value.Items, true
}

func answerList(response interaction.Response, name string) ([]interaction.Value, bool) {
	for _, answer := range response.Values {
		if answer.Name == name && answer.Value.Kind == interaction.ValueList {
			return answer.Value.Items, true
		}
	}
	return nil, false
}
