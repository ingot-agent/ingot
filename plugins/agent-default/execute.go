package agentdefault

import (
	"context"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/contextwindow"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/pipeline"
	"github.com/ingot-agent/sdk/prompt"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/tool"
)

func (r *runtime) execute(ctx context.Context, turn agent.Turn, handler agent.StreamHandler) (agent.Result, error) {
	if ctx == nil {
		return agent.Result{}, fmt.Errorf("run agent: nil context: %w", ErrInvalidTurn)
	}
	if turn.SessionID == "" || !utf8.ValidString(string(turn.SessionID)) || !utf8.ValidString(turn.Input) {
		return agent.Result{}, ErrInvalidTurn
	}
	if err := content.ValidateAttachments(turn.Attachments); err != nil {
		return agent.Result{}, fmt.Errorf("attachments: %w: %w", ErrInvalidTurn, err)
	}
	if err := ctx.Err(); err != nil {
		return agent.Result{}, err
	}
	release, err := r.gates.acquire(ctx, string(turn.SessionID))
	if err != nil {
		return agent.Result{}, err
	}
	defer release()

	// Retain consumer failure even if a model or turn interceptor swallows it
	// or attempts to invoke its terminal again.
	var handlerErr error
	if handler != nil {
		consumer := handler
		handler = func(event agent.StreamEvent) error {
			if handlerErr == nil {
				handlerErr = ctx.Err()
			}
			if handlerErr == nil {
				handlerErr = consumer(event)
			}
			return handlerErr
		}
	}
	originalSessionID := turn.SessionID
	terminal := func(callCtx context.Context, selected agent.Turn) (agent.Result, error) {
		if handlerErr != nil {
			return agent.Result{}, handlerErr
		}
		if selected.SessionID != originalSessionID {
			return agent.Result{}, fmt.Errorf("agent interceptor changed session id from %q to %q: %w", originalSessionID, selected.SessionID, ErrInvalidTurn)
		}
		return r.runTurn(callCtx, selected, handler)
	}
	next := pipeline.Compose[agent.Turn, agent.Result](terminal, r.interceptors...)
	owned := turn
	owned.Attachments = content.CloneAttachments(turn.Attachments)
	result, err := next(ctx, owned)
	if handlerErr != nil {
		return agent.Result{}, handlerErr
	}
	if err != nil {
		return agent.Result{}, err
	}
	if err := content.Validate(result.Output); err != nil {
		return agent.Result{}, fmt.Errorf("agent result: %w: %w", ErrInvalidModelMessage, err)
	}
	return agent.Result{Output: content.Clone(result.Output)}, nil
}

func (r *runtime) runTurn(ctx context.Context, turn agent.Turn, handler agent.StreamHandler) (agent.Result, error) {
	if err := ctx.Err(); err != nil {
		return agent.Result{}, err
	}
	history, err := r.loadHistory(ctx, turn.SessionID)
	if err != nil {
		return agent.Result{}, err
	}
	history, err = r.recoverTrailingRound(ctx, turn.SessionID, history)
	if err != nil {
		return agent.Result{}, err
	}
	if !utf8.ValidString(turn.Input) {
		return agent.Result{}, ErrInvalidTurn
	}
	if err := content.ValidateAttachments(turn.Attachments); err != nil {
		return agent.Result{}, fmt.Errorf("attachments: %w: %w", ErrInvalidTurn, err)
	}
	input := content.FromInput(turn.Input, turn.Attachments)
	user, err := r.appendMessage(ctx, turn.SessionID, model.Message{Role: model.RoleUser, Content: input})
	if err != nil {
		return agent.Result{}, fmt.Errorf("append user message: %w", err)
	}
	messages, err := r.prompt.Render(ctx, prompt.Request{SessionID: turn.SessionID, Input: content.Clone(user.Content), History: cloneMessages(history)})
	if err != nil {
		return agent.Result{}, fmt.Errorf("render prompt: %w", err)
	}
	messages = cloneMessages(messages)
	definitions := cloneDefinitions(r.tools.Definitions())
	rounds := 0
	for {
		request := model.Request{
			Provider: r.provider, Model: r.modelName, Messages: cloneMessages(messages), Tools: cloneDefinitions(definitions),
			Temperature: copyFloat(r.temperature), MaxTokens: copyInt(r.maxTokens),
		}
		request, err = r.compactRequest(ctx, turn.SessionID, request)
		if err != nil {
			return agent.Result{}, err
		}
		response, err := r.invokeModel(ctx, request, handler)
		if err != nil {
			return agent.Result{}, err
		}
		response.Message, err = r.appendMessage(ctx, turn.SessionID, response.Message)
		if err != nil {
			return agent.Result{}, fmt.Errorf("append assistant message: %w", err)
		}
		messages = append(messages, cloneMessage(response.Message))
		if len(response.Message.ToolCalls) == 0 {
			return agent.Result{Output: content.Clone(response.Message.Content)}, nil
		}
		rounds++
		if rounds > r.maxToolRounds {
			return agent.Result{}, ErrMaxToolRounds
		}
		for _, call := range response.Message.ToolCalls {
			if err := ctx.Err(); err != nil {
				return agent.Result{}, err
			}
			result, callErr := r.tools.Call(ctx, cloneCall(call))
			if callErr != nil {
				if errors.Is(callErr, context.Canceled) || errors.Is(callErr, context.DeadlineExceeded) {
					return agent.Result{}, callErr
				}
				if r.toolErrorMode == "fail" {
					return agent.Result{}, fmt.Errorf("tool %q call %q: %w", call.Name, call.ID, callErr)
				}
				result = tool.Result{Content: content.FromText(safeToolError(callErr))}
			}
			if err := content.Validate(result.Content); err != nil {
				return agent.Result{}, fmt.Errorf("tool %q returned invalid content: %w", call.Name, err)
			}
			message := model.Message{Role: model.RoleTool, Content: result.Content, ToolCallID: call.ID}
			message, err = r.appendMessage(ctx, turn.SessionID, message)
			if err != nil {
				return agent.Result{}, fmt.Errorf("append tool result for %q: %w", call.ID, err)
			}
			messages = append(messages, message)
		}
	}
}

func (r *runtime) compactRequest(ctx context.Context, sessionID session.ID, request model.Request) (model.Request, error) {
	if !r.compactor.Valid {
		return request, nil
	}
	result, err := r.compactor.Value.Compact(ctx, contextwindow.CompactionRequest{
		SessionID:  sessionID,
		Invocation: cloneModelRequest(request),
	})
	if err != nil {
		return model.Request{}, fmt.Errorf("compact model context: %w", err)
	}
	request.Messages = cloneMessages(result.Messages)
	return request, nil
}
