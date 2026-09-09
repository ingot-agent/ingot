package httpdefault

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ingot-agent/ingot-abi/state"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/operation"
)

const (
	setupOperationName  = "http.default.config"
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
		Description:  "HTTP transport and proxy settings.",
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
		return operation.Result{}, fmt.Errorf("http.default.config: state scope is required: %w", ErrInvalidConfig)
	}
	current, err := loadConfig(o.scope.Dir())
	if err != nil {
		return operation.Result{}, err
	}
	response, err := request.Interaction.Request(ctx, interaction.Request{
		Name:        setupOperationName,
		Description: "HTTP transport and proxy settings.",
		Fields: []interaction.Field{
			{Name: "proxy_mode", Label: "Proxy Mode", Kind: interaction.FieldString, Required: false, Default: &interaction.Value{Kind: interaction.ValueString, String: current.ProxyMode}},
			{Name: "proxy_url", Label: "Proxy Url", Kind: interaction.FieldString, Required: false, Default: &interaction.Value{Kind: interaction.ValueString, String: current.ProxyURL}},
			{Name: "max_idle_conns", Label: "Max Idle Conns", Kind: interaction.FieldInteger, Required: false, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: int64(current.MaxIdleConns)}},
			{Name: "max_idle_conns_per_host", Label: "Max Idle Conns Per Host", Kind: interaction.FieldInteger, Required: false, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: int64(current.MaxIdleConnsPerHost)}},
			{Name: "idle_conn_timeout_seconds", Label: "Idle Conn Timeout Seconds", Kind: interaction.FieldInteger, Required: false, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: int64(current.IdleConnTimeoutSeconds)}},
			{Name: "tls_handshake_timeout_seconds", Label: "Tls Handshake Timeout Seconds", Kind: interaction.FieldInteger, Required: false, Default: &interaction.Value{Kind: interaction.ValueInteger, Integer: int64(current.TLSHandshakeTimeoutSeconds)}},
		},
	})
	if err != nil {
		return operation.Result{}, err
	}
	// Start from the persisted configuration and overlay only the answers the
	// Host supplied, so an untouched field keeps its current value.
	updated := current
	if v, ok := answerString(response, "proxy_mode"); ok {
		updated.ProxyMode = v
	}
	if v, ok := answerString(response, "proxy_url"); ok {
		updated.ProxyURL = v
	}
	if v, ok := answerInteger(response, "max_idle_conns"); ok {
		updated.MaxIdleConns = int(v)
	}
	if v, ok := answerInteger(response, "max_idle_conns_per_host"); ok {
		updated.MaxIdleConnsPerHost = int(v)
	}
	if v, ok := answerInteger(response, "idle_conn_timeout_seconds"); ok {
		updated.IdleConnTimeoutSeconds = int(v)
	}
	if v, ok := answerInteger(response, "tls_handshake_timeout_seconds"); ok {
		updated.TLSHandshakeTimeoutSeconds = int(v)
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
