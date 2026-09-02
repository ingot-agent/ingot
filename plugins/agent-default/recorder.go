package agentdefault

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/observation"
)

type recorderContextKey struct{}

type executionRecorder struct {
	startedAt   time.Time
	accounting  *turnAccounting
	observation observation.Consumer

	mu         sync.Mutex
	failure    *agent.Failure
	failureErr error
}

func newExecutionRecorder(consumer observation.Consumer) *executionRecorder {
	return &executionRecorder{startedAt: time.Now(), accounting: newTurnAccounting(), observation: consumer}
}

func withExecutionRecorder(ctx context.Context, recorder *executionRecorder) context.Context {
	return context.WithValue(ctx, recorderContextKey{}, recorder)
}

func executionRecorderFrom(ctx context.Context) *executionRecorder {
	recorder, _ := ctx.Value(recorderContextKey{}).(*executionRecorder)
	return recorder
}

func (r *executionRecorder) emit(ctx context.Context, detail observation.Detail) {
	r.observation.Emit(ctx, detail)
}

func (r *executionRecorder) recordFailure(err error, stage agent.FailureStage, roundIndex *int, toolCallID string) {
	if err == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failure != nil && (errors.Is(err, r.failureErr) || errors.Is(r.failureErr, err)) {
		return
	}
	failure := &agent.Failure{Stage: stage, ToolCallID: toolCallID}
	if roundIndex != nil {
		index := *roundIndex
		failure.RoundIndex = &index
	}
	r.failure = failure
	r.failureErr = err
}

func (r *executionRecorder) finalize(result *agent.Result, err error) agent.Execution {
	outcome := agent.Outcome{
		Status: terminalOutcomeStatus(err), Duration: time.Since(r.startedAt), Accounting: r.accounting.snapshot(),
	}
	if err != nil {
		r.mu.Lock()
		if r.failure == nil {
			r.failure = &agent.Failure{Stage: agent.FailureTurnControl}
		}
		failure := *r.failure
		if r.failure.RoundIndex != nil {
			index := *r.failure.RoundIndex
			failure.RoundIndex = &index
		}
		r.mu.Unlock()
		outcome.Failure = &failure
	}
	execution := agent.Execution{Outcome: outcome}
	if err == nil && result != nil {
		execution.Result = &agent.Result{Output: content.Clone(result.Output)}
	}
	return execution
}

func cloneOutcome(value agent.Outcome) agent.Outcome {
	value.Accounting.Models = append([]agent.ModelAccounting(nil), value.Accounting.Models...)
	if value.Failure != nil {
		failure := *value.Failure
		if value.Failure.RoundIndex != nil {
			index := *value.Failure.RoundIndex
			failure.RoundIndex = &index
		}
		value.Failure = &failure
	}
	return value
}

func cloneResult(value *agent.Result) *agent.Result {
	if value == nil {
		return nil
	}
	return &agent.Result{Output: content.Clone(value.Output)}
}

func terminalOutcomeStatus(err error) agent.OutcomeStatus {
	if err == nil {
		return agent.OutcomeSucceeded
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return agent.OutcomeCanceled
	}
	return agent.OutcomeFailed
}

func roundIndexFrom(ctx context.Context) *int {
	correlation, ok := observation.CorrelationFromContext(ctx)
	if !ok {
		return nil
	}
	index := correlation.RoundIndex
	return &index
}

func restoreExecutionContext(ctx, source context.Context, recorder *executionRecorder) context.Context {
	if correlation, ok := observation.CorrelationFromContext(source); ok {
		ctx = observation.WithCorrelation(ctx, correlation)
	}
	return withExecutionRecorder(ctx, recorder)
}
