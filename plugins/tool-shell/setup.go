package toolshell

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/ingot-agent/ingot-abi/state"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/operation"
)

const (
	setupOperationName  = "tool.shell.config"
	setupOperationGroup = "configuration"
)

// setupOperation asks the Host for this Plugin's configuration through a
// structured interaction request and persists the answer in its own state
// scope. environment and inherit_env use the nested and repeated field kinds
// because they are maps and lists rather than scalar values.
type setupOperation struct{ scope state.Scope }

var _ operation.Operation = (*setupOperation)(nil)

func (*setupOperation) Definition() operation.Definition {
	return operation.Definition{
		Name:         setupOperationName,
		Description:  "Review and update the tool.shell execution boundary.",
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
		return operation.Result{}, fmt.Errorf("tool.shell config: state scope is required: %w", ErrInvalidConfig)
	}
	current, err := loadConfig(o.scope.Dir())
	if err != nil {
		return operation.Result{}, err
	}
	timeoutDefault := int64(current.TimeoutSeconds)
	if timeoutDefault == 0 {
		timeoutDefault = defaultTimeoutSeconds
	}
	maxOutputDefault := int64(current.MaxOutputBytes)
	if maxOutputDefault == 0 {
		maxOutputDefault = defaultMaxOutputBytes
	}
	// environment is a map, so it is modelled as a list of {name, value}
	// entries; inherit_env is a plain list of names.
	environmentFields := make([]interaction.Field, 0, len(current.Environment))
	for _, key := range sortedKeys(current.Environment) {
		environmentFields = append(environmentFields, interaction.Field{
			Name: key, Label: key, Kind: interaction.FieldString, Sensitive: isSensitiveKey(key),
			Default: &interaction.Value{Kind: interaction.ValueString, String: current.Environment[key]},
		})
	}
	inheritDefault := make([]string, 0, len(current.InheritEnv))
	inheritDefault = append(inheritDefault, current.InheritEnv...)
	response, err := request.Interaction.Request(ctx, interaction.Request{
		Name:        setupOperationName,
		Description: "Shell execution limits, environment and inherited variables.",
		Fields: []interaction.Field{
			{Name: "shell", Label: "Shell", Description: "Absolute shell path; empty resolves a platform default.", Kind: interaction.FieldString, Default: stringValue(current.Shell)},
			{Name: "timeout_seconds", Label: "Timeout seconds", Kind: interaction.FieldInteger, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: timeoutDefault}},
			{Name: "max_output_bytes", Label: "Max output bytes", Kind: interaction.FieldInteger, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: maxOutputDefault}},
			{Name: "environment", Label: "Environment", Description: "Explicit environment entries.", Kind: interaction.FieldList, Element: &interaction.Field{Name: "entry", Kind: interaction.FieldObject, Fields: []interaction.Field{
				{Name: "name", Kind: interaction.FieldString, Required: true},
				{Name: "value", Kind: interaction.FieldString},
			}}},
			{Name: "inherit_env", Label: "Inherited variables", Description: "Parent variables passed through.", Kind: interaction.FieldList, Element: &interaction.Field{Name: "name", Kind: interaction.FieldString}, Default: &interaction.Value{Kind: interaction.ValueList, Items: stringItems(inheritDefault)}},
		},
	})
	if err != nil {
		return operation.Result{}, err
	}
	updated := current
	if v, ok := answerString(response, "shell"); ok {
		updated.Shell = v
	}
	if v, ok := answerInteger(response, "timeout_seconds"); ok {
		updated.TimeoutSeconds = int(v)
	}
	if v, ok := answerInteger(response, "max_output_bytes"); ok {
		updated.MaxOutputBytes = int(v)
	}
	if v, ok := answerList(response, "environment"); ok {
		environment := map[string]string{}
		for i, item := range v {
			entries := map[string]interaction.Value{}
			for _, entry := range item.Entries {
				entries[entry.Name] = entry.Value
			}
			name, ok := entries["name"]
			if !ok || name.Kind != interaction.ValueString || name.String == "" {
				return operation.Result{}, fmt.Errorf("environment[%d] requires a name: %w", i, ErrInvalidConfig)
			}
			value := entries["value"]
			environment[name.String] = value.String
		}
		updated.Environment = environment
	}
	if v, ok := answerList(response, "inherit_env"); ok {
		names := make([]string, 0, len(v))
		for _, item := range v {
			if item.Kind != interaction.ValueString {
				return operation.Result{}, fmt.Errorf("inherit_env entries must be strings: %w", ErrInvalidConfig)
			}
			names = append(names, item.String)
		}
		updated.InheritEnv = names
	}
	if _, err := normalizeConfig(updated); err != nil {
		return operation.Result{}, err
	}
	if err := saveConfig(o.scope.Dir(), updated); err != nil {
		return operation.Result{}, err
	}
	output, err := json.Marshal(map[string]any{"restart_required": updated.Shell != current.Shell || updated.TimeoutSeconds != current.TimeoutSeconds || updated.MaxOutputBytes != current.MaxOutputBytes || !equalEnvironment(updated.Environment, current.Environment) || !equalStrings(updated.InheritEnv, current.InheritEnv)})
	if err != nil {
		return operation.Result{}, err
	}
	return operation.Result{Output: output}, nil
}

func stringValue(value string) *interaction.Value {
	if value == "" {
		return nil
	}
	result := interaction.StringValue(value)
	return &result
}

func stringItems(values []string) []interaction.Value {
	items := make([]interaction.Value, 0, len(values))
	for _, value := range values {
		items = append(items, interaction.StringValue(value))
	}
	return items
}

// isSensitiveKey keeps obvious credentials out of the default projection.
func isSensitiveKey(key string) bool {
	upper := strings.ToUpper(key)
	return strings.Contains(upper, "KEY") || strings.Contains(upper, "TOKEN") || strings.Contains(upper, "SECRET") || strings.Contains(upper, "PASSWORD")
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func equalEnvironment(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
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
