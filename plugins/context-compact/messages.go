package contextcompact

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"unicode/utf8"

	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/tool"
)

type turnRange struct {
	start int
	end   int
}

type messageLayout struct {
	system       []model.Message
	conversation []model.Message
	turns        []turnRange
	anchorEnd    int
	eligibleEnd  int
}

func inspectRequest(request model.Request, anchorTurns, recentTurns int) (messageLayout, error) {
	if !utf8.ValidString(request.Provider) || !utf8.ValidString(request.Model) {
		return messageLayout{}, fmt.Errorf("provider or model is invalid UTF-8: %w", ErrInvalidRequest)
	}
	for i, stop := range request.Stop {
		if !utf8.ValidString(stop) {
			return messageLayout{}, fmt.Errorf("stop[%d] is invalid UTF-8: %w", i, ErrInvalidRequest)
		}
	}
	if request.Temperature != nil && (math.IsNaN(*request.Temperature) || math.IsInf(*request.Temperature, 0)) {
		return messageLayout{}, fmt.Errorf("temperature is not finite: %w", ErrInvalidRequest)
	}
	for i, definition := range request.Tools {
		if !utf8.ValidString(definition.Name) || !utf8.ValidString(definition.Description) || !validRawJSON(definition.InputSchema) {
			return messageLayout{}, fmt.Errorf("tools[%d] is invalid: %w", i, ErrInvalidRequest)
		}
	}

	owned := cloneMessages(request.Messages)
	systemEnd := 0
	for systemEnd < len(owned) && owned[systemEnd].Role == model.RoleSystem {
		if err := validateMessage(owned[systemEnd]); err != nil {
			return messageLayout{}, fmt.Errorf("messages[%d]: %w", systemEnd, err)
		}
		systemEnd++
	}
	conversation := owned[systemEnd:]
	turns, err := groupTurns(conversation)
	if err != nil {
		return messageLayout{}, err
	}
	anchorCount := min(anchorTurns, len(turns))
	anchorEnd := 0
	if anchorCount > 0 {
		anchorEnd = turns[anchorCount-1].end
	}
	recentStart := len(turns) - recentTurns
	if recentStart < anchorCount {
		recentStart = anchorCount
	}
	eligibleEnd := len(conversation)
	if recentStart < len(turns) {
		eligibleEnd = turns[recentStart].start
	}
	return messageLayout{
		system: cloneMessages(owned[:systemEnd]), conversation: cloneMessages(conversation), turns: turns,
		anchorEnd: anchorEnd, eligibleEnd: eligibleEnd,
	}, nil
}

func groupTurns(messages []model.Message) ([]turnRange, error) {
	if len(messages) == 0 {
		return []turnRange{}, nil
	}
	if messages[0].Role != model.RoleUser {
		return nil, fmt.Errorf("conversation must start with user: %w", ErrInvalidHistory)
	}
	turns := make([]turnRange, 0)
	start := -1
	var pending []tool.Call
	matched := 0
	for i, message := range messages {
		if err := validateMessage(message); err != nil {
			return nil, fmt.Errorf("conversation message %d: %w", i, err)
		}
		switch message.Role {
		case model.RoleSystem:
			return nil, fmt.Errorf("system message after conversation start at %d: %w", i, ErrInvalidHistory)
		case model.RoleUser:
			if len(pending) != matched {
				return nil, fmt.Errorf("user message follows incomplete tool round at %d: %w", i, ErrInvalidHistory)
			}
			if start >= 0 {
				turns = append(turns, turnRange{start: start, end: i})
			}
			start = i
			pending = nil
			matched = 0
		case model.RoleAssistant:
			if start < 0 || len(pending) != matched {
				return nil, fmt.Errorf("assistant message has no complete user turn at %d: %w", i, ErrInvalidHistory)
			}
			pending = cloneCalls(message.ToolCalls)
			matched = 0
		case model.RoleTool:
			if start < 0 || matched >= len(pending) || message.ToolCallID != pending[matched].ID {
				return nil, fmt.Errorf("tool result has no matching call at %d: %w", i, ErrInvalidHistory)
			}
			matched++
		}
	}
	if start >= 0 {
		turns = append(turns, turnRange{start: start, end: len(messages)})
	}
	return turns, nil
}

