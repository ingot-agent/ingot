package usagedefault

import (
	"encoding/json"
	"fmt"
	"math"
	"unicode/utf8"

	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/tool"
)

func validateRequest(request model.Request, requireTarget bool) error {
	if !utf8.ValidString(request.Provider) || !utf8.ValidString(request.Model) {
		return fmt.Errorf("provider or model contains invalid UTF-8: %w", ErrInvalidRequest)
	}
	if requireTarget && (request.Provider == "" || request.Model == "") {
		return fmt.Errorf("resolved provider and model are required: %w", ErrInvalidRequest)
	}
	if request.Temperature != nil && (math.IsNaN(*request.Temperature) || math.IsInf(*request.Temperature, 0)) {
		return fmt.Errorf("temperature must be finite: %w", ErrInvalidRequest)
	}
	if request.MaxTokens != nil && *request.MaxTokens <= 0 {
		return fmt.Errorf("max_tokens must be positive: %w", ErrInvalidRequest)
	}
	for i, stop := range request.Stop {
		if !utf8.ValidString(stop) {
			return fmt.Errorf("stop[%d] contains invalid UTF-8: %w", i, ErrInvalidRequest)
		}
	}
	for i, message := range request.Messages {
		if !validRole(message.Role) {
			return fmt.Errorf("messages[%d] has unsupported role %q: %w", i, message.Role, ErrInvalidRequest)
		}
		if !utf8.ValidString(message.Name) || !utf8.ValidString(message.ToolCallID) {
			return fmt.Errorf("messages[%d] contains invalid UTF-8: %w", i, ErrInvalidRequest)
		}
		if err := content.Validate(message.Content); err != nil {
			return fmt.Errorf("messages[%d] content is invalid: %w: %w", i, ErrInvalidRequest, err)
		}
		for j, call := range message.ToolCalls {
			if call.ID == "" || call.Name == "" || !utf8.ValidString(call.ID) || !utf8.ValidString(call.Name) || !json.Valid(call.Arguments) {
				return fmt.Errorf("messages[%d].tool_calls[%d] is invalid: %w", i, j, ErrInvalidRequest)
			}
		}
	}
	for i, definition := range request.Tools {
		if definition.Name == "" || !utf8.ValidString(definition.Name) || !utf8.ValidString(definition.Description) || !json.Valid(definition.InputSchema) {
			return fmt.Errorf("tools[%d] is invalid: %w", i, ErrInvalidRequest)
		}
	}
	return nil
}

func validRole(role model.Role) bool {
	switch role {
	case model.RoleSystem, model.RoleUser, model.RoleAssistant, model.RoleTool:
		return true
	default:
		return false
	}
}

func cloneRequest(request model.Request) model.Request {
	if request.Messages != nil {
		messages := make([]model.Message, len(request.Messages))
		for i, message := range request.Messages {
			messages[i] = message
			messages[i].Content = content.Clone(message.Content)
			if message.ToolCalls != nil {
				messages[i].ToolCalls = make([]tool.Call, len(message.ToolCalls))
				for j, call := range message.ToolCalls {
					messages[i].ToolCalls[j] = call
					messages[i].ToolCalls[j].Arguments = cloneRawMessage(call.Arguments)
				}
			}
		}
		request.Messages = messages
	}
	if request.Tools != nil {
		definitions := make([]tool.Definition, len(request.Tools))
		for i, definition := range request.Tools {
			definitions[i] = definition
			definitions[i].InputSchema = cloneRawMessage(definition.InputSchema)
		}
		request.Tools = definitions
	}
	if request.Temperature != nil {
		value := *request.Temperature
		request.Temperature = &value
	}
	if request.MaxTokens != nil {
		value := *request.MaxTokens
		request.MaxTokens = &value
	}
	if request.Stop != nil {
		request.Stop = append([]string(nil), request.Stop...)
	}
	return request
}

func cloneRawMessage(value json.RawMessage) json.RawMessage {
	if value == nil {
		return nil
	}
	return append(json.RawMessage(nil), value...)
}
