package agentdefault

import (
	"sort"
	"sync"

	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/model"
)

type modelKey struct {
	provider string
	model    string
}

type tokenAccounting struct {
	input      int64
	output     int64
	total      int64
	reported   int
	unresolved int
	attempts   int
}

func (a *tokenAccounting) start() {
	a.attempts++
	a.unresolved++
}

func (a *tokenAccounting) settleNoUsage() {
	a.unresolved--
}

func (a *tokenAccounting) settleUsage(usage model.Usage) {
	if !usage.Reported {
		return
	}
	a.unresolved--
	a.reported++
	a.input += int64(usage.InputTokens)
	a.output += int64(usage.OutputTokens)
	a.total += int64(usage.TotalTokens)
}

func (a tokenAccounting) snapshot() agent.TokenUsage {
	coverage := agent.UsageUnavailable
	switch {
	case a.attempts > 0 && a.unresolved == 0:
		coverage = agent.UsageComplete
	case a.reported > 0:
		coverage = agent.UsagePartial
	}
	return agent.TokenUsage{
		InputTokens: a.input, OutputTokens: a.output, TotalTokens: a.total, Coverage: coverage,
	}
}

type mutableModelAccounting struct {
	completed int
	usage     tokenAccounting
}

type turnAccounting struct {
	mu sync.Mutex

	rounds           int
	modelInvocations int
	toolCalls        int
	usage            tokenAccounting
	models           map[modelKey]*mutableModelAccounting
}

type modelAttempt struct {
	settled bool
}

func newTurnAccounting() *turnAccounting {
	return &turnAccounting{models: make(map[modelKey]*mutableModelAccounting)}
}

func (a *turnAccounting) roundStarted() {
	a.mu.Lock()
	a.rounds++
	a.mu.Unlock()
}

func (a *turnAccounting) modelStarted() *modelAttempt {
	a.mu.Lock()
	a.modelInvocations++
	a.usage.start()
	a.mu.Unlock()
	return &modelAttempt{}
}

func (a *turnAccounting) modelFinished(attempt *modelAttempt, response model.Response, err error, rejectedWithoutUsage bool) {
	if attempt == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if attempt.settled {
		return
	}
	attempt.settled = true
	if rejectedWithoutUsage {
		a.usage.settleNoUsage()
		return
	}
	if err != nil {
		return
	}
	a.usage.settleUsage(response.Usage)
	key := modelKey{provider: response.Provider, model: response.Model}
	detail := a.models[key]
	if detail == nil {
		detail = &mutableModelAccounting{}
		a.models[key] = detail
	}
	detail.completed++
	detail.usage.start()
	detail.usage.settleUsage(response.Usage)
}

func (a *turnAccounting) toolStarted() {
	a.mu.Lock()
	a.toolCalls++
	a.mu.Unlock()
}

func (a *turnAccounting) snapshot() agent.Accounting {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := agent.Accounting{
		Rounds: a.rounds, ModelInvocations: a.modelInvocations, ToolCalls: a.toolCalls,
		Usage: a.usage.snapshot(),
	}
	if len(a.models) != 0 {
		result.Models = make([]agent.ModelAccounting, 0, len(a.models))
	}
	for key, detail := range a.models {
		result.Models = append(result.Models, agent.ModelAccounting{
			Provider: key.provider, Model: key.model,
			CompletedInvocations: detail.completed, Usage: detail.usage.snapshot(),
		})
	}
	sort.Slice(result.Models, func(i, j int) bool {
		if result.Models[i].Provider != result.Models[j].Provider {
			return result.Models[i].Provider < result.Models[j].Provider
		}
		return result.Models[i].Model < result.Models[j].Model
	})
	return result
}
