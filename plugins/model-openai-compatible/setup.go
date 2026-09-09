package openaicompat

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ingot-agent/ingot-abi/state"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/operation"
)

const (
	setupOperationName  = "model.openai-compatible.config"
	setupOperationGroup = "configuration"
)

// setupOperation asks the Host for this Plugin's provider list through a
// structured interaction request and persists the answer in its own state
// scope. providers is a repeated object containing a repeated string field,
// which is exactly the shape the nested interaction kinds exist for. The API
// key is marked Sensitive, so the Host never receives the stored value back as
// a default.
type setupOperation struct{ scope state.Scope }

var _ operation.Operation = (*setupOperation)(nil)

func (*setupOperation) Definition() operation.Definition {
	return operation.Definition{
		Name:         setupOperationName,
		Description:  "Review and update the OpenAI-compatible model providers.",
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
		return operation.Result{}, fmt.Errorf("model.openai-compatible config: state scope is required: %w", ErrInvalidConfig)
	}
	current, err := loadConfig(o.scope.Dir())
	if err != nil {
		return operation.Result{}, err
	}
	// Sensitive fields never carry a default back to the Host, so an existing
	// key stays in place unless the operator supplies a new one.
	response, err := request.Interaction.Request(ctx, interaction.Request{
		Name:        setupOperationName,
		Description: "One entry per OpenAI-compatible provider.",
		Fields: []interaction.Field{
			{Name: "providers", Label: "Providers", Kind: interaction.FieldList, Element: &interaction.Field{Name: "provider", Kind: interaction.FieldObject, Fields: []interaction.Field{
				{Name: "name", Label: "Name", Description: "Stable provider name referenced by model.runtime.", Kind: interaction.FieldString, Required: true},
				{Name: "base_url", Label: "Base URL", Description: "Absolute http/https endpoint.", Kind: interaction.FieldString, Required: true},
				{Name: "api_key", Label: "API key", Kind: interaction.FieldString, Sensitive: true},
				{Name: "models", Label: "Models", Kind: interaction.FieldList, Element: &interaction.Field{Name: "model", Kind: interaction.FieldString}},
				{Name: "organization", Label: "Organization", Kind: interaction.FieldString},
				{Name: "project", Label: "Project", Kind: interaction.FieldString},
			}}},
		},
	})
	if err != nil {
		return operation.Result{}, err
	}
	items, ok := answerList(response, "providers")
	if !ok {
		return operation.Result{}, fmt.Errorf("model.openai-compatible config: providers are required: %w", ErrInvalidConfig)
	}
	existing := make(map[string]ProviderConfig, len(current.Providers))
	for _, item := range current.Providers {
		existing[item.Name] = item
	}
	providers := make([]ProviderConfig, 0, len(items))
	for i, item := range items {
		entries := map[string]interaction.Value{}
		for _, entry := range item.Entries {
			entries[entry.Name] = entry.Value
		}
		name, ok := stringEntry(entries, "name")
		if !ok || name == "" {
			return operation.Result{}, fmt.Errorf("providers[%d].name is required: %w", i, ErrInvalidConfig)
		}
		baseURL, ok := stringEntry(entries, "base_url")
		if !ok || baseURL == "" {
			return operation.Result{}, fmt.Errorf("providers[%d].base_url is required: %w", i, ErrInvalidConfig)
		}
		candidate := ProviderConfig{Name: name, BaseURL: baseURL}
		// An empty answer means "keep the stored secret" rather than "clear it",
		// because the Host never received the previous value.
		if apiKey, present := stringEntry(entries, "api_key"); present && apiKey != "" {
			candidate.APIKey = apiKey
		} else if previous, found := existing[name]; found {
			candidate.APIKey = previous.APIKey
		}
		if organization, present := stringEntry(entries, "organization"); present {
			candidate.Organization = organization
		}
		if project, present := stringEntry(entries, "project"); present {
			candidate.Project = project
		}
		if models, present := listEntry(entries, "models"); present {
			names := make([]string, 0, len(models))
			for _, value := range models {
				if value.Kind != interaction.ValueString {
					return operation.Result{}, fmt.Errorf("providers[%d].models must be strings: %w", i, ErrInvalidConfig)
				}
				names = append(names, value.String)
			}
			candidate.Models = names
		} else if previous, found := existing[name]; found {
			candidate.Models = previous.Models
		}
		// Carry over limits the form does not expose.
		if previous, found := existing[name]; found {
			candidate.DefaultHeaders = previous.DefaultHeaders
			candidate.MaxResponseBytes = previous.MaxResponseBytes
			candidate.MaxErrorBodyBytes = previous.MaxErrorBodyBytes
			candidate.MaxAssetBytes = previous.MaxAssetBytes
			candidate.AssetConcurrency = previous.AssetConcurrency
		}
		providers = append(providers, candidate)
	}
	updated := Config{Providers: providers}
	// Reuse construction validation so a persisted provider list is always one
	// the Plugin would accept at startup.
	if err := validateProviders(updated); err != nil {
		return operation.Result{}, err
	}
	if err := saveConfig(o.scope.Dir(), updated); err != nil {
		return operation.Result{}, err
	}
	output, err := json.Marshal(map[string]any{"providers": len(updated.Providers), "restart_required": true})
	if err != nil {
		return operation.Result{}, err
	}
	return operation.Result{Output: output}, nil
}

func stringEntry(entries map[string]interaction.Value, name string) (string, bool) {
	value, ok := entries[name]
	if !ok || value.Kind != interaction.ValueString {
		return "", false
	}
	return value.String, true
}

func answerList(response interaction.Response, name string) ([]interaction.Value, bool) {
	for _, answer := range response.Values {
		if answer.Name == name && answer.Value.Kind == interaction.ValueList {
			return answer.Value.Items, true
		}
	}
	return nil, false
}

func listEntry(entries map[string]interaction.Value, name string) ([]interaction.Value, bool) {
	value, ok := entries[name]
	if !ok || value.Kind != interaction.ValueList {
		return nil, false
	}
	return value.Items, true
}
