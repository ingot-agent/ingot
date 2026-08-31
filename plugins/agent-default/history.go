package agentdefault

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/ingot-agent/sdk/content"
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
	Content    []persistedPart `json:"content"`
	Name       string          `json:"name"`
	ToolCallID string          `json:"tool_call_id"`
	ToolCalls  []persistedCall `json:"tool_calls"`
}

type persistedPart struct {
	Kind  string          `json:"kind"`
	Text  string          `json:"text,omitempty"`
	Media *persistedMedia `json:"media,omitempty"`
}

type persistedMedia struct {
	MIMEType persistedOpaqueString `json:"mime_type"`
	Name     string                `json:"name"`
	Source   persistedSource       `json:"source"`
}

type persistedSource struct {
	Kind  string                `json:"kind"`
	Data  []byte                `json:"data,omitempty"`
	URI   persistedOpaqueString `json:"uri,omitempty"`
	Asset *persistedAsset       `json:"asset,omitempty"`
}

type persistedAsset struct {
	ID persistedOpaqueString `json:"id"`
}

// persistedOpaqueString keeps common UTF-8 values readable while preserving
// every byte of SDK strings whose format and character set are intentionally
// opaque. The object form is canonical only for invalid UTF-8 values.
type persistedOpaqueString string

func (value persistedOpaqueString) MarshalJSON() ([]byte, error) {
	text := string(value)
	if utf8.ValidString(text) {
		return json.Marshal(text)
	}
	return json.Marshal(struct {
		Bytes []byte `json:"bytes"`
	}{Bytes: []byte(text)})
}

func (value *persistedOpaqueString) UnmarshalJSON(raw []byte) error {
	if value == nil {
		return errors.New("decode opaque string into nil destination")
	}
	if !utf8.Valid(raw) {
		return errors.New("opaque string JSON is not valid UTF-8")
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return err
		}
		*value = persistedOpaqueString(text)
		return nil
	}
	var encoded struct {
		Bytes *[]byte `json:"bytes"`
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&encoded); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("opaque string contains multiple JSON values")
	}
	if encoded.Bytes == nil || utf8.Valid(*encoded.Bytes) {
		return errors.New("opaque string byte form must contain invalid UTF-8")
	}
	*value = persistedOpaqueString(string(*encoded.Bytes))
	return nil
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
		message := model.Message{Role: model.RoleTool, Content: content.FromText(interruptedContent), ToolCallID: call.ID}
		message, err = r.appendMessage(ctx, id, message)
		if err != nil {
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
	if !utf8.ValidString(message.Name) || !utf8.ValidString(message.ToolCallID) {
		return ErrCorruptHistory
	}
	if err := content.Validate(message.Content); err != nil {
		return fmt.Errorf("%w: %v", ErrCorruptHistory, err)
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

func (r *runtime) appendMessage(ctx context.Context, id session.ID, message model.Message) (model.Message, error) {
	materialized, err := r.materializeContent(ctx, message.Content)
	if err != nil {
		return model.Message{}, err
	}
	message = cloneMessage(message)
	message.Content = materialized
	payload, err := encodePersistedMessage(message)
	if err != nil {
		return model.Message{}, err
	}
	if err := r.store.Append(ctx, id, session.Entry{Kind: agentMessageKind, Version: agentMessageVersion, Payload: payload}); err != nil {
		return model.Message{}, err
	}
	return message, nil
}

func encodePersistedMessage(message model.Message) ([]byte, error) {
	if err := validateStoredMessage(message); err != nil {
		return nil, err
	}
	persisted := persistedMessage{
		Role: string(message.Role), Content: encodeContent(message.Content), Name: message.Name,
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

func decodePersistedMessage(raw []byte) (model.Message, error) {
	if !utf8.Valid(raw) {
		return model.Message{}, fmt.Errorf("payload is not valid UTF-8: %w", ErrCorruptHistory)
	}
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
	messageContent, err := decodeContent(persisted.Content)
	if err != nil {
		return model.Message{}, err
	}
	message := model.Message{
		Role: model.Role(persisted.Role), Content: messageContent, Name: persisted.Name,
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
	message.Content = content.Clone(message.Content)
	message.ToolCalls = cloneCalls(message.ToolCalls)
	return message
}

func encodeContent(value content.Content) []persistedPart {
	result := make([]persistedPart, len(value))
	for i, part := range value {
		result[i].Kind = contentKindName(part.Kind)
		if part.Kind == content.KindText {
			result[i].Text = part.Text
			continue
		}
		source := persistedSource{Kind: sourceKindName(part.Media.Source.Kind)}
		switch part.Media.Source.Kind {
		case content.SourceInline:
			source.Data = append([]byte(nil), part.Media.Source.Data...)
		case content.SourceURI:
			source.URI = persistedOpaqueString(part.Media.Source.URI)
		case content.SourceAsset:
			source.Asset = &persistedAsset{ID: persistedOpaqueString(part.Media.Source.Asset.ID)}
		}
		result[i].Media = &persistedMedia{MIMEType: persistedOpaqueString(part.Media.MIMEType), Name: part.Media.Name, Source: source}
	}
	return result
}

func decodeContent(value []persistedPart) (content.Content, error) {
	result := make(content.Content, len(value))
	for i, part := range value {
		kind := parseContentKind(part.Kind)
		if kind == 0 {
			return nil, fmt.Errorf("content part %d has unknown kind: %w", i, ErrCorruptHistory)
		}
		if kind == content.KindText {
			if part.Media != nil {
				return nil, fmt.Errorf("text content part %d has media: %w", i, ErrCorruptHistory)
			}
			result[i] = content.Text(part.Text)
			continue
		}
		if part.Text != "" || part.Media == nil {
			return nil, fmt.Errorf("media content part %d has invalid representation: %w", i, ErrCorruptHistory)
		}
		sourceKind := parseSourceKind(part.Media.Source.Kind)
		source := content.Source{Kind: sourceKind, Data: append([]byte(nil), part.Media.Source.Data...), URI: string(part.Media.Source.URI)}
		if part.Media.Source.Asset != nil {
			source.Asset.ID = string(part.Media.Source.Asset.ID)
		}
		result[i] = content.Part{Kind: kind, Media: content.Media{MIMEType: string(part.Media.MIMEType), Name: part.Media.Name, Source: source}}
	}
	if err := content.Validate(result); err != nil {
		return nil, fmt.Errorf("persisted content: %w: %v", ErrCorruptHistory, err)
	}
	return result, nil
}

func contentKindName(kind content.Kind) string {
	switch kind {
	case content.KindText:
		return "text"
	case content.KindImage:
		return "image"
	case content.KindAudio:
		return "audio"
	case content.KindVideo:
		return "video"
	case content.KindFile:
		return "file"
	default:
		return ""
	}
}

func parseContentKind(value string) content.Kind {
	switch value {
	case "text":
		return content.KindText
	case "image":
		return content.KindImage
	case "audio":
		return content.KindAudio
	case "video":
		return content.KindVideo
	case "file":
		return content.KindFile
	default:
		return 0
	}
}

func sourceKindName(kind content.SourceKind) string {
	switch kind {
	case content.SourceInline:
		return "inline"
	case content.SourceURI:
		return "uri"
	case content.SourceAsset:
		return "asset"
	default:
		return ""
	}
}

func parseSourceKind(value string) content.SourceKind {
	switch value {
	case "inline":
		return content.SourceInline
	case "uri":
		return content.SourceURI
	case "asset":
		return content.SourceAsset
	default:
		return 0
	}
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
