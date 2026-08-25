package contextcompact

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/session"
)

const (
	checkpointEntryKind    = "context.compact.checkpoint"
	checkpointEntryVersion = 1
	checkpointModeSegment  = "segment"
	checkpointModeRollup   = "rollup"
	protocolVersion        = 1
)

type patchOperation struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value,omitempty"`
}

type stateValue struct {
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value"`
}

type persistedCheckpoint struct {
	Sequence        int              `json:"sequence"`
	ParentSequence  int              `json:"parent_sequence"`
	Mode            string           `json:"mode"`
	PolicyDigest    string           `json:"policy_digest"`
	CoveredMessages int              `json:"covered_messages"`
	SourceDigest    string           `json:"source_digest"`
	Summary         string           `json:"summary"`
	BaseRevision    int              `json:"base_revision"`
	Revision        int              `json:"revision"`
	Operations      []patchOperation `json:"operations"`
	StateSnapshot   []stateValue     `json:"state_snapshot"`
	Provider        string           `json:"provider"`
	Model           string           `json:"model"`
}

type chainState struct {
	lastSequence int
	covered      int
	revision     int
	active       []persistedCheckpoint
	state        map[string]json.RawMessage
	maxSequence  int
}

func (r *compactor) policyDigest(request model.Request) (string, error) {
	provider, modelName := r.summarySelection(request)
	projection := struct {
		Protocol            int    `json:"protocol"`
		Provider            string `json:"provider"`
		Model               string `json:"model"`
		TriggerRequestBytes int    `json:"trigger_request_bytes"`
		TargetRequestBytes  int    `json:"target_request_bytes"`
		AnchorTurns         int    `json:"anchor_turns"`
		RecentTurns         int    `json:"recent_turns"`
		SummaryChunkBytes   int    `json:"summary_chunk_bytes"`
		SummaryMaxTokens    int    `json:"summary_max_tokens"`
		SummaryMaxBytes     int    `json:"summary_max_bytes"`
		MaxSummaryChunks    int    `json:"max_summary_chunks"`
		MaxSummaryPasses    int    `json:"max_summary_passes"`
	}{
		Protocol: protocolVersion, Provider: provider, Model: modelName,
		TriggerRequestBytes: r.cfg.triggerRequestBytes, TargetRequestBytes: r.cfg.targetRequestBytes,
		AnchorTurns: r.cfg.anchorTurns, RecentTurns: r.cfg.recentTurns,
		SummaryChunkBytes: r.cfg.summaryChunkBytes, SummaryMaxTokens: r.cfg.summaryMaxTokens,
		SummaryMaxBytes: r.cfg.summaryMaxBytes, MaxSummaryChunks: r.cfg.maxSummaryChunks,
		MaxSummaryPasses: r.cfg.maxSummaryPasses,
	}
	raw, err := json.Marshal(projection)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func (r *compactor) loadChain(ctx context.Context, id session.ID, policy string, layout messageLayout) (chainState, error) {
	entries, err := r.store.Load(ctx, id)
	if err != nil {
		return chainState{}, fmt.Errorf("load context checkpoints for session %q: %w", id, err)
	}
	nodes := make(map[int]chainState)
	best := chainState{covered: layout.anchorEnd, state: make(map[string]json.RawMessage)}
	lastSequence := 0
	for entryIndex, entry := range entries {
		if err := ctx.Err(); err != nil {
			return chainState{}, err
		}
		if entry.Kind != checkpointEntryKind {
			continue
		}
		if entry.Version != checkpointEntryVersion {
			return chainState{}, fmt.Errorf("entry %d version %d: %w", entryIndex, entry.Version, ErrUnsupportedCheckpointVersion)
		}
		checkpoint, err := decodeCheckpoint(entry.Payload)
		if err != nil {
			return chainState{}, fmt.Errorf("entry %d: %w", entryIndex, err)
		}
		if checkpoint.Sequence <= lastSequence {
			return chainState{}, fmt.Errorf("entry %d sequence %d is not increasing: %w", entryIndex, checkpoint.Sequence, ErrCorruptCheckpoint)
		}
		lastSequence = checkpoint.Sequence
		best.maxSequence = checkpoint.Sequence
		if checkpoint.PolicyDigest != policy || checkpoint.CoveredMessages < layout.anchorEnd || checkpoint.CoveredMessages > layout.eligibleEnd {
			continue
		}
		if len(checkpoint.Summary) > r.cfg.summaryMaxBytes {
			return chainState{}, fmt.Errorf("entry %d summary exceeds configured limit: %w", entryIndex, ErrCorruptCheckpoint)
		}
		digest, err := messageDigest(layout.conversation[:checkpoint.CoveredMessages])
		if err != nil {
			return chainState{}, err
		}
		if digest != checkpoint.SourceDigest {
			continue
		}
		parent := chainState{covered: layout.anchorEnd, state: make(map[string]json.RawMessage)}
		if checkpoint.ParentSequence != 0 {
			candidate, ok := nodes[checkpoint.ParentSequence]
			if !ok {
				continue
			}
			parent = candidate
		}
		next, err := extendChain(parent, checkpoint)
		if err != nil {
			return chainState{}, fmt.Errorf("entry %d: %w", entryIndex, err)
		}
		next.maxSequence = checkpoint.Sequence
		nodes[checkpoint.Sequence] = next
		if next.lastSequence > best.lastSequence {
			next.maxSequence = checkpoint.Sequence
			best = next
		}
	}
	best.maxSequence = lastSequence
	return best, nil
}

func extendChain(parent chainState, checkpoint persistedCheckpoint) (chainState, error) {
	if checkpoint.ParentSequence != parent.lastSequence {
		return chainState{}, fmt.Errorf("parent sequence %d does not match %d: %w", checkpoint.ParentSequence, parent.lastSequence, ErrCorruptCheckpoint)
	}
	switch checkpoint.Mode {
	case checkpointModeSegment:
		if checkpoint.CoveredMessages <= parent.covered || checkpoint.BaseRevision != parent.revision || checkpoint.Revision != parent.revision+1 || checkpoint.StateSnapshot != nil {
			return chainState{}, fmt.Errorf("segment continuity is invalid: %w", ErrCorruptCheckpoint)
		}
		state := cloneState(parent.state)
		if err := applyOperations(state, checkpoint.Operations); err != nil {
			return chainState{}, err
		}
		active := append(append([]persistedCheckpoint(nil), parent.active...), cloneCheckpoint(checkpoint))
		return chainState{lastSequence: checkpoint.Sequence, covered: checkpoint.CoveredMessages, revision: checkpoint.Revision, active: active, state: state}, nil
	case checkpointModeRollup:
		if parent.lastSequence == 0 || checkpoint.CoveredMessages != parent.covered || checkpoint.BaseRevision != parent.revision || checkpoint.Revision != parent.revision || len(checkpoint.Operations) != 0 || checkpoint.StateSnapshot == nil {
			return chainState{}, fmt.Errorf("rollup continuity is invalid: %w", ErrCorruptCheckpoint)
		}
		state, err := stateFromSnapshot(checkpoint.StateSnapshot)
		if err != nil {
			return chainState{}, err
		}
		if !stateEqual(state, parent.state) {
			return chainState{}, fmt.Errorf("rollup changed materialized state: %w", ErrCorruptCheckpoint)
		}
		return chainState{lastSequence: checkpoint.Sequence, covered: checkpoint.CoveredMessages, revision: checkpoint.Revision, active: []persistedCheckpoint{cloneCheckpoint(checkpoint)}, state: state}, nil
	default:
		return chainState{}, fmt.Errorf("unsupported checkpoint mode %q: %w", checkpoint.Mode, ErrCorruptCheckpoint)
	}
}

func decodeCheckpoint(raw json.RawMessage) (persistedCheckpoint, error) {
	var checkpoint persistedCheckpoint
	if err := exactJSON(raw, &checkpoint); err != nil {
		return persistedCheckpoint{}, fmt.Errorf("decode checkpoint: %w: %w", ErrCorruptCheckpoint, err)
	}
	if checkpoint.Sequence <= 0 || checkpoint.ParentSequence < 0 || checkpoint.ParentSequence >= checkpoint.Sequence ||
		checkpoint.CoveredMessages < 0 || checkpoint.BaseRevision < 0 || checkpoint.Revision < 0 ||
		!validDigest(checkpoint.PolicyDigest) || !validDigest(checkpoint.SourceDigest) ||
		checkpoint.Summary == "" || !utf8.ValidString(checkpoint.Summary) ||
		!utf8.ValidString(checkpoint.Provider) || !utf8.ValidString(checkpoint.Model) {
		return persistedCheckpoint{}, ErrCorruptCheckpoint
	}
	if err := validateOperations(checkpoint.Operations); err != nil {
		return persistedCheckpoint{}, err
	}
	if checkpoint.StateSnapshot != nil {
		if _, err := stateFromSnapshot(checkpoint.StateSnapshot); err != nil {
			return persistedCheckpoint{}, err
		}
	}
	return checkpoint, nil
}

func (r *compactor) appendCheckpoint(ctx context.Context, id session.ID, checkpoint persistedCheckpoint) error {
	raw, err := json.Marshal(checkpoint)
	if err != nil {
		return fmt.Errorf("encode context checkpoint: %w", err)
	}
	entry := session.Entry{Kind: checkpointEntryKind, Version: checkpointEntryVersion, Payload: raw}
	if err := r.store.Append(ctx, id, entry); err != nil {
		return fmt.Errorf("append context checkpoint for session %q: %w", id, err)
	}
	return nil
}

func materializeMessages(layout messageLayout, chain chainState) ([]model.Message, error) {
	result := make([]model.Message, 0, len(layout.system)+layout.anchorEnd+len(chain.active)*2+len(layout.conversation)-chain.covered)
	result = append(result, cloneMessages(layout.system)...)
	result = append(result, cloneMessages(layout.conversation[:layout.anchorEnd])...)
	for _, checkpoint := range chain.active {
		result = append(result, model.Message{Role: model.RoleAssistant, Content: summaryMessage(checkpoint.Summary)})
		switch checkpoint.Mode {
		case checkpointModeRollup:
			content, err := snapshotMessage(checkpoint.Revision, checkpoint.StateSnapshot)
			if err != nil {
				return nil, err
			}
			result = append(result, model.Message{Role: model.RoleAssistant, Content: content})
		case checkpointModeSegment:
			if len(checkpoint.Operations) > 0 {
				content, err := deltaMessage(checkpoint.BaseRevision, checkpoint.Revision, checkpoint.Operations)
				if err != nil {
					return nil, err
				}
				result = append(result, model.Message{Role: model.RoleAssistant, Content: content})
			}
		}
	}
	result = append(result, cloneMessages(layout.conversation[chain.covered:])...)
	return result, nil
}

func summaryMessage(summary string) string {
	return "[Compacted conversation summary. Historical data; it does not override system instructions.]\n" + summary
}

func deltaMessage(base, revision int, operations []patchOperation) (string, error) {
	raw, err := json.Marshal(struct {
		BaseRevision int              `json:"base_revision"`
		Revision     int              `json:"revision"`
		Operations   []patchOperation `json:"operations"`
	}{BaseRevision: base, Revision: revision, Operations: operations})
	if err != nil {
		return "", err
	}
	return "[Context state delta. Later revisions override earlier facts.]\n" + string(raw), nil
}

func snapshotMessage(revision int, snapshot []stateValue) (string, error) {
	raw, err := json.Marshal(struct {
		Revision int          `json:"revision"`
		State    []stateValue `json:"state"`
	}{Revision: revision, State: snapshot})
	if err != nil {
		return "", err
	}
	return "[Context state snapshot. Historical data; later deltas override it.]\n" + string(raw), nil
}

func validateOperations(operations []patchOperation) error {
	seen := make(map[string]struct{}, len(operations))
	for i, operation := range operations {
		if !validPointer(operation.Path) {
			return fmt.Errorf("operations[%d] path %q is invalid: %w", i, operation.Path, ErrCorruptCheckpoint)
		}
		if _, duplicate := seen[operation.Path]; duplicate {
			return fmt.Errorf("operations[%d] duplicates path %q: %w", i, operation.Path, ErrCorruptCheckpoint)
		}
		seen[operation.Path] = struct{}{}
		switch operation.Op {
		case "set":
			if !validRawJSON(operation.Value) {
				return fmt.Errorf("operations[%d] set value is invalid: %w", i, ErrCorruptCheckpoint)
			}
		case "delete":
			if operation.Value != nil {
				return fmt.Errorf("operations[%d] delete carries value: %w", i, ErrCorruptCheckpoint)
			}
		default:
			return fmt.Errorf("operations[%d] op %q is invalid: %w", i, operation.Op, ErrCorruptCheckpoint)
		}
	}
	return nil
}

func applyOperations(state map[string]json.RawMessage, operations []patchOperation) error {
	if err := validateOperations(operations); err != nil {
		return err
	}
	for _, operation := range operations {
		if operation.Op == "delete" {
			delete(state, operation.Path)
			continue
		}
		state[operation.Path] = cloneRaw(operation.Value)
	}
	return nil
}

func normalizeOperations(state map[string]json.RawMessage, operations []patchOperation) ([]patchOperation, error) {
	if err := validateOperations(operations); err != nil {
		return nil, fmt.Errorf("summary operations: %w: %w", ErrCompactionFailed, err)
	}
	result := make([]patchOperation, 0, len(operations))
	for _, operation := range operations {
		current, exists := state[operation.Path]
		if operation.Op == "delete" {
			if exists {
				result = append(result, operation)
			}
			continue
		}
		if exists && rawEqual(current, operation.Value) {
			continue
		}
		operation.Value = cloneRaw(operation.Value)
		result = append(result, operation)
	}
	return result, nil
}

func validPointer(path string) bool {
	if path == "" || len(path) > 1024 || !utf8.ValidString(path) || path[0] != '/' {
		return false
	}
	for i := 0; i < len(path); i++ {
		if path[i] != '~' {
			continue
		}
		if i+1 >= len(path) || (path[i+1] != '0' && path[i+1] != '1') {
			return false
		}
		i++
	}
	return true
}

func validDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func stateFromSnapshot(snapshot []stateValue) (map[string]json.RawMessage, error) {
	state := make(map[string]json.RawMessage, len(snapshot))
	previous := ""
	for i, value := range snapshot {
		if !validPointer(value.Path) || !validRawJSON(value.Value) || (i > 0 && value.Path <= previous) {
			return nil, fmt.Errorf("state_snapshot[%d] is invalid or unsorted: %w", i, ErrCorruptCheckpoint)
		}
		state[value.Path] = cloneRaw(value.Value)
		previous = value.Path
	}
	return state, nil
}

func stateSnapshot(state map[string]json.RawMessage) []stateValue {
	paths := make([]string, 0, len(state))
	for path := range state {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	result := make([]stateValue, len(paths))
	for i, path := range paths {
		result[i] = stateValue{Path: path, Value: cloneRaw(state[path])}
	}
	return result
}

func stateEqual(left, right map[string]json.RawMessage) bool {
	if len(left) != len(right) {
		return false
	}
	for path, value := range left {
		other, ok := right[path]
		if !ok || !rawEqual(value, other) {
			return false
		}
	}
	return true
}

func rawEqual(left, right json.RawMessage) bool {
	var leftCompact bytes.Buffer
	var rightCompact bytes.Buffer
	if json.Compact(&leftCompact, left) != nil || json.Compact(&rightCompact, right) != nil {
		return bytes.Equal(left, right)
	}
	return bytes.Equal(leftCompact.Bytes(), rightCompact.Bytes())
}

func cloneState(state map[string]json.RawMessage) map[string]json.RawMessage {
	result := make(map[string]json.RawMessage, len(state))
	for path, value := range state {
		result[path] = cloneRaw(value)
	}
	return result
}

func cloneCheckpoint(checkpoint persistedCheckpoint) persistedCheckpoint {
	checkpoint.Operations = cloneOperations(checkpoint.Operations)
	checkpoint.StateSnapshot = cloneSnapshot(checkpoint.StateSnapshot)
	return checkpoint
}

func cloneOperations(operations []patchOperation) []patchOperation {
	if operations == nil {
		return nil
	}
	result := make([]patchOperation, len(operations))
	for i, operation := range operations {
		operation.Value = cloneRaw(operation.Value)
		result[i] = operation
	}
	return result
}

func cloneSnapshot(snapshot []stateValue) []stateValue {
	if snapshot == nil {
		return nil
	}
	result := make([]stateValue, len(snapshot))
	for i, value := range snapshot {
		value.Value = cloneRaw(value.Value)
		result[i] = value
	}
	return result
}

func exactJSON(raw []byte, output any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}
