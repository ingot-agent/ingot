package agentdefault

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/tool"
)

const (
	agentMessageKind    = "agent.message"
	agentMessageVersion = 1
	interruptedContent  = "tool error [interrupted]: previous execution was interrupted; result unknown"
)

type persistedMessage struct {
	Role       string          `json:"role"`
	Content    string          `json:"content"`
	Name       string          `json:"name"`
	ToolCallID string          `json:"tool_call_id"`
	ToolCalls  []persistedCall `json:"tool_calls"`
}

type persistedCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type trailingRound struct {
	calls   []tool.Call
	matched int
}

func (r *runtime) loadHistory(ctx context.Context, id session.ID) ([]model.Message, error) {
	entries, err := r.store.Load(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("load session %q: %w", id, err)
	}
	messages := make([]model.Message, 0, len(entries))
	for i, entry := range entries {
		if entry.Kind != agentMessageKind {
			continue
		}
		if entry.Version != agentMessageVersion {
			return nil, fmt.Errorf("entry %d version %d: %w", i, entry.Version, ErrUnsupportedEntryVersion)
		}
		message, err := decodePersistedMessage(entry.Payload)
		if err != nil {
			return nil, fmt.Errorf("entry %d: %w", i, err)
		}
		messages = append(messages, message)
	}
	if _, err := inspectHistory(messages); err != nil {
		return nil, err
	}
	return messages, nil
}

func (r *runtime) recoverTrailingRound(ctx context.Context, id session.ID, history []model.Message) ([]model.Message, error) {
	round, err := inspectHistory(history)
	if err != nil {
		return nil, err
	}
	if round == nil || round.matched == len(round.calls) {
		return history, nil
	}
	result := cloneMessages(history)
	for _, call := range round.calls[round.matched:] {
		message := model.Message{Role: model.RoleTool, Content: interruptedContent, ToolCallID: call.ID}
		if err := r.appendMessage(ctx, id, message); err != nil {
			return nil, fmt.Errorf("recover tool call %q: %w", call.ID, err)
		}
		result = append(result, message)
	}
	return result, nil
}

func inspectHistory(messages []model.Message) (*trailingRound, error) {
	var round *trailingRound
	for i, message := range messages {
		if err := validateStoredMessage(message); err != nil {
			return nil, fmt.Errorf("message %d: %w", i, err)
		}
		if round != nil && round.matched < len(round.calls) {
			if message.Role != model.RoleTool {
				return nil, fmt.Errorf("message %d follows incomplete tool round: %w", i, ErrCorruptHistory)
			}
			expected := round.calls[round.matched]
			if message.ToolCallID != expected.ID {
				return nil, fmt.Errorf("message %d tool_call_id %q want %q: %w", i, message.ToolCallID, expected.ID, ErrCorruptHistory)
			}
			round.matched++
			continue
		}
		if message.Role == model.RoleTool {
			return nil, fmt.Errorf("message %d has no pending tool call: %w", i, ErrCorruptHistory)
		}
		if message.Role == model.RoleAssistant && len(message.ToolCalls) > 0 {
			round = &trailingRound{calls: cloneCalls(message.ToolCalls)}
		}
	}
	if round != nil && round.matched == len(round.calls) {
		return nil, nil
	}
	return round, nil
}

func validateStoredMessage(message model.Message) error {
	if !utf8.ValidString(message.Content) || !utf8.ValidString(message.Name) || !utf8.ValidString(message.ToolCallID) {
		return ErrCorruptHistory
	}
	switch message.Role {
	case model.RoleUser:
		if message.ToolCallID != "" || len(message.ToolCalls) != 0 {
			return ErrCorruptHistory
		}
	case model.RoleAssistant:
		if message.ToolCallID != "" {
			return ErrCorruptHistory
		}
		if err := validateAssistant(message); err != nil {
			return fmt.Errorf("%w: %v", ErrCorruptHistory, err)
		}
	case model.RoleTool:
		if message.ToolCallID == "" || len(message.ToolCalls) != 0 {
			return ErrCorruptHistory
		}
	default:
		return ErrCorruptHistory
	}
	return nil
}

func (r *runtime) appendMessage(ctx context.Context, id session.ID, message model.Message) error {
	payload, err := encodePersistedMessage(message)
	if err != nil {
		return err
	}
	return r.store.Append(ctx, id, session.Entry{Kind: agentMessageKind, Version: agentMessageVersion, Payload: payload})
}

func encodePersistedMessage(message model.Message) (json.RawMessage, error) {
	if err := validateStoredMessage(message); err != nil {
		return nil, err
	}
	persisted := persistedMessage{
		Role: string(message.Role), Content: message.Content, Name: message.Name,
		ToolCallID: message.ToolCallID, ToolCalls: make([]persistedCall, len(message.ToolCalls)),
	}
	for i, call := range message.ToolCalls {
		persisted.ToolCalls[i] = persistedCall{ID: call.ID, Name: call.Name, Arguments: append(json.RawMessage(nil), call.Arguments...)}
	}
	raw, err := json.Marshal(persisted)
	if err != nil {
		return nil, fmt.Errorf("encode agent message: %w", err)
	}
	return raw, nil
}

func decodePersistedMessage(raw json.RawMessage) (model.Message, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var persisted persistedMessage
	if err := decoder.Decode(&persisted); err != nil {
		return model.Message{}, fmt.Errorf("decode payload: %w: %w", ErrCorruptHistory, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return model.Message{}, fmt.Errorf("payload has multiple values: %w", ErrCorruptHistory)
	}
	message := model.Message{
		Role: model.Role(persisted.Role), Content: persisted.Content, Name: persisted.Name,
		ToolCallID: persisted.ToolCallID, ToolCalls: make([]tool.Call, len(persisted.ToolCalls)),
	}
	for i, call := range persisted.ToolCalls {
		message.ToolCalls[i] = tool.Call{ID: call.ID, Name: call.Name, Arguments: append(json.RawMessage(nil), call.Arguments...)}
	}
	if err := validateStoredMessage(message); err != nil {
		return model.Message{}, err
	}
	return message, nil
}

func cloneDefinitions(definitions []tool.Definition) []tool.Definition {
	result := make([]tool.Definition, len(definitions))
	for i, definition := range definitions {
		result[i] = tool.Definition{Name: definition.Name, Description: definition.Description, InputSchema: append(json.RawMessage(nil), definition.InputSchema...)}
	}
	return result
}

func cloneModelRequest(request model.Request) model.Request {
	request.Messages = cloneMessages(request.Messages)
	request.Tools = cloneDefinitions(request.Tools)
	request.Temperature = copyFloat(request.Temperature)
	request.MaxTokens = copyInt(request.MaxTokens)
	request.Stop = append([]string(nil), request.Stop...)
	return request
}

func cloneMessages(messages []model.Message) []model.Message {
	result := make([]model.Message, len(messages))
	for i, message := range messages {
		result[i] = cloneMessage(message)
	}
	return result
}

func cloneMessage(message model.Message) model.Message {
	message.ToolCalls = cloneCalls(message.ToolCalls)
	return message
}

func cloneCalls(calls []tool.Call) []tool.Call {
	result := make([]tool.Call, len(calls))
	for i, call := range calls {
		result[i] = cloneCall(call)
	}
	return result
}

func cloneCall(call tool.Call) tool.Call {
	call.Arguments = append(json.RawMessage(nil), call.Arguments...)
	return call
}
