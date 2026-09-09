package appcomponent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	appbackend "github.com/ingot-agent/app-webui"
	"github.com/ingot-agent/ingot-abi/state"
	"github.com/ingot-agent/sdk/operation"
)

// setupOperationName is the stable protocol identity of the app.backend
// configuration Operation. Hosts discover it like any other Operation; the
// runtime never special-cases configuration.
const setupOperationName = "app.backend.config"

const setupOperationGroup = "configuration"

// newConfigOperations returns the Plugin-owned configuration Operations. They
// read and write this Plugin's own state scope; no host-side configuration
// decoding or injection is involved.
func newConfigOperations(scope state.Scope) []operation.Operation {
	return []operation.Operation{&configOperation{scope: scope}}
}

type configOperation struct{ scope state.Scope }

func (*configOperation) Definition() operation.Definition {
	return operation.Definition{
		Name:        setupOperationName,
		Description: "Review and update the app.backend HTTP server configuration.",
		Group:       setupOperationGroup,
		InputSchema: json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "address": {"type": "string", "description": "HTTP bind address, for example 127.0.0.1:7316"},
    "replay_capacity": {"type": "integer", "minimum": 1},
    "subscriber_buffer": {"type": "integer", "minimum": 1},
    "heartbeat_interval_seconds": {"type": "integer", "minimum": 0},
    "operation_retention": {"type": "integer", "minimum": 1},
    "max_asset_bytes": {"type": "integer", "minimum": 1}
  }
}`),
		OutputSchema: json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["address", "replay_capacity", "subscriber_buffer", "heartbeat_interval_seconds", "operation_retention", "max_asset_bytes", "restart_required"],
  "properties": {
    "address": {"type": "string"},
    "replay_capacity": {"type": "integer"},
    "subscriber_buffer": {"type": "integer"},
    "heartbeat_interval_seconds": {"type": "integer"},
    "operation_retention": {"type": "integer"},
    "max_asset_bytes": {"type": "integer"},
    "restart_required": {"type": "boolean"}
  }
}`),
	}
}

type configInput struct {
	Address                  *string `json:"address"`
	ReplayCapacity           *int    `json:"replay_capacity"`
	SubscriberBuffer         *int    `json:"subscriber_buffer"`
	HeartbeatIntervalSeconds *int    `json:"heartbeat_interval_seconds"`
	OperationRetention       *int    `json:"operation_retention"`
	MaxAssetBytes            *int64  `json:"max_asset_bytes"`
}

func (o *configOperation) Invoke(ctx context.Context, request operation.Request) (operation.Result, error) {
	if err := ctx.Err(); err != nil {
		return operation.Result{}, err
	}
	if o.scope == nil || o.scope.Dir() == "" {
		return operation.Result{}, fmt.Errorf("app.backend config: %w", appbackend.ErrInvalidConfig)
	}
	current, err := appbackend.LoadConfig(o.scope.Dir())
	if err != nil {
		return operation.Result{}, err
	}
	normalized, err := current.Normalize()
	if err != nil {
		return operation.Result{}, err
	}
	var input configInput
	if len(request.Input) != 0 {
		decoder := json.NewDecoder(bytes.NewReader(request.Input))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			return operation.Result{}, fmt.Errorf("app.backend config input: %w", err)
		}
	}
	// Explicit input wins; otherwise the current persisted values are
	// presented as form defaults so the host can render an edit form.
	if input.Address != nil {
		current.Backend.Address = *input.Address
	}
	if input.ReplayCapacity != nil {
		current.Backend.ReplayCapacity = *input.ReplayCapacity
	}
	if input.SubscriberBuffer != nil {
		current.Backend.SubscriberBuffer = *input.SubscriberBuffer
	}
	if input.HeartbeatIntervalSeconds != nil {
		current.Backend.HeartbeatIntervalSeconds = *input.HeartbeatIntervalSeconds
	}
	if input.OperationRetention != nil {
		current.Backend.OperationRetention = *input.OperationRetention
	}
	if input.MaxAssetBytes != nil {
		current.Backend.MaxAssetBytes = *input.MaxAssetBytes
	}
	updated, err := current.Normalize()
	if err != nil {
		return operation.Result{}, err
	}
	if err := appbackend.SaveConfig(o.scope.Dir(), current); err != nil {
		return operation.Result{}, err
	}
	// Binding a socket or resizing buffers cannot change in place, so the
	// Plugin reports that a restart is required instead of pretending the
	// running process adopted the new values.
	restartRequired := updated.Address != normalized.Address ||
		updated.ReplayCapacity != normalized.ReplayCapacity ||
		updated.SubscriberBuffer != normalized.SubscriberBuffer ||
		updated.Heartbeat != normalized.Heartbeat ||
		updated.OperationRetention != normalized.OperationRetention ||
		updated.MaxAssetBytes != normalized.MaxAssetBytes
	output, err := json.Marshal(map[string]any{
		"address":                    updated.Address,
		"replay_capacity":            updated.ReplayCapacity,
		"subscriber_buffer":          updated.SubscriberBuffer,
		"heartbeat_interval_seconds": int(updated.Heartbeat / 1e9),
		"operation_retention":        updated.OperationRetention,
		"max_asset_bytes":            updated.MaxAssetBytes,
		"restart_required":           restartRequired,
	})
	if err != nil {
		return operation.Result{}, err
	}
	return operation.Result{Output: output}, nil
}

var _ operation.Operation = (*configOperation)(nil)
