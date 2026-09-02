package agentdefault

import (
	"context"
	"fmt"
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

func (r *runtime) execute(ctx context.Context, turn agent.Turn, handler agent.StreamHandler) (result agent.Result, err error) {
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
		finished := observation.TurnFinished{Status: terminalStatus(err), Error: errorText(err)}
		if err == nil {
			finished.Result = &result
		}
		r.observation.Emit(ctx, finished)
	}()
	release, err := r.gates.acquire(ctx, string(turn.SessionID))
	if err != nil {
		return agent.Result{}, err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return agent.Result{}, err
	}

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
	if err := ctx.Err(); err != nil {
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
	if err := ctx.Err(); err != nil {
		return agent.Result{}, err
	}
	messages, err := r.prompt.Render(ctx, prompt.Request{SessionID: turn.SessionID, Input: content.Clone(user.Content), History: cloneMessages(history)})
	if err != nil {
		return agent.Result{}, fmt.Errorf("render prompt: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return agent.Result{}, err
	}
	messages = cloneMessages(messages)
	definitions := cloneDefinitions(r.tools.Definitions())
	for roundIndex := 0; roundIndex < r.maxRounds; roundIndex++ {
		result, err := r.observeRound(ctx, turn.SessionID, roundIndex, messages, definitions, handler, roundIndex == r.maxRounds-1)
		if err != nil {
			return agent.Result{}, err
		}
		messages = append(messages, cloneMessage(result.Decision))
		messages = append(messages, cloneMessages(result.ToolMessages)...)
		if len(result.Decision.ToolCalls) == 0 {
			return agent.Result{Output: content.Clone(result.Decision.Content)}, nil
		}
	}
	return agent.Result{}, ErrMaxRounds
}

func (r *runtime) observeRound(
	ctx context.Context,
	sessionID session.ID,
	index int,
	messages []model.Message,
	definitions []tool.Definition,
	handler agent.StreamHandler,
	lastAllowed bool,
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

	round, err := r.invokeRoundModel(ctx, sessionID, index, messages, definitions, handler)
	if err != nil {
		return agent.RoundResult{}, err
	}
	result, resultErr = r.executeRound(ctx, round, lastAllowed)
	return result, resultErr
}

func (r *runtime) compactRequest(ctx context.Context, sessionID session.ID, request model.Request) (model.Request, error) {
	if !r.compactor.Valid {
		return request, nil
	}
	if err := ctx.Err(); err != nil {
		return model.Request{}, err
	}
	result, err := r.compactor.Value.Compact(ctx, contextwindow.CompactionRequest{
		SessionID:  sessionID,
		Invocation: cloneModelRequest(request),
	})
	if err != nil {
		return model.Request{}, fmt.Errorf("compact model context: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return model.Request{}, err
	}
	request.Messages = cloneMessages(result.Messages)
	return request, nil
}
