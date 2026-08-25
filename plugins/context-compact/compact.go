package contextcompact

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ingot-agent/sdk/contextwindow"
	"github.com/ingot-agent/sdk/model"
)

func (r *compactor) Compact(ctx context.Context, input contextwindow.CompactionRequest) (contextwindow.CompactionResult, error) {
	if ctx == nil {
		return contextwindow.CompactionResult{}, fmt.Errorf("compact context: nil context: %w", ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return contextwindow.CompactionResult{}, err
	}
	if input.SessionID == "" {
		return contextwindow.CompactionResult{}, fmt.Errorf("session_id is required: %w", ErrInvalidRequest)
	}
	request := cloneRequest(input.Invocation)
	layout, err := inspectRequest(request, r.cfg.anchorTurns, r.cfg.recentTurns)
	if err != nil {
		return contextwindow.CompactionResult{}, err
	}
	if _, err := canonicalRequestBytes(request); err != nil {
		return contextwindow.CompactionResult{}, fmt.Errorf("canonicalize invocation: %w: %w", ErrInvalidRequest, err)
	}
	release, err := r.gates.acquire(ctx, string(input.SessionID))
	if err != nil {
		return contextwindow.CompactionResult{}, err
	}
	defer release()

	policy, err := r.policyDigest(request)
	if err != nil {
		return contextwindow.CompactionResult{}, fmt.Errorf("compute compaction policy: %w", err)
	}
	chain, err := r.loadChain(ctx, input.SessionID, policy, layout)
	if err != nil {
		return contextwindow.CompactionResult{}, err
	}
	messages, err := materializeMessages(layout, chain)
	if err != nil {
		return contextwindow.CompactionResult{}, err
	}
	view := cloneRequest(request)
	view.Messages = messages
	currentBytes, err := requestSize(view)
	if err != nil {
		return contextwindow.CompactionResult{}, err
	}
	if currentBytes < r.cfg.triggerRequestBytes {
		return contextwindow.CompactionResult{Messages: cloneMessages(messages), Changed: chain.lastSequence != 0}, nil
	}

	passes := 0
	for currentBytes > r.cfg.targetRequestBytes {
		if err := ctx.Err(); err != nil {
			return contextwindow.CompactionResult{}, err
		}
		if passes >= r.cfg.maxSummaryPasses {
			return contextwindow.CompactionResult{}, fmt.Errorf("needed more than %d summary calls: %w", r.cfg.maxSummaryPasses, ErrContextUncompactable)
		}
		if len(chain.active) >= r.cfg.maxSummaryChunks && !(len(chain.active) == 1 && chain.active[0].Mode == checkpointModeRollup) {
			previousBytes := currentBytes
			checkpoint, next, err := r.prepareRollup(ctx, request, policy, layout, chain)
			if err != nil {
				return contextwindow.CompactionResult{}, err
			}
			messages, currentBytes, err = materializedSize(view, layout, next)
			if err != nil {
				return contextwindow.CompactionResult{}, err
			}
			if currentBytes >= previousBytes {
				return contextwindow.CompactionResult{}, fmt.Errorf("rollup did not reduce canonical request bytes: %w", ErrContextUncompactable)
			}
			if err := r.appendCheckpoint(ctx, input.SessionID, checkpoint); err != nil {
				return contextwindow.CompactionResult{}, err
			}
			chain = next
			passes++
			continue
		}

		start, end, err := selectSource(layout, chain.covered, r.cfg.summaryChunkBytes)
		if err != nil {
			return contextwindow.CompactionResult{}, err
		}
		previousBytes := currentBytes
		output, provider, modelName, err := r.summarizeSegment(ctx, request, chain.state, layout.conversation[start:end])
		if err != nil {
			return contextwindow.CompactionResult{}, err
		}
		digest, err := messageDigest(layout.conversation[:end])
		if err != nil {
			return contextwindow.CompactionResult{}, err
		}
		checkpoint := persistedCheckpoint{
			Sequence: chain.maxSequence + 1, ParentSequence: chain.lastSequence, Mode: checkpointModeSegment,
			PolicyDigest: policy, CoveredMessages: end, SourceDigest: digest, Summary: output.Summary,
			BaseRevision: chain.revision, Revision: chain.revision + 1,
			Operations: cloneOperations(output.Operations), StateSnapshot: nil,
			Provider: provider, Model: modelName,
		}
		next, err := extendChain(chain, checkpoint)
		if err != nil {
			return contextwindow.CompactionResult{}, err
		}
		next.maxSequence = checkpoint.Sequence
		messages, currentBytes, err = materializedSize(view, layout, next)
		if err != nil {
			return contextwindow.CompactionResult{}, err
		}
		if currentBytes >= previousBytes {
			return contextwindow.CompactionResult{}, fmt.Errorf("summary segment did not reduce canonical request bytes: %w", ErrContextUncompactable)
		}
		if err := r.appendCheckpoint(ctx, input.SessionID, checkpoint); err != nil {
			return contextwindow.CompactionResult{}, err
		}
		chain = next
		passes++
	}
	return contextwindow.CompactionResult{Messages: cloneMessages(messages), Changed: true}, nil
}

func (r *compactor) prepareRollup(
	ctx context.Context,
	request model.Request,
	policy string,
	layout messageLayout,
	chain chainState,
) (persistedCheckpoint, chainState, error) {
	summaries := make([]string, len(chain.active))
	for i, checkpoint := range chain.active {
		summaries[i] = checkpoint.Summary
	}
	output, provider, modelName, err := r.summarizeRollup(ctx, request, chain.state, summaries)
	if err != nil {
		return persistedCheckpoint{}, chainState{}, err
	}
	digest, err := messageDigest(layout.conversation[:chain.covered])
	if err != nil {
		return persistedCheckpoint{}, chainState{}, err
	}
	checkpoint := persistedCheckpoint{
		Sequence: chain.maxSequence + 1, ParentSequence: chain.lastSequence, Mode: checkpointModeRollup,
		PolicyDigest: policy, CoveredMessages: chain.covered, SourceDigest: digest, Summary: output.Summary,
		BaseRevision: chain.revision, Revision: chain.revision, Operations: []patchOperation{},
		StateSnapshot: stateSnapshot(chain.state), Provider: provider, Model: modelName,
	}
	next, err := extendChain(chain, checkpoint)
	if err != nil {
		return persistedCheckpoint{}, chainState{}, err
	}
	next.maxSequence = checkpoint.Sequence
	return checkpoint, next, nil
}

func requestSize(request model.Request) (int, error) {
	raw, err := canonicalRequestBytes(request)
	if err != nil {
		return 0, fmt.Errorf("canonicalize compacted invocation: %w", err)
	}
	return len(raw), nil
}

func materializedSize(base model.Request, layout messageLayout, chain chainState) ([]model.Message, int, error) {
	messages, err := materializeMessages(layout, chain)
	if err != nil {
		return nil, 0, err
	}
	request := cloneRequest(base)
	request.Messages = messages
	size, err := requestSize(request)
	return messages, size, err
}

func selectSource(layout messageLayout, covered, minimumBytes int) (int, int, error) {
	if covered < layout.anchorEnd || covered >= layout.eligibleEnd {
		return 0, 0, fmt.Errorf("no complete middle turn remains: %w", ErrContextUncompactable)
	}
	turnIndex := -1
	for i, turn := range layout.turns {
		if turn.start == covered {
			turnIndex = i
			break
		}
	}
	if turnIndex < 0 {
		return 0, 0, fmt.Errorf("covered message %d is not a turn boundary: %w", covered, ErrInvalidHistory)
	}
	end := covered
	for i := turnIndex; i < len(layout.turns); i++ {
		turn := layout.turns[i]
		if turn.end > layout.eligibleEnd {
			break
		}
		end = turn.end
		raw, err := jsonMessages(layout.conversation[covered:end])
		if err != nil {
			return 0, 0, err
		}
		if len(raw) >= minimumBytes {
			break
		}
	}
	if end == covered {
		return 0, 0, fmt.Errorf("no eligible complete turn remains: %w", ErrContextUncompactable)
	}
	return covered, end, nil
}

func jsonMessages(messages []model.Message) ([]byte, error) {
	return json.Marshal(projectMessages(messages))
}