func validateMessage(message model.Message) error {
	if !utf8.ValidString(string(message.Role)) ||
		!utf8.ValidString(message.Name) || !utf8.ValidString(message.ToolCallID) {
		return fmt.Errorf("invalid UTF-8: %w", ErrInvalidHistory)
	}
	if err := content.Validate(message.Content); err != nil {
		return fmt.Errorf("invalid content: %w: %w", ErrInvalidHistory, err)
	}
	for i, part := range message.Content {
		if part.Kind == content.KindText {
			continue
		}
		if !utf8.ValidString(part.Media.MIMEType) {
			return fmt.Errorf("content part %d MIME type is invalid UTF-8: %w", i, ErrInvalidHistory)
		}
		switch part.Media.Source.Kind {
		case content.SourceURI:
			if !utf8.ValidString(part.Media.Source.URI) {
				return fmt.Errorf("content part %d URI is invalid UTF-8: %w", i, ErrInvalidHistory)
			}
		case content.SourceAsset:
			if !utf8.ValidString(part.Media.Source.Asset.ID) {
				return fmt.Errorf("content part %d asset ID is invalid UTF-8: %w", i, ErrInvalidHistory)
			}
		}
	}
	switch message.Role {
	case model.RoleSystem, model.RoleUser:
		if message.ToolCallID != "" || len(message.ToolCalls) != 0 {
			return fmt.Errorf("system/user message contains tool linkage: %w", ErrInvalidHistory)
		}
	case model.RoleAssistant:
		if message.ToolCallID != "" {
			return fmt.Errorf("assistant message contains tool_call_id: %w", ErrInvalidHistory)
		}
		seen := make(map[string]struct{}, len(message.ToolCalls))
		for i, call := range message.ToolCalls {
			if call.ID == "" || call.Name == "" || !utf8.ValidString(call.ID) || !utf8.ValidString(call.Name) || !validRawJSON(call.Arguments) {
				return fmt.Errorf("assistant tool_calls[%d] is invalid: %w", i, ErrInvalidHistory)
			}
			if _, duplicate := seen[call.ID]; duplicate {
				return fmt.Errorf("duplicate tool call id %q: %w", call.ID, ErrInvalidHistory)
			}
			seen[call.ID] = struct{}{}
		}
	case model.RoleTool:
		if message.ToolCallID == "" || len(message.ToolCalls) != 0 {
			return fmt.Errorf("tool message linkage is invalid: %w", ErrInvalidHistory)
		}
	default:
		return fmt.Errorf("unsupported role %q: %w", message.Role, ErrInvalidHistory)
	}
	return nil
}

func validRawJSON(raw json.RawMessage) bool {
	return len(raw) > 0 && utf8.Valid(raw) && json.Valid(raw)
}

type requestProjection struct {
	Provider    string              `json:"provider"`
	Model       string              `json:"model"`
	Messages    []messageProjection `json:"messages"`
	Tools       []toolProjection    `json:"tools"`
	Temperature *float64            `json:"temperature"`
	MaxTokens   *int                `json:"max_tokens"`
	Stop        []string            `json:"stop"`
}

type messageProjection struct {
	Role       string           `json:"role"`
	Content    content.Content  `json:"content"`
	Name       string           `json:"name"`
	ToolCallID string           `json:"tool_call_id"`
	ToolCalls  []callProjection `json:"tool_calls"`
}

type callProjection struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type toolProjection struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

func canonicalRequestBytes(request model.Request) ([]byte, error) {
	projection := requestProjection{
		Provider: request.Provider, Model: request.Model, Messages: projectMessages(request.Messages),
		Tools: make([]toolProjection, len(request.Tools)), Temperature: copyFloat(request.Temperature),
		MaxTokens: copyInt(request.MaxTokens), Stop: append([]string(nil), request.Stop...),
	}
	for i, definition := range request.Tools {
		projection.Tools[i] = toolProjection{Name: definition.Name, Description: definition.Description, InputSchema: cloneRaw(definition.InputSchema)}
	}
	return json.Marshal(projection)
}

func projectMessages(messages []model.Message) []messageProjection {
	result := make([]messageProjection, len(messages))
	for i, message := range messages {
		calls := make([]callProjection, len(message.ToolCalls))
		for j, call := range message.ToolCalls {
			calls[j] = callProjection{ID: call.ID, Name: call.Name, Arguments: cloneRaw(call.Arguments)}
		}
		result[i] = messageProjection{
			Role: string(message.Role), Content: content.Clone(message.Content), Name: message.Name,
			ToolCallID: message.ToolCallID, ToolCalls: calls,
		}
	}
	return result
}

func messageDigest(messages []model.Message) (string, error) {
	raw, err := json.Marshal(projectMessages(messages))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func cloneRequest(request model.Request) model.Request {
	request.Messages = cloneMessages(request.Messages)
	request.Tools = cloneDefinitions(request.Tools)
	request.Stop = append([]string(nil), request.Stop...)
	request.Temperature = copyFloat(request.Temperature)
	request.MaxTokens = copyInt(request.MaxTokens)
	return request
}

func cloneMessages(messages []model.Message) []model.Message {
	if messages == nil {
		return nil
	}
	result := make([]model.Message, len(messages))
	for i, message := range messages {
		message.Content = content.Clone(message.Content)
		message.ToolCalls = cloneCalls(message.ToolCalls)
		result[i] = message
	}
	return result
}

func cloneCalls(calls []tool.Call) []tool.Call {
	if calls == nil {
		return nil
	}
	result := make([]tool.Call, len(calls))
	for i, call := range calls {
		call.Arguments = cloneRaw(call.Arguments)
		result[i] = call
	}
	return result
}

func cloneDefinitions(definitions []tool.Definition) []tool.Definition {
	if definitions == nil {
		return nil
	}
	result := make([]tool.Definition, len(definitions))
	for i, definition := range definitions {
		definition.InputSchema = cloneRaw(definition.InputSchema)
		result[i] = definition
	}
	return result
}

func cloneRaw(raw json.RawMessage) json.RawMessage {
	if raw == nil {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

func copyFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func copyInt(value *int) *int {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}
