package agentdefault

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"unicode/utf8"

	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/contextwindow"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/observation"
	"github.com/ingot-agent/sdk/pipeline"
	"github.com/ingot-agent/sdk/prompt"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/tool"
)

func (r *runtime) execute(ctx context.Context, turn agent.Turn, handler agent.StreamHandler) (result agent.Result, resultErr error) {
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
	turnID, err := newTurnID()
	if err != nil {
		return agent.Result{}, fmt.Errorf("generate turn id: %w", err)
	}
	ctx = observation.WithCorrelation(ctx, observation.Correlation{SessionID: turn.SessionID, TurnID: turnID})
	r.observation.Emit(ctx, observation.TurnStarted{Turn: turn})
	defer func() {
		if recovered := recover(); recovered != nil {
			r.observation.Emit(ctx, observation.TurnFinished{Status: observation.StatusFailed, Error: fmt.Sprint(recovered)})
			panic(recovered)
		}
		finished := observation.TurnFinished{Status: terminalStatus(resultErr), Error: errorText(resultErr)}
		if resultErr == nil {
			finished.Result = &result
		}
		r.observation.Emit(ctx, finished)
	}()
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
	result, err = next(ctx, owned)
	if handlerErr != nil {
		return agent.Result{}, handlerErr
	}
	if err != nil {
		return agent.Result{}, err
	}
	if err := content.Validate(result.Output); err != nil {
		return agent.Result{}, fmt.Errorf("agent result: %w: %w", ErrInvalidModelMessage, err)
	}
	result = agent.Result{Output: content.Clone(result.Output)}
	return result, nil
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
	toolRounds := 0
	for roundIndex := 0; ; roundIndex++ {
		request := model.Request{
			Provider: r.provider, Model: r.modelName, Messages: cloneMessages(messages), Tools: cloneDefinitions(definitions),
			Temperature: copyFloat(r.temperature), MaxTokens: copyInt(r.maxTokens),
		}
		roundResult, roundErr := r.executeRound(ctx, turn.SessionID, roundIndex, toolRounds, request, handler)
		if roundErr != nil {
			return agent.Result{}, roundErr
		}
		messages = append(messages, cloneMessage(roundResult.Decision))
		messages = append(messages, cloneMessages(roundResult.ToolMessages)...)
		if len(roundResult.Decision.ToolCalls) == 0 {
			return agent.Result{Output: content.Clone(roundResult.Decision.Content)}, nil
		}
		toolRounds++
	}
}

func (r *runtime) executeRound(
	ctx context.Context,
	sessionID session.ID,
	index int,
	completedToolRounds int,
	request model.Request,
	handler agent.StreamHandler,
) (result agent.RoundResult, resultErr error) {
	correlation, _ := observation.CorrelationFromContext(ctx)
	correlation.RoundIndex = index
	correlation.ToolCallID = ""
	ctx = observation.WithCorrelation(ctx, correlation)
	r.observation.Emit(ctx, observation.RoundStarted{})
	defer func() {
		if recovered := recover(); recovered != nil {
			r.observation.Emit(ctx, observation.RoundFinished{Status: observation.StatusFailed, Error: fmt.Sprint(recovered)})
			panic(recovered)
		}
		finished := observation.RoundFinished{Status: terminalStatus(resultErr), Error: errorText(resultErr)}
		if resultErr == nil {
			finished.Result = &result
		}
		r.observation.Emit(ctx, finished)
	}()

	request, err := r.compactRequest(ctx, sessionID, request)
	if err != nil {
		return agent.RoundResult{}, err
	}
	response, err := r.invokeModel(ctx, request, handler)
	if err != nil {
		return agent.RoundResult{}, err
	}
	original := agent.Round{
		SessionID: sessionID, Index: index, Invocation: cloneModelRequest(request),
		Response: cloneModelResponse(response), Decision: cloneMessage(response.Message),
	}
	terminalCalled := false
	var terminalResult agent.RoundResult
	var terminalErr error
	terminal := func(callCtx context.Context, selected agent.Round) (agent.RoundResult, error) {
		if terminalCalled {
			return agent.RoundResult{}, fmt.Errorf("round terminal invoked more than once: %w", agent.ErrInvalidRound)
		}
		terminalCalled = true
		if err := validateRound(selected, original); err != nil {
			terminalErr = err
			return agent.RoundResult{}, err
		}
		terminalResult, terminalErr = r.executeRoundDecision(callCtx, selected, completedToolRounds)
		return cloneRoundResult(terminalResult), terminalErr
	}
	next := pipeline.Compose[agent.Round, agent.RoundResult](terminal, r.roundInterceptors...)
	result, err = next(ctx, cloneRound(original))
	if !terminalCalled {
		if err != nil {
			return agent.RoundResult{}, err
		}
		return agent.RoundResult{}, fmt.Errorf("round interceptor short-circuited without durable execution: %w", agent.ErrInvalidRoundResult)
	}
	if terminalErr != nil && err == nil {
		return agent.RoundResult{}, fmt.Errorf("round interceptor replaced terminal failure: %w", agent.ErrInvalidRoundResult)
	}
	if err != nil {
		return agent.RoundResult{}, err
	}
	if !reflect.DeepEqual(cloneRoundResult(result), cloneRoundResult(terminalResult)) {
		return agent.RoundResult{}, fmt.Errorf("round interceptor rewrote durable result: %w", agent.ErrInvalidRoundResult)
	}
	return cloneRoundResult(result), nil
}

