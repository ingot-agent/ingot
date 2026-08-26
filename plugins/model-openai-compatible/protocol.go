package openaicompat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"unicode/utf8"

	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/tool"
)

type chatRequest struct {
	Model         string        `json:"model"`
	Messages      []chatMessage `json:"messages"`
	Tools         []chatTool    `json:"tools,omitempty"`
	Temperature   *float64      `json:"temperature,omitempty"`
	MaxTokens     *int          `json:"max_tokens,omitempty"`
	Stop          *[]string     `json:"stop,omitempty"`
	Stream        bool          `json:"stream"`
	StreamOptions *streamOption `json:"stream_options,omitempty"`
}

type streamOption struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	Name       string         `json:"name,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
}

type chatTool struct {
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

type chatFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	// OpenAI-compatible APIs represent function arguments as a JSON-encoded
	// string on the wire. The SDK model contract keeps them as json.RawMessage,
	// so encode/decodeMessage perform the boundary conversion explicitly.
	Arguments string `json:"arguments,omitempty"`
}

type chatToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

type chatResponse struct {
	Model   string         `json:"model"`
	Choices []chatChoice   `json:"choices"`
	Usage   *chatUsage     `json:"usage"`
	Error   map[string]any `json:"error"`
}

type chatChoice struct {
	Index        int          `json:"index"`
	Message      *chatMessage `json:"message"`
	FinishReason *string      `json:"finish_reason"`
}

type chatUsage struct {
	InputTokens  *int `json:"prompt_tokens"`
	OutputTokens *int `json:"completion_tokens"`
	TotalTokens  *int `json:"total_tokens"`
}

func encodeChatRequest(request model.Request, stream bool) ([]byte, error) {
	messages := make([]chatMessage, len(request.Messages))
	for i, message := range request.Messages {
		mapped, err := encodeMessage(message)
		if err != nil {
			return nil, fmt.Errorf("messages[%d]: %w", i, err)
		}
		messages[i] = mapped
	}
	tools := make([]chatTool, len(request.Tools))
	for i, definition := range request.Tools {
		if definition.Name == "" || !utf8.ValidString(definition.Name) || !utf8.ValidString(definition.Description) || !validJSONObject(definition.InputSchema) {
			return nil, fmt.Errorf("tools[%d] is invalid: %w", i, ErrInvalidRequest)
		}
		tools[i] = chatTool{Type: "function", Function: chatFunction{
			Name: definition.Name, Description: definition.Description,
			Parameters: append(json.RawMessage(nil), definition.InputSchema...),
		}}
	}
	payload := chatRequest{
		Model: request.Model, Messages: messages, Tools: tools,
		Temperature: request.Temperature, MaxTokens: request.MaxTokens, Stream: stream,
	}
	if request.Stop != nil {
		stops := append([]string(nil), request.Stop...)
		payload.Stop = &stops
	}
	if stream {
		payload.StreamOptions = &streamOption{IncludeUsage: true}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode model request: %w", err)
	}
	return raw, nil
}

func validateSDKRequest(request model.Request) error {
	if !utf8.ValidString(request.Model) || !utf8.ValidString(request.Provider) {
		return fmt.Errorf("provider or model is invalid UTF-8: %w", ErrInvalidRequest)
	}
	if request.Temperature != nil && (math.IsNaN(*request.Temperature) || math.IsInf(*request.Temperature, 0) || *request.Temperature < 0 || *request.Temperature > 2) {
		return fmt.Errorf("temperature is outside [0,2]: %w", ErrInvalidRequest)
	}
	if request.MaxTokens != nil && *request.MaxTokens < 1 {
		return fmt.Errorf("max_tokens must be positive: %w", ErrInvalidRequest)
	}
	for i, stop := range request.Stop {
		if !utf8.ValidString(stop) {
			return fmt.Errorf("stop[%d] is invalid UTF-8: %w", i, ErrInvalidRequest)
		}
	}
	for i, message := range request.Messages {
		if _, err := encodeMessage(message); err != nil {
			return fmt.Errorf("messages[%d]: %w", i, err)
		}
	}
	return nil
}

func encodeMessage(message model.Message) (chatMessage, error) {
	if !validRole(message.Role) || !utf8.ValidString(message.Content) || !utf8.ValidString(message.Name) || !utf8.ValidString(message.ToolCallID) {
		return chatMessage{}, ErrInvalidRequest
	}
	result := chatMessage{Role: string(message.Role), Content: message.Content, Name: message.Name, ToolCallID: message.ToolCallID}
	result.ToolCalls = make([]chatToolCall, len(message.ToolCalls))
	for i, call := range message.ToolCalls {
		if call.ID == "" || call.Name == "" || !utf8.ValidString(call.ID) || !utf8.ValidString(call.Name) || !json.Valid(call.Arguments) {
			return chatMessage{}, fmt.Errorf("tool_calls[%d]: %w", i, ErrInvalidRequest)
		}
		result.ToolCalls[i] = chatToolCall{ID: call.ID, Type: "function", Function: chatFunction{Name: call.Name, Arguments: string(call.Arguments)}}
	}
	if message.Role == model.RoleTool && message.ToolCallID == "" {
		return chatMessage{}, fmt.Errorf("tool message requires tool_call_id: %w", ErrInvalidRequest)
	}
	return result, nil
}

func validRole(role model.Role) bool {
	return role == model.RoleSystem || role == model.RoleUser || role == model.RoleAssistant || role == model.RoleTool
}

func validJSONObject(raw json.RawMessage) bool {
	if !json.Valid(raw) {
		return false
	}
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) >= 2 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}'
}

func decodeComplete(raw []byte, providerName string) (model.Response, error) {
	if !utf8.Valid(raw) {
		return model.Response{}, protocolError("complete response is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var response chatResponse
	if err := decoder.Decode(&response); err != nil {
		return model.Response{}, protocolError("decode complete response: %v", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return model.Response{}, protocolError("multiple JSON values in complete response")
	}
	if response.Model == "" || len(response.Choices) != 1 {
		return model.Response{}, protocolError("complete response requires non-empty model and exactly one choice")
	}
	choice := response.Choices[0]
	if choice.Index != 0 || choice.Message == nil || choice.FinishReason == nil || *choice.FinishReason == "" {
		return model.Response{}, protocolError("choice requires index 0, message, and finish_reason")
	}
	message, err := decodeMessage(*choice.Message)
	if err != nil {
		return model.Response{}, err
	}
	if message.Role != model.RoleAssistant {
		return model.Response{}, protocolError("complete response message role is %q", message.Role)
	}
	usage, err := decodeUsage(response.Usage)
	if err != nil {
		return model.Response{}, err
	}
	return model.Response{Message: message, FinishReason: *choice.FinishReason, Usage: usage, Provider: providerName, Model: response.Model}, nil
}

func decodeMessage(message chatMessage) (model.Message, error) {
	role := model.Role(message.Role)
	if !validRole(role) || !utf8.ValidString(message.Content) || !utf8.ValidString(message.Name) || !utf8.ValidString(message.ToolCallID) {
		return model.Message{}, protocolError("invalid message fields")
	}
	result := model.Message{Role: role, Content: message.Content, Name: message.Name, ToolCallID: message.ToolCallID}
	result.ToolCalls = make([]tool.Call, len(message.ToolCalls))
	for i, call := range message.ToolCalls {
		if call.ID == "" || call.Function.Name == "" || call.Type != "function" || !json.Valid([]byte(call.Function.Arguments)) {
			return model.Message{}, protocolError("invalid tool call at index %d", i)
		}
		result.ToolCalls[i] = tool.Call{ID: call.ID, Name: call.Function.Name, Arguments: json.RawMessage(call.Function.Arguments)}
	}
	return result, nil
}

func decodeUsage(value *chatUsage) (model.Usage, error) {
	if value == nil {
		return model.Usage{}, nil
	}
	if value.InputTokens == nil || value.OutputTokens == nil || value.TotalTokens == nil {
		return model.Usage{}, protocolError("usage requires prompt_tokens, completion_tokens, and total_tokens")
	}
	if *value.InputTokens < 0 || *value.OutputTokens < 0 || *value.TotalTokens < 0 || *value.TotalTokens != *value.InputTokens+*value.OutputTokens {
		return model.Usage{}, protocolError("invalid usage counts")
	}
	return model.Usage{InputTokens: *value.InputTokens, OutputTokens: *value.OutputTokens, TotalTokens: *value.TotalTokens}, nil
}
