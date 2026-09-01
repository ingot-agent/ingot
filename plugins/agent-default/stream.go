package agentdefault

import (
	"context"
	"errors"
	"fmt"

	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/observation"
)

func (r *runtime) Stream(ctx context.Context, turn agent.Turn, handler agent.StreamHandler) (agent.Result, error) {
	if handler == nil {
		return agent.Result{}, agent.ErrNilStreamHandler
	}
	if !r.streaming.Valid {
		return agent.Result{}, agent.ErrStreamingUnsupported
	}
	return r.execute(ctx, turn, handler)
}

func (r *runtime) invokeModel(ctx context.Context, request model.Request, handler agent.StreamHandler) (response model.Response, resultErr error) {
	if err := ctx.Err(); err != nil {
		return model.Response{}, err
	}
	r.observation.Emit(ctx, observation.ModelStarted{Request: request})
	defer func() {
		if recovered := recover(); recovered != nil {
			r.observation.Emit(ctx, observation.ModelFinished{Status: observation.StatusFailed, Error: fmt.Sprint(recovered)})
			panic(recovered)
		}
		finished := observation.ModelFinished{Status: terminalStatus(resultErr), Error: errorText(resultErr)}
		if resultErr == nil {
			finished.Response = &response
		}
		r.observation.Emit(ctx, finished)
	}()
	var err error
	if handler == nil {
		response, err = r.model.Complete(ctx, request)
		if err != nil {
			return model.Response{}, fmt.Errorf("complete model: %w", err)
		}
	} else {
		if !r.streaming.Valid {
			return model.Response{}, agent.ErrStreamingUnsupported
		}
		var handlerErr error
		response, err = r.streaming.Value.Stream(ctx, request, func(event model.StreamEvent) error {
			r.observation.Emit(ctx, observation.ModelProgress{Progress: event})
			if handlerErr != nil {
				return handlerErr
			}
			if handlerErr = ctx.Err(); handlerErr != nil {
				return handlerErr
			}
			if mapped, ok := mapModelStreamEvent(event); ok {
				handlerErr = handler(mapped)
			}
			return handlerErr
		})
		if handlerErr != nil {
			return model.Response{}, handlerErr
		}
		if err != nil {
			if errors.Is(err, model.ErrStreamingUnsupported) {
				return model.Response{}, fmt.Errorf("stream model: %w: %w", agent.ErrStreamingUnsupported, err)
			}
			return model.Response{}, fmt.Errorf("stream model: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return model.Response{}, err
	}
	if err := validateAssistant(response.Message); err != nil {
		return model.Response{}, err
	}
	return response, nil
}

func mapModelStreamEvent(event model.StreamEvent) (agent.StreamEvent, bool) {
	// Model delta events normally omit PartKind (it is carried on start).
	// Only nonempty text is part of the agent output contract.
	if event.Kind != model.StreamPartDelta || event.TextDelta == "" || len(event.DataDelta) != 0 ||
		(event.PartKind != 0 && event.PartKind != content.KindText) {
		return agent.StreamEvent{}, false
	}
	switch event.Semantic {
	case model.StreamSemanticReasoning:
		return agent.StreamEvent{Kind: agent.StreamReasoningDelta, TextDelta: event.TextDelta}, true
	case model.StreamSemanticContent:
		return agent.StreamEvent{Kind: agent.StreamOutputDelta, TextDelta: event.TextDelta}, true
	default:
		return agent.StreamEvent{}, false
	}
}
