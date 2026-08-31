package script

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"unicode/utf8"

	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/tool"
)

const hookProtocolVersion = 2

type beforeEnvelope struct {
	ProtocolVersion int    `json:"protocol_version"`
	Hook            string `json:"hook"`
	Target          string `json:"target"`
	Phase           string `json:"phase"`
	Request         any    `json:"request"`
}

type afterEnvelope struct {
	ProtocolVersion int     `json:"protocol_version"`
	Hook            string  `json:"hook"`
	Target          string  `json:"target"`
	Phase           string  `json:"phase"`
	Request         any     `json:"request"`
	Outcome         outcome `json:"outcome"`
}

type outcome struct {
	Response any              `json:"response"`
	Error    *errorDescriptor `json:"error"`
}

type hookResponse struct {
	ProtocolVersion int     `json:"protocol_version"`
	Action          string  `json:"action"`
	Message         *string `json:"message,omitempty"`
}

type errorDescriptor struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

type callProjection struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type definitionProjection struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type messageProjection struct {
	Role       string           `json:"role"`
	Content    []partProjection `json:"content"`
	Name       string           `json:"name"`
	ToolCallID string           `json:"tool_call_id"`
	ToolCalls  []callProjection `json:"tool_calls"`
}

type modelRequestProjection struct {
	Provider    string                 `json:"provider"`
	Model       string                 `json:"model"`
	Messages    []messageProjection    `json:"messages"`
	Tools       []definitionProjection `json:"tools"`
	Temperature *float64               `json:"temperature"`
	MaxTokens   *int                   `json:"max_tokens"`
	Stop        []string               `json:"stop"`
}

type usageProjection struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type modelResponseProjection struct {
	Message      messageProjection `json:"message"`
	FinishReason string            `json:"finish_reason"`
	Usage        usageProjection   `json:"usage"`
	Provider     string            `json:"provider"`
	Model        string            `json:"model"`
}

type toolResponseProjection struct {
	Content []partProjection `json:"content"`
}
type agentRequestProjection struct {
	SessionID   string           `json:"session_id"`
	Input       string           `json:"input"`
	Attachments []partProjection `json:"attachments"`
}
type agentResponseProjection struct {
	Output []partProjection `json:"output"`
}

type partProjection struct {
	Kind  string           `json:"kind"`
	Text  string           `json:"text,omitempty"`
	Media *mediaProjection `json:"media,omitempty"`
}

type mediaProjection struct {
	MIMEType string           `json:"mime_type"`
	Name     string           `json:"name"`
	Source   sourceProjection `json:"source"`
}

type sourceProjection struct {
	Kind  string           `json:"kind"`
	Data  []byte           `json:"data,omitempty"`
	URI   string           `json:"uri,omitempty"`
	Asset *assetProjection `json:"asset,omitempty"`
}

type assetProjection struct {
	ID string `json:"id"`
}

func runBefore(ctx context.Context, hook normalizedHook, request any) error {
	if ctx == nil {
		return context.Canceled
	}
	input, err := json.Marshal(beforeEnvelope{ProtocolVersion: hookProtocolVersion, Hook: hook.name, Target: hook.target, Phase: "before", Request: request})
	if err != nil {
		return fmt.Errorf("encode before hook %q: %w: %w", hook.name, ErrHookFailed, err)
	}
	response, err := executeHook(ctx, hook, input)
	if err != nil {
		return fmt.Errorf("before hook %q: %w: %w", hook.name, ErrHookFailed, err)
	}
	parsed, err := decodeHookResponse(response)
	if err != nil {
		return fmt.Errorf("before hook %q: %w: %w", hook.name, ErrHookFailed, err)
	}
	switch parsed.Action {
	case "continue":
		if parsed.Message != nil {
			return fmt.Errorf("continue response contains message: %w", ErrHookFailed)
		}
		return nil
	case "reject":
		if parsed.Message == nil || *parsed.Message == "" || !utf8.ValidString(*parsed.Message) {
			return fmt.Errorf("reject response requires message: %w", ErrHookFailed)
		}
		return fmt.Errorf("hook %q: %s: %w", hook.name, *parsed.Message, ErrHookRejected)
	default:
		return fmt.Errorf("hook %q returned action %q: %w", hook.name, parsed.Action, ErrHookFailed)
	}
}

