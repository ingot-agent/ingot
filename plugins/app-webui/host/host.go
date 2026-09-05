// Package hostcomponent provides the process-local Web host component for the
// app.backend composite plugin.
package hostcomponent

import (
	"context"
	"fmt"

	appbackend "github.com/ingot-agent/app-webui"
	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/observation"
)

// Dependencies contains no consumed capabilities. Keeping the host independent
// avoids a graph cycle when an agent consumes the exported interaction channel.
type Dependencies struct{}

// Exports contains host capabilities used by agent and HTTP components.
type Exports struct {
	Channel  interaction.Channel
	Runtime  appbackend.Runtime
	Observer observation.Observer
}

type runtime struct {
	events       *eventHub
	interactions *interactionHost
}

// New constructs the shared event and interaction host state.
func New(ctx context.Context, cfg appbackend.Config, _ Dependencies) (Exports, ingotabi.Cleanup, error) {
	if ctx == nil {
		return Exports{}, nil, fmt.Errorf("construct app.backend host: %w", appbackend.ErrInvalidConfig)
	}
	if err := ctx.Err(); err != nil {
		return Exports{}, nil, err
	}
	normalized, err := cfg.Normalize()
	if err != nil {
		return Exports{}, nil, err
	}
	events := newEventHub(normalized.ReplayCapacity, normalized.SubscriberBuffer)
	interactions := newInteractionHost(events)
	instance := &runtime{events: events, interactions: interactions}
	return Exports{Channel: interactions, Runtime: instance, Observer: &observer{events: events}}, nil, nil
}

func (r *runtime) Events() appbackend.EventHub { return r.events }

func (r *runtime) Interactions() appbackend.InteractionHost { return r.interactions }

var _ appbackend.Runtime = (*runtime)(nil)
