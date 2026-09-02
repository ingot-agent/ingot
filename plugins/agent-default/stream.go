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
	return r.execute(ctx, turn, handler)
}

func (r *runtime) invokeModel(ctx context.Context, request model.Request, handler agent.StreamHandler) (model.Response, error) {
	if err := ctx.Err(); err != nil {
		return model.Response{}, err
	}
	invocation := cloneModelRequest(request)
	if handler == nil || !r.streaming.Valid {
		response, err := r.invokeCompleteModel(ctx, invocation)
		if err != nil {
			return model.Response{}, err
		}
		if err := ctx.Err(); err != nil {
			return model.Response{}, err
		}
		return response, nil
	}

	delivered := false
	var handlerErr error
	response, err := r.observeModelInvocation(ctx, invocation, func() (model.Response, error) {
		response, streamErr := r.streaming.Value.Stream(ctx, cloneModelRequest(invocation), func(event model.StreamEvent) error {
			r.observation.Emit(ctx, observation.ModelProgress{Progress: event})
			if handlerErr != nil {
				return handlerErr
			}
			if handlerErr = ctx.Err(); handlerErr != nil {
				return handlerErr
			}
			if mapped, ok := mapModelStreamEvent(event); ok {
				delivered = true
				handlerErr = handler(mapped)
			}
			return handlerErr
		})
		if handlerErr != nil {
			return model.Response{}, handlerErr
		}
		if streamErr != nil {
			return model.Response{}, fmt.Errorf("stream model: %w", streamErr)
		}
		return response, nil
	})
	if err == nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return model.Response{}, contextErr
		}
		return response, nil
	}
	if handlerErr != nil {
		return model.Response{}, handlerErr
	}
	if errors.Is(err, model.ErrStreamingUnsupported) && !delivered {
		if contextErr := ctx.Err(); contextErr != nil {
			return model.Response{}, contextErr
		}
		response, completeErr := r.invokeCompleteModel(ctx, invocation)
		if completeErr != nil {
			return model.Response{}, completeErr
		}
		if contextErr := ctx.Err(); contextErr != nil {
			return model.Response{}, contextErr
		}
		return response, nil
	}
	return model.Response{}, err
}

func (r *runtime) invokeCompleteModel(ctx context.Context, request model.Request) (model.Response, error) {
	return r.observeModelInvocation(ctx, request, func() (model.Response, error) {
		response, err := r.model.Complete(ctx, cloneModelRequest(request))
		if err != nil {
			return model.Response{}, fmt.Errorf("complete model: %w", err)
		}
		return response, nil
	})
}

func (r *runtime) observeModelInvocation(
	ctx context.Context,
	request model.Request,
	invoke func() (model.Response, error),
) (response model.Response, resultErr error) {
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
	response, resultErr = invoke()
	if resultErr != nil {
		return model.Response{}, resultErr
	}
	if err := validateAssistant(response.Message); err != nil {
		return model.Response{}, err
	}
	response = cloneModelResponse(response)
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
