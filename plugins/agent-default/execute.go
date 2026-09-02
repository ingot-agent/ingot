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

func (r *runtime) execute(ctx context.Context, turn agent.Turn, handler agent.StreamHandler) (execution agent.Execution, err error) {
	if ctx == nil {
		return agent.Execution{}, fmt.Errorf("run agent: nil context: %w", ErrInvalidTurn)
	}
	if turn.SessionID == "" || !utf8.ValidString(string(turn.SessionID)) || !utf8.ValidString(turn.Input) {
		return agent.Execution{}, ErrInvalidTurn
	}
	if err := content.ValidateAttachments(turn.Attachments); err != nil {
		return agent.Execution{}, fmt.Errorf("attachments: %w: %w", ErrInvalidTurn, err)
	}
	if err := ctx.Err(); err != nil {
		return agent.Execution{}, err
	}
	turnID, err := newTurnID()
	if err != nil {
		return agent.Execution{}, fmt.Errorf("generate turn id: %w", err)
	}
	ctx = observation.WithCorrelation(ctx, observation.Correlation{SessionID: turn.SessionID, TurnID: turnID})
	recorder := newExecutionRecorder(r.observation)
	ctx = withExecutionRecorder(ctx, recorder)
	recorder.emit(ctx, observation.TurnStarted{Turn: turn})
	var result agent.Result
	defer func() {
		if recovered := recover(); recovered != nil {
			panicErr := fmt.Errorf("%v", recovered)
			execution = recorder.finalize(nil, panicErr)
			recorder.emit(ctx, observation.TurnFinished{
				Status: observation.StatusFailed, Outcome: cloneOutcome(execution.Outcome), Error: fmt.Sprint(recovered),
			})
			panic(recovered)
		}
		var canonical *agent.Result
		if err == nil {
			canonical = &result
		}
		execution = recorder.finalize(canonical, err)
		recorder.emit(ctx, observation.TurnFinished{
			Status: terminalStatus(err), Result: cloneResult(execution.Result), Outcome: cloneOutcome(execution.Outcome), Error: errorText(err),
		})
	}()
	release, err := r.gates.acquire(ctx, string(turn.SessionID))
	if err != nil {
		recorder.recordFailure(err, agent.FailureSessionGate, nil, "")
		return agent.Execution{}, err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		recorder.recordFailure(err, agent.FailureSessionGate, nil, "")
		return agent.Execution{}, err
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
			recorder.recordFailure(handlerErr, agent.FailureStreamConsumer, nil, "")
			return agent.Result{}, handlerErr
		}
		if selected.SessionID != originalSessionID {
			controlErr := fmt.Errorf("agent interceptor changed session id from %q to %q: %w", originalSessionID, selected.SessionID, ErrInvalidTurn)
			recorder.recordFailure(controlErr, agent.FailureTurnControl, nil, "")
			return agent.Result{}, controlErr
		}
		if callCtx == nil {
			controlErr := fmt.Errorf("nil turn context: %w", ErrInvalidTurn)
			recorder.recordFailure(controlErr, agent.FailureTurnControl, nil, "")
			return agent.Result{}, controlErr
		}
		callCtx = restoreExecutionContext(callCtx, ctx, recorder)
		return r.runTurn(callCtx, selected, handler)
	}
	next := pipeline.Compose[agent.Turn, agent.Result](terminal, r.interceptors...)
	owned := turn
	owned.Attachments = content.CloneAttachments(turn.Attachments)
	result, err = next(ctx, owned)
	if handlerErr != nil {
		recorder.recordFailure(handlerErr, agent.FailureStreamConsumer, nil, "")
		return agent.Execution{}, handlerErr
	}
	if err != nil {
		recorder.recordFailure(err, agent.FailureTurnControl, nil, "")
		return agent.Execution{}, err
	}
	if err := content.Validate(result.Output); err != nil {
		resultErr := fmt.Errorf("agent result: %w: %w", ErrInvalidModelMessage, err)
		recorder.recordFailure(resultErr, agent.FailureTurnControl, nil, "")
		return agent.Execution{}, resultErr
	}
	result = agent.Result{Output: content.Clone(result.Output)}
	return agent.Execution{}, nil
}