func finishAfter[T any](ctx context.Context, hook normalizedHook, request, responseProjection any, response T, downstreamErr, projectionErr error) (T, error) {
	if ctx == nil {
		return response, errors.Join(downstreamErr, context.Canceled)
	}
	if err := ctx.Err(); err != nil {
		return response, errors.Join(downstreamErr, err)
	}
	if errors.Is(downstreamErr, context.Canceled) || errors.Is(downstreamErr, context.DeadlineExceeded) {
		return response, downstreamErr
	}
	if downstreamErr == nil && projectionErr != nil {
		return response, afterHookFailure(hook, fmt.Errorf("project response: %w", projectionErr))
	}
	result := outcome{Response: responseProjection}
	if downstreamErr != nil {
		result.Response = nil
		var err error
		result.Error, err = describeError(downstreamErr)
		if err != nil {
			return response, errors.Join(downstreamErr, afterHookFailure(hook, err))
		}
	}
	input, err := json.Marshal(afterEnvelope{ProtocolVersion: hookProtocolVersion, Hook: hook.name, Target: hook.target, Phase: "after", Request: request, Outcome: result})
	if err == nil {
		var raw []byte
		raw, err = executeHook(ctx, hook, input)
		if err == nil {
			var parsed hookResponse
			parsed, err = decodeHookResponse(raw)
			if err == nil && (parsed.Action != "continue" || parsed.Message != nil) {
				err = fmt.Errorf("after response must be continue without message")
			}
		}
	}
	if err != nil {
		return response, errors.Join(downstreamErr, afterHookFailure(hook, err))
	}
	return response, downstreamErr
}

func afterHookFailure(hook normalizedHook, cause error) error {
	return errors.Join(
		fmt.Errorf("hook %q: %w: %w", hook.name, ErrAfterHookFailed, ErrCompletionUnknown),
		fmt.Errorf("%w: %w", ErrHookFailed, cause),
	)
}

