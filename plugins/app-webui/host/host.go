// Package hostcomponent provides the process-local Web host component for the
// app.backend composite plugin.
package hostcomponent

import (
	"context"
	"fmt"

	appbackend "github.com/ingot-agent/app-webui"
	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/ingot-abi/state"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/observation"
)

// Dependencies contains this Plugin's own persistent state scope. Keeping the
// host independent of other capabilities avoids a graph cycle when an agent
// consumes the exported interaction channel.
type Dependencies struct {
	State state.Scope
}

// Exports contains host capabilities used by agent and HTTP components.
type Exports struct {
	Channel               interaction.Channel
	ExecutionInteractions interaction.ExecutionBinder
	Runtime               appbackend.Runtime
	Observer              observation.Observer
}

type runtime struct {
	events       *eventHub
	interactions *interactionHost
}

// New loads this Plugin's own configuration from its state scope and
// constructs the shared event and interaction host state. A missing
// configuration file is the normal Unconfigured state; defaults apply.
func New(ctx context.Context, deps Dependencies) (Exports, ingotabi.Cleanup, error) {
	if ctx == nil {
		return Exports{}, nil, fmt.Errorf("construct app.backend host: %w", appbackend.ErrInvalidConfig)
	}
	if err := ctx.Err(); err != nil {
		return Exports{}, nil, err
	}
	if deps.State == nil {
		return Exports{}, nil, fmt.Errorf("state dependency is required: %w", appbackend.ErrInvalidConfig)
	}
	cfg, err := appbackend.LoadConfig(deps.State.Dir())
	if err != nil {
		return Exports{}, nil, fmt.Errorf("construct app.backend host: %w: %w", err, appbackend.ErrInvalidConfig)
	}
	normalized, err := cfg.Normalize()
	if err != nil {
		return Exports{}, nil, err
	}
	events := newEventHub(normalized.ReplayCapacity, normalized.SubscriberBuffer)
	interactions := newInteractionHost(events)
	instance := &runtime{events: events, interactions: interactions}
	return Exports{Channel: interactions, ExecutionInteractions: interactions, Runtime: instance, Observer: &observer{events: events}}, nil, nil
}

func (r *runtime) Events() appbackend.EventHub { return r.events }

func (r *runtime) Interactions() appbackend.InteractionHost { return r.interactions }

var _ appbackend.Runtime = (*runtime)(nil)
