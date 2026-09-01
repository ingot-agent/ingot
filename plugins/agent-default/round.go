package agentdefault

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/pipeline"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/tool"
)

// invokeRoundModel builds the actual invocation, applies compaction, invokes
// the model, and materializes an interceptor-owned Round snapshot.
func (r *runtime) invokeRoundModel(
	ctx context.Context,
	sessionID session.ID,
	index int,
	messages []model.Message,
	definitions []tool.Definition,
	handler agent.StreamHandler,
) (agent.Round, error) {
	request := model.Request{
		Provider:    r.provider,
		Model:       r.modelName,
		Messages:    cloneMessages(messages),
		Tools:       cloneDefinitions(definitions),
		Temperature: copyFloat(r.temperature),
		MaxTokens:   copyInt(r.maxTokens),
	}
	request, err := r.compactRequest(ctx, sessionID, request)
	if err != nil {
		return agent.Round{}, err
	}
	invocation := cloneModelRequest(request)
	response, err := r.invokeModel(ctx, cloneModelRequest(request), handler)
	if err != nil {
		return agent.Round{}, err
	}
	return agent.Round{
		SessionID:  sessionID,
		Index:      index,
		Invocation: invocation,
		Response:   cloneModelResponse(response),
		Decision:   cloneMessage(response.Message),
	}, nil
}

// executeRound applies round policy and performs canonical durable execution.
// Interceptors that call next may inspect, but must not rewrite, its committed
// result after next returns.
func (r *runtime) executeRound(ctx context.Context, round agent.Round, lastAllowed bool) (agent.RoundResult, error) {
	if round.Index < 0 || round.SessionID == "" || !reflect.DeepEqual(round.Decision, round.Response.Message) {
		return agent.RoundResult{}, ErrInvalidRound
	}
	original := cloneRound(round)
	var (
		terminalCalled bool
		terminalErr    error
		committed      *agent.RoundResult
	)

	terminal := func(callCtx context.Context, selected agent.Round) (agent.RoundResult, error) {
		if terminalCalled {
			terminalErr = ErrInvalidRoundResult
			return agent.RoundResult{}, terminalErr
		}
		terminalCalled = true
		if callCtx == nil {
			terminalErr = fmt.Errorf("nil round context: %w", ErrInvalidRound)
			return agent.RoundResult{}, terminalErr
		}
		if err := callCtx.Err(); err != nil {
			terminalErr = err
			return agent.RoundResult{}, err
		}
		if err := validateRoundIdentity(original, selected); err != nil {
			terminalErr = err
			return agent.RoundResult{}, err
		}
		if err := validateRoundDecisionMutation(original.Response.Message, selected.Decision); err != nil {
			terminalErr = err
			return agent.RoundResult{}, err
		}
		if lastAllowed && len(selected.Decision.ToolCalls) != 0 {
			terminalErr = ErrMaxRounds
			return agent.RoundResult{}, terminalErr
		}
		assistant, err := r.appendMessage(callCtx, selected.SessionID, selected.Decision)
		if err != nil {
			terminalErr = fmt.Errorf("append assistant message: %w", err)
			return agent.RoundResult{}, terminalErr
		}
		toolMessages, err := r.executeToolCalls(callCtx, selected.SessionID, assistant.ToolCalls)
		if err != nil {
			terminalErr = err
			return agent.RoundResult{}, err
		}
		result := agent.RoundResult{Decision: assistant, ToolMessages: toolMessages}
		snapshot := cloneRoundResult(result)
		committed = &snapshot
		return cloneRoundResult(snapshot), nil
	}

	next := pipeline.Compose[agent.Round, agent.RoundResult](terminal, r.roundInterceptors...)
	result, err := next(ctx, cloneRound(round))
	if err != nil {
		return agent.RoundResult{}, err
	}
	if terminalErr != nil {
		return agent.RoundResult{}, terminalErr
	}
	if terminalCalled {
		if committed == nil || !reflect.DeepEqual(result, *committed) {
			return agent.RoundResult{}, ErrInvalidRoundResult
		}
		return cloneRoundResult(*committed), nil
	}

	if err := validateShortCircuitResult(result); err != nil {
		return agent.RoundResult{}, err
	}
	decision, err := r.appendMessage(ctx, round.SessionID, result.Decision)
	if err != nil {
		return agent.RoundResult{}, fmt.Errorf("append short-circuit assistant message: %w", err)
	}
	return agent.RoundResult{Decision: decision}, nil
}

func (r *runtime) executeToolCalls(ctx context.Context, sessionID session.ID, calls []tool.Call) ([]model.Message, error) {
	messages := make([]model.Message, 0, len(calls))
	for _, call := range calls {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result, callErr := r.tools.Call(ctx, cloneCall(call))
		if callErr != nil {
			if errors.Is(callErr, context.Canceled) || errors.Is(callErr, context.DeadlineExceeded) {
				return nil, callErr
			}
			if r.toolErrorMode == "fail" {
				return nil, fmt.Errorf("tool %q call %q: %w", call.Name, call.ID, callErr)
			}
			result = tool.Result{Content: content.FromText(safeToolError(callErr))}
		}
		if err := content.Validate(result.Content); err != nil {
			return nil, fmt.Errorf("tool %q returned invalid content: %w", call.Name, err)
		}
		message := model.Message{Role: model.RoleTool, Content: result.Content, ToolCallID: call.ID}
		message, err := r.appendMessage(ctx, sessionID, message)
		if err != nil {
			return nil, fmt.Errorf("append tool result for %q: %w", call.ID, err)
		}
		messages = append(messages, message)
	}
	return messages, nil
}

func validateRoundIdentity(original, selected agent.Round) error {
	if selected.SessionID != original.SessionID || selected.Index != original.Index ||
		!reflect.DeepEqual(selected.Invocation, original.Invocation) ||
		!reflect.DeepEqual(selected.Response, original.Response) {
		return ErrInvalidRound
	}
	return nil
}

func validateRoundDecisionMutation(original, decision model.Message) error {
	if decision.Role != original.Role || decision.Name != original.Name || decision.ToolCallID != original.ToolCallID {
		return ErrInvalidRoundDecision
	}
	originalIndex := 0
	for _, call := range decision.ToolCalls {
		for originalIndex < len(original.ToolCalls) && original.ToolCalls[originalIndex].ID != call.ID {
			originalIndex++
		}
		if originalIndex == len(original.ToolCalls) {
			return ErrInvalidRoundDecision
		}
		originalIndex++
	}
	if err := validateAssistant(decision); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRoundDecision, err)
	}
	return nil
}

func validateShortCircuitResult(result agent.RoundResult) error {
	if len(result.ToolMessages) != 0 || len(result.Decision.ToolCalls) != 0 {
		return ErrInvalidRoundResult
	}
	if err := validateAssistant(result.Decision); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRoundResult, err)
	}
	return nil
}

func cloneModelResponse(response model.Response) model.Response {
	response.Message = cloneMessage(response.Message)
	return response
}

func cloneRound(round agent.Round) agent.Round {
	round.Invocation = cloneModelRequest(round.Invocation)
	round.Response = cloneModelResponse(round.Response)
	round.Decision = cloneMessage(round.Decision)
	return round
}

func cloneRoundResult(result agent.RoundResult) agent.RoundResult {
	result.Decision = cloneMessage(result.Decision)
	result.ToolMessages = cloneMessages(result.ToolMessages)
	return result
}