func (r *runtime) executeRoundDecision(ctx context.Context, round agent.Round, completedToolRounds int) (agent.RoundResult, error) {
	decision, err := r.appendMessage(ctx, round.SessionID, round.Decision)
	if err != nil {
		return agent.RoundResult{}, fmt.Errorf("append assistant message: %w", err)
	}
	result := agent.RoundResult{Decision: cloneMessage(decision)}
	if len(decision.ToolCalls) == 0 {
		return result, nil
	}
	if completedToolRounds >= r.maxToolRounds {
		return agent.RoundResult{}, ErrMaxToolRounds
	}
	result.ToolMessages = make([]model.Message, 0, len(decision.ToolCalls))
	for _, call := range decision.ToolCalls {
		if err := ctx.Err(); err != nil {
			return agent.RoundResult{}, err
		}
		toolResult, callErr := r.executeTool(ctx, call)
		if callErr != nil {
			if errors.Is(callErr, context.Canceled) || errors.Is(callErr, context.DeadlineExceeded) {
				return agent.RoundResult{}, callErr
			}
			if r.toolErrorMode == "fail" {
				return agent.RoundResult{}, fmt.Errorf("tool %q call %q: %w", call.Name, call.ID, callErr)
			}
			toolResult = tool.Result{Content: content.FromText(safeToolError(callErr))}
		}
		message := model.Message{Role: model.RoleTool, Content: toolResult.Content, ToolCallID: call.ID}
		message, err = r.appendMessage(ctx, round.SessionID, message)
		if err != nil {
			return agent.RoundResult{}, fmt.Errorf("append tool result for %q: %w", call.ID, err)
		}
		result.ToolMessages = append(result.ToolMessages, cloneMessage(message))
	}
	return result, nil
}

func (r *runtime) executeTool(ctx context.Context, call tool.Call) (result tool.Result, resultErr error) {
	correlation, _ := observation.CorrelationFromContext(ctx)
	correlation.ToolCallID = call.ID
	ctx = observation.WithCorrelation(ctx, correlation)
	r.observation.Emit(ctx, observation.ToolStarted{Call: call})
	defer func() {
		if recovered := recover(); recovered != nil {
			r.observation.Emit(ctx, observation.ToolFinished{Status: observation.StatusFailed, Error: fmt.Sprint(recovered)})
			panic(recovered)
		}
		finished := observation.ToolFinished{Status: terminalStatus(resultErr), Error: errorText(resultErr)}
		if resultErr == nil {
			finished.Result = &result
		}
		r.observation.Emit(ctx, finished)
	}()
	result, resultErr = r.tools.Call(ctx, cloneCall(call))
	if resultErr != nil {
		return tool.Result{}, resultErr
	}
	if err := content.Validate(result.Content); err != nil {
		return tool.Result{}, fmt.Errorf("tool %q returned invalid content: %w", call.Name, err)
	}
	result.Content = content.Clone(result.Content)
	return result, nil
}

func validateRound(selected, original agent.Round) error {
	if selected.SessionID != original.SessionID || selected.Index != original.Index ||
		!reflect.DeepEqual(selected.Invocation, original.Invocation) || !reflect.DeepEqual(selected.Response, original.Response) {
		return agent.ErrInvalidRound
	}
	if selected.Decision.ToolCallID != "" {
		return agent.ErrInvalidRoundDecision
	}
	if err := validateAssistant(selected.Decision); err != nil {
		return fmt.Errorf("%w: %v", agent.ErrInvalidRoundDecision, err)
	}
	return nil
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