func decodeHookResponse(raw []byte) (hookResponse, error) {
	if !utf8.Valid(raw) {
		return hookResponse{}, errors.New("hook response is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var response hookResponse
	first, err := decoder.Token()
	if err != nil {
		return hookResponse{}, err
	}
	if delimiter, ok := first.(json.Delim); !ok || delimiter != '{' {
		return hookResponse{}, errors.New("hook response must be a JSON object")
	}
	seen := make(map[string]struct{}, 3)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return hookResponse{}, err
		}
		key, ok := token.(string)
		if !ok {
			return hookResponse{}, errors.New("hook response contains a non-string key")
		}
		if _, exists := seen[key]; exists {
			return hookResponse{}, fmt.Errorf("hook response contains duplicate field %q", key)
		}
		seen[key] = struct{}{}
		switch key {
		case "protocol_version":
			if err := decoder.Decode(&response.ProtocolVersion); err != nil {
				return hookResponse{}, fmt.Errorf("protocol_version: %w", err)
			}
		case "action":
			if err := decoder.Decode(&response.Action); err != nil {
				return hookResponse{}, fmt.Errorf("action: %w", err)
			}
		case "message":
			var encoded json.RawMessage
			if err := decoder.Decode(&encoded); err != nil {
				return hookResponse{}, fmt.Errorf("message: %w", err)
			}
			var message string
			if bytes.Equal(bytes.TrimSpace(encoded), []byte("null")) {
				return hookResponse{}, errors.New("message must be a string, not null")
			}
			if err := json.Unmarshal(encoded, &message); err != nil {
				return hookResponse{}, fmt.Errorf("message: %w", err)
			}
			response.Message = &message
		default:
			return hookResponse{}, fmt.Errorf("hook response contains unknown field %q", key)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return hookResponse{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return hookResponse{}, errors.New("hook response contains multiple JSON values")
	}
	if _, exists := seen["protocol_version"]; !exists {
		return hookResponse{}, errors.New("hook response is missing protocol_version")
	}
	if _, exists := seen["action"]; !exists {
		return hookResponse{}, errors.New("hook response is missing action")
	}
	if response.ProtocolVersion != hookProtocolVersion {
		return hookResponse{}, fmt.Errorf("unsupported protocol_version %d", response.ProtocolVersion)
	}
	return response, nil
}

func projectToolRequest(call tool.Call) (callProjection, error) {
	if err := validateUTF8("tool call id", call.ID); err != nil {
		return callProjection{}, err
	}
	if err := validateUTF8("tool name", call.Name); err != nil {
		return callProjection{}, err
	}
	if !utf8.Valid(call.Arguments) || !json.Valid(call.Arguments) {
		return callProjection{}, errors.New("tool arguments are invalid JSON")
	}
	return callProjection{ID: call.ID, Name: call.Name, Arguments: append(json.RawMessage(nil), call.Arguments...)}, nil
}

func projectToolResponse(result tool.Result) (toolResponseProjection, error) {
	projected, err := projectContent(result.Content)
	if err != nil {
		return toolResponseProjection{}, err
	}
	return toolResponseProjection{Content: projected}, nil
}

func projectModelRequest(request model.Request) (modelRequestProjection, error) {
	if err := validateUTF8("provider", request.Provider); err != nil {
		return modelRequestProjection{}, err
	}
	if err := validateUTF8("model", request.Model); err != nil {
		return modelRequestProjection{}, err
	}
	if request.Temperature != nil && (math.IsNaN(*request.Temperature) || math.IsInf(*request.Temperature, 0)) {
		return modelRequestProjection{}, errors.New("temperature must be finite")
	}
	messages := make([]messageProjection, len(request.Messages))
	for i, message := range request.Messages {
		projected, err := projectMessage(message)
		if err != nil {
			return modelRequestProjection{}, fmt.Errorf("messages[%d]: %w", i, err)
		}
		messages[i] = projected
	}
	tools := make([]definitionProjection, len(request.Tools))
	for i, definition := range request.Tools {
		if err := validateUTF8("tool name", definition.Name); err != nil {
			return modelRequestProjection{}, fmt.Errorf("tools[%d]: %w", i, err)
		}
		if err := validateUTF8("tool description", definition.Description); err != nil {
			return modelRequestProjection{}, fmt.Errorf("tools[%d]: %w", i, err)
		}
		if !utf8.Valid(definition.InputSchema) || !json.Valid(definition.InputSchema) {
			return modelRequestProjection{}, fmt.Errorf("tools[%d] schema is invalid JSON", i)
		}
		tools[i] = definitionProjection{Name: definition.Name, Description: definition.Description, InputSchema: append(json.RawMessage(nil), definition.InputSchema...)}
	}
	for i, stop := range request.Stop {
		if err := validateUTF8("stop", stop); err != nil {
			return modelRequestProjection{}, fmt.Errorf("stop[%d]: %w", i, err)
		}
	}
	return modelRequestProjection{
		Provider: request.Provider, Model: request.Model, Messages: messages, Tools: tools,
		Temperature: copyFloat(request.Temperature), MaxTokens: copyInt(request.MaxTokens), Stop: append([]string{}, request.Stop...),
	}, nil
}

func projectModelResponse(response model.Response) (modelResponseProjection, error) {
	message, err := projectMessage(response.Message)
	if err != nil {
		return modelResponseProjection{}, err
	}
	for _, field := range []struct{ name, value string }{
		{name: "finish reason", value: response.FinishReason},
		{name: "provider", value: response.Provider},
		{name: "model", value: response.Model},
	} {
		if err := validateUTF8(field.name, field.value); err != nil {
			return modelResponseProjection{}, err
		}
	}
	return modelResponseProjection{
		Message: message, FinishReason: response.FinishReason,
		Usage:    usageProjection{InputTokens: response.Usage.InputTokens, OutputTokens: response.Usage.OutputTokens, TotalTokens: response.Usage.TotalTokens},
		Provider: response.Provider, Model: response.Model,
	}, nil
}

func projectMessage(message model.Message) (messageProjection, error) {
	for _, field := range []struct{ name, value string }{
		{name: "role", value: string(message.Role)},
		{name: "name", value: message.Name},
		{name: "tool call id", value: message.ToolCallID},
	} {
		if err := validateUTF8(field.name, field.value); err != nil {
			return messageProjection{}, err
		}
	}
	projectedContent, err := projectContent(message.Content)
	if err != nil {
		return messageProjection{}, err
	}
	calls := make([]callProjection, len(message.ToolCalls))
	for i, call := range message.ToolCalls {
		projected, err := projectToolRequest(call)
		if err != nil {
			return messageProjection{}, fmt.Errorf("tool_calls[%d]: %w", i, err)
		}
		calls[i] = projected
	}
	return messageProjection{Role: string(message.Role), Content: projectedContent, Name: message.Name, ToolCallID: message.ToolCallID, ToolCalls: calls}, nil
}

func projectAgentRequest(turn agent.Turn) (agentRequestProjection, error) {
	if err := validateUTF8("session id", string(turn.SessionID)); err != nil {
		return agentRequestProjection{}, err
	}
	if err := validateUTF8("agent input", turn.Input); err != nil {
		return agentRequestProjection{}, err
	}
	if err := content.ValidateAttachments(turn.Attachments); err != nil {
		return agentRequestProjection{}, err
	}
	attachments := make([]partProjection, len(turn.Attachments))
	for i, attachment := range turn.Attachments {
		projected, err := projectContent(content.Content{content.AttachmentPart(attachment)})
		if err != nil {
			return agentRequestProjection{}, fmt.Errorf("attachment %d: %w", i, err)
		}
		attachments[i] = projected[0]
	}
	return agentRequestProjection{SessionID: string(turn.SessionID), Input: turn.Input, Attachments: attachments}, nil
}

func projectAgentResponse(result agent.Result) (agentResponseProjection, error) {
	projected, err := projectContent(result.Output)
	if err != nil {
		return agentResponseProjection{}, err
	}
	return agentResponseProjection{Output: projected}, nil
}

func projectContent(value content.Content) ([]partProjection, error) {
	if err := content.Validate(value); err != nil {
		return nil, err
	}
	result := make([]partProjection, len(value))
	for i, part := range value {
		result[i].Kind = contentKindName(part.Kind)
		if part.Kind == content.KindText {
			result[i].Text = part.Text
			continue
		}
		if !utf8.ValidString(part.Media.MIMEType) {
			return nil, fmt.Errorf("content part %d MIME type is not valid UTF-8", i)
		}
		source := sourceProjection{Kind: sourceKindName(part.Media.Source.Kind)}
		switch part.Media.Source.Kind {
		case content.SourceInline:
			source.Data = append([]byte(nil), part.Media.Source.Data...)
		case content.SourceURI:
			if !utf8.ValidString(part.Media.Source.URI) {
				return nil, fmt.Errorf("content part %d URI is not valid UTF-8", i)
			}
			source.URI = part.Media.Source.URI
		case content.SourceAsset:
			if !utf8.ValidString(part.Media.Source.Asset.ID) {
				return nil, fmt.Errorf("content part %d asset ID is not valid UTF-8", i)
			}
			source.Asset = &assetProjection{ID: part.Media.Source.Asset.ID}
		}
		result[i].Media = &mediaProjection{MIMEType: part.Media.MIMEType, Name: part.Media.Name, Source: source}
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

func describeError(err error) (*errorDescriptor, error) {
	kind := "other"
	switch {
	case errors.Is(err, context.Canceled):
		kind = "context_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		kind = "deadline_exceeded"
	case errors.Is(err, tool.ErrNotFound):
		kind = "tool_not_found"
	case errors.Is(err, tool.ErrInvalidArguments):
		kind = "invalid_arguments"
	case errors.Is(err, model.ErrProviderNotFound):
		kind = "provider_not_found"
	case errors.Is(err, model.ErrModelNotFound):
		kind = "model_not_found"
	case errors.Is(err, model.ErrStreamingUnsupported):
		kind = "streaming_unsupported"
	case errors.Is(err, session.ErrNotFound):
		kind = "session_not_found"
	}
	message := err.Error()
	if err := validateUTF8("downstream error", message); err != nil {
		return nil, err
	}
	return &errorDescriptor{Kind: kind, Message: message}, nil
}

func validateUTF8(name, value string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s is not valid UTF-8", name)
	}
	return nil
}

func copyFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
func copyInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
