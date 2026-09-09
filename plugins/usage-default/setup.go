package usagedefault

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/ingot-agent/ingot-abi/state"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/operation"
)

const (
	setupOperationName  = "usage.default.config"
	setupOperationGroup = "configuration"
)

// setupOperation asks the Host for this Plugin's configuration through a
// structured interaction request and persists the answer in its own state
// scope. routes is a repeated object, which is why the interaction protocol
// needs nested field kinds.
type setupOperation struct{ scope state.Scope }

var _ operation.Operation = (*setupOperation)(nil)

func (*setupOperation) Definition() operation.Definition {
	return operation.Definition{
		Name:         setupOperationName,
		Description:  "Review and update the usage.default route table.",
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
		return operation.Result{}, fmt.Errorf("usage.default config: state scope is required: %w", ErrInvalidConfig)
	}
	current, err := loadConfig(o.scope.Dir())
	if err != nil {
		return operation.Result{}, err
	}
	profiles, err := builtInProfiles()
	if err != nil {
		return operation.Result{}, err
	}
	profileNames := make([]string, 0, len(profiles))
	for name := range profiles {
		profileNames = append(profileNames, name)
	}
	sort.Strings(profileNames)
	options := make([]interaction.Option, 0, len(profileNames))
	for _, name := range profileNames {
		options = append(options, interaction.Option{Value: name, Label: name})
	}
	response, err := request.Interaction.Request(ctx, interaction.Request{
		Name:        setupOperationName,
		Description: "Model-to-profile routes and the token estimate cache size.",
		Fields: []interaction.Field{
			{Name: "routes", Label: "Routes", Description: "One route per provider and model pattern.", Kind: interaction.FieldList, Element: &interaction.Field{Name: "route", Kind: interaction.FieldObject, Fields: []interaction.Field{
				{Name: "provider", Label: "Provider", Kind: interaction.FieldString, Required: true},
				{Name: "model_pattern", Label: "Model pattern", Description: "Regular expression matched against the model name.", Kind: interaction.FieldString, Required: true},
				{Name: "profile", Label: "Profile", Kind: interaction.FieldChoice, Required: true, Options: options},
			}}},
			{Name: "cache_entries", Label: "Cache entries", Description: "Zero selects the built-in default.", Kind: interaction.FieldInteger, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: int64(current.CacheEntries)}},
		},
	})
	if err != nil {
		return operation.Result{}, err
	}
	updated := current
	if v, ok := answerList(response, "routes"); ok {
		routes := make([]Route, 0, len(v))
		for i, item := range v {
			entries := map[string]interaction.Value{}
			for _, entry := range item.Entries {
				entries[entry.Name] = entry.Value
			}
			provider, pok := entries["provider"]
			pattern, mok := entries["model_pattern"]
			profile, fok := entries["profile"]
			if !pok || !mok || !fok || provider.Kind != interaction.ValueString || pattern.Kind != interaction.ValueString || profile.Kind != interaction.ValueString {
				return operation.Result{}, fmt.Errorf("routes[%d] requires provider, model_pattern, and profile: %w", i, ErrInvalidConfig)
			}
			routes = append(routes, Route{Provider: provider.String, ModelPattern: pattern.String, Profile: profile.String})
		}
		updated.Routes = routes
	}
	if v, ok := answerInteger(response, "cache_entries"); ok {
		updated.CacheEntries = int(v)
	}
	if len(updated.Routes) == 0 {
		return operation.Result{}, fmt.Errorf("routes must not be empty: %w", ErrInvalidConfig)
	}
	if updated.CacheEntries < 0 {
		return operation.Result{}, fmt.Errorf("cache_entries must not be negative: %w", ErrInvalidConfig)
	}
	if _, err := compileRoutes(updated.Routes, profiles); err != nil {
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