func (r *runtime) runTurn(ctx context.Context, turn agent.Turn, handler agent.StreamHandler) (agent.Result, error) {
	if err := ctx.Err(); err != nil {
		executionRecorderFrom(ctx).recordFailure(err, agent.FailureHistoryLoad, nil, "")
		return agent.Result{}, err
	}
	history, err := r.loadHistory(ctx, turn.SessionID)
	if err != nil {
		executionRecorderFrom(ctx).recordFailure(err, agent.FailureHistoryLoad, nil, "")
		return agent.Result{}, err
	}
	if err := ctx.Err(); err != nil {
		executionRecorderFrom(ctx).recordFailure(err, agent.FailureHistoryLoad, nil, "")
		return agent.Result{}, err
	}
	history, err = r.recoverTrailingRound(ctx, turn.SessionID, history)
	if err != nil {
		executionRecorderFrom(ctx).recordFailure(err, agent.FailureRecovery, nil, "")
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
		persistErr := fmt.Errorf("append user message: %w", err)
		executionRecorderFrom(ctx).recordFailure(persistErr, agent.FailureUserPersistence, nil, "")
		return agent.Result{}, persistErr
	}
	if err := ctx.Err(); err != nil {
		executionRecorderFrom(ctx).recordFailure(err, agent.FailureUserPersistence, nil, "")
		return agent.Result{}, err
	}
	messages, err := r.prompt.Render(ctx, prompt.Request{SessionID: turn.SessionID, Input: content.Clone(user.Content), History: cloneMessages(history)})
	if err != nil {
		promptErr := fmt.Errorf("render prompt: %w", err)
		executionRecorderFrom(ctx).recordFailure(promptErr, agent.FailurePrompt, nil, "")
		return agent.Result{}, promptErr
	}
	if err := ctx.Err(); err != nil {
		executionRecorderFrom(ctx).recordFailure(err, agent.FailurePrompt, nil, "")
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
	lastRound := r.maxRounds - 1
	executionRecorderFrom(ctx).recordFailure(ErrMaxRounds, agent.FailureRoundControl, &lastRound, "")
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
	recorder := executionRecorderFrom(ctx)
	recorder.accounting.roundStarted()
	recorder.emit(ctx, observation.RoundStarted{})
	defer func() {
		if recovered := recover(); recovered != nil {
			recorder.emit(ctx, observation.RoundFinished{Status: observation.StatusFailed, Error: fmt.Sprint(recovered)})
			panic(recovered)
		}
		finished := observation.RoundFinished{Status: terminalStatus(resultErr), Error: errorText(resultErr)}
		if resultErr == nil {
			finished.Result = &result
		}
		recorder.emit(ctx, finished)
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
		executionRecorderFrom(ctx).recordFailure(err, agent.FailureCompaction, roundIndexFrom(ctx), "")
		return model.Request{}, err
	}
	result, err := r.compactor.Value.Compact(ctx, contextwindow.CompactionRequest{
		SessionID:  sessionID,
		Invocation: cloneModelRequest(request),
	})
	if err != nil {
		correlation, _ := observation.CorrelationFromContext(ctx)
		compactionErr := fmt.Errorf("compact model context: %w", err)
		executionRecorderFrom(ctx).recordFailure(compactionErr, agent.FailureCompaction, &correlation.RoundIndex, "")
		return model.Request{}, compactionErr
	}
	if err := ctx.Err(); err != nil {
		executionRecorderFrom(ctx).recordFailure(err, agent.FailureCompaction, roundIndexFrom(ctx), "")
		return model.Request{}, err
	}
	request.Messages = cloneMessages(result.Messages)
	return request, nil
}
