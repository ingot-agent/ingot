package agentdefault_test

import (
	"context"
	"testing"

	agentdefault "github.com/ingot-agent/agent-default"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/pipeline"
)

type roundInterceptor struct{}

func (roundInterceptor) Invoke(
	ctx context.Context,
	round agent.Round,
	next pipeline.Next[agent.Round, agent.RoundResult],
) (agent.RoundResult, error) {
	return next(ctx, round)
}

func TestComponentContractIncludesRoundInterceptors(t *testing.T) {
	exports, _, err := agentdefault.New(context.Background(), agentdefault.Config{MaxRounds: 1}, agentdefault.Dependencies{
		Model: modelRuntime{}, Tools: toolRuntime{}, Store: sessionStore{}, Assets: assetStore{}, Prompt: promptRenderer{},
		RoundInterceptors: []agent.RoundInterceptor{roundInterceptor{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if exports.Runtime == nil {
		t.Fatal("runtime is nil")
	}
}
