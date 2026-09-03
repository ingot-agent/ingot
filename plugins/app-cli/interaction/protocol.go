package interactioncomponent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ingot-agent/sdk/interaction"
)

type askOption struct {
	Value       string
	Label       string
	Description string
}

type askRequest struct {
	Prompt         string
	Options        []askOption
	AllowTextInput bool
}

type askFunc func(context.Context, askRequest) (string, error)

func collectResponse(ctx context.Context, request interaction.Request, ask askFunc) (interaction.Response, error) {
	if err := validateRequest(request); err != nil {
		return interaction.Response{}, err
	}
	response := interaction.Response{Values: make([]interaction.Answer, 0, len(request.Fields))}
	for _, field := range request.Fields {
		question := questionForField(request, field)
		text, err := ask(ctx, question)
		if err != nil {
			return interaction.Response{}, err
		}
		if strings.TrimSpace(text) == "" && field.Default != nil {
			response.Values = append(response.Values, interaction.Answer{Name: field.Name, Value: cloneValue(*field.Default)})
			continue
		}
		if strings.TrimSpace(text) == "" && !field.Required {
			continue
		}
		value, err := parseAnswer(field, text)
		if err != nil {
			return interaction.Response{}, fmt.Errorf("field %q: %w", field.Name, err)
		}
		response.Values = append(response.Values, interaction.Answer{Name: field.Name, Value: value})
	}
	return response, nil
}

func validateRequest(request interaction.Request) error {
	if request.Name == "" || !utf8.ValidString(request.Name) || !utf8.ValidString(request.Description) {
		return fmt.Errorf("interaction request name must be non-empty UTF-8 and description must be UTF-8")
	}
	if len(request.Fields) == 0 {
		return fmt.Errorf("interaction request %q has no fields", request.Name)
	}
	names := make(map[string]struct{}, len(request.Fields))
	for index, field := range request.Fields {
		if field.Name == "" || !utf8.ValidString(field.Name) || !utf8.ValidString(field.Label) || !utf8.ValidString(field.Description) {
			return fmt.Errorf("interaction request field %d has invalid text", index)
		}
		if _, exists := names[field.Name]; exists {
			return fmt.Errorf("interaction request field %d duplicates name %q", index, field.Name)
		}
		names[field.Name] = struct{}{}
		switch field.Kind {
		case interaction.FieldString, interaction.FieldInteger, interaction.FieldNumber, interaction.FieldBoolean:
		case interaction.FieldChoice, interaction.FieldMultiChoice:
			if len(field.Options) == 0 {
				return fmt.Errorf("interaction request field %q requires options", field.Name)
			}
		default:
			return fmt.Errorf("interaction request field %q has unsupported kind %d", field.Name, field.Kind)
		}
		values := make(map[string]struct{}, len(field.Options))
		for optionIndex, option := range field.Options {
			if option.Value == "" || !utf8.ValidString(option.Value) || !utf8.ValidString(option.Label) || !utf8.ValidString(option.Description) {
				return fmt.Errorf("interaction request field %q option %d has invalid text", field.Name, optionIndex)
			}
			if _, exists := values[option.Value]; exists {
				return fmt.Errorf("interaction request field %q duplicates option value %q", field.Name, option.Value)
			}
			values[option.Value] = struct{}{}
		}
	}
	return nil
}

func questionForField(request interaction.Request, field interaction.Field) askRequest {
	prompt := field.Label
	if prompt == "" {
		prompt = field.Name
	}
	if field.Description != "" {
		prompt += "\n" + field.Description
	} else if request.Description != "" {
		prompt += "\n" + request.Description
	}
	question := askRequest{Prompt: prompt, Options: make([]askOption, len(field.Options))}
	for index, option := range field.Options {
		label := option.Label
		if label == "" {
			label = option.Value
		}
		question.Options[index] = askOption{Value: option.Value, Label: label, Description: option.Description}
	}
	switch field.Kind {
	case interaction.FieldString:
		question.AllowTextInput = true
	case interaction.FieldBoolean:
		question.Options = []askOption{{Value: "true", Label: "Yes"}, {Value: "false", Label: "No"}}
	case interaction.FieldMultiChoice:
		question.AllowTextInput = true
		question.Prompt += "\nChoose one option or enter comma-separated values."
	}
	return question
}

func parseAnswer(field interaction.Field, text string) (interaction.Value, error) {
	switch field.Kind {
	case interaction.FieldString:
		return interaction.StringValue(text), nil
	case interaction.FieldInteger:
		value, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
		if err != nil {
			return interaction.Value{}, fmt.Errorf("expected an integer")
		}
		return interaction.IntegerValue(value), nil
	case interaction.FieldNumber:
		value, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
		if err != nil {
			return interaction.Value{}, fmt.Errorf("expected a number")
		}
		return interaction.NumberValue(value), nil
	case interaction.FieldBoolean:
		value, err := strconv.ParseBool(strings.TrimSpace(text))
		if err != nil {
			return interaction.Value{}, fmt.Errorf("expected a boolean")
		}
		return interaction.BooleanValue(value), nil
	case interaction.FieldChoice:
		if !allowedOption(field.Options, text) {
			return interaction.Value{}, fmt.Errorf("expected one of the configured choices")
		}
		return interaction.StringValue(text), nil
	case interaction.FieldMultiChoice:
		parts := strings.Split(text, ",")
		values := make([]string, 0, len(parts))
		for _, part := range parts {
			value := strings.TrimSpace(part)
			if value == "" || !allowedOption(field.Options, value) {
				return interaction.Value{}, fmt.Errorf("expected only configured choices")
			}
			values = append(values, value)
		}
		return interaction.StringsValue(values), nil
	default:
		return interaction.Value{}, fmt.Errorf("unsupported field kind")
	}
}

func allowedOption(options []interaction.Option, value string) bool {
	for _, option := range options {
		if value == option.Value {
			return true
		}
	}
	return false
}

func validateAskRequest(request askRequest) error {
	if !utf8.ValidString(request.Prompt) {
		return fmt.Errorf("ask prompt must be valid UTF-8")
	}
	values := make(map[string]struct{}, len(request.Options))
	for index, option := range request.Options {
		if option.Value == "" || !utf8.ValidString(option.Value) || option.Label == "" || !utf8.ValidString(option.Label) {
			return fmt.Errorf("ask option %d must have non-empty UTF-8 value and label", index)
		}
		if !utf8.ValidString(option.Description) {
			return fmt.Errorf("ask option %d description must be valid UTF-8", index)
		}
		if _, exists := values[option.Value]; exists {
			return fmt.Errorf("ask option %d duplicates value %q", index, option.Value)
		}
		values[option.Value] = struct{}{}
	}
	return nil
}

func cloneState(value interaction.State) interaction.State {
	value.Values = append([]interaction.Entry(nil), value.Values...)
	for index := range value.Values {
		value.Values[index].Value = cloneValue(value.Values[index].Value)
	}
	return value
}

func cloneValue(value interaction.Value) interaction.Value {
	value.Strings = append([]string(nil), value.Strings...)
	return value
}

func compactJSON(raw json.RawMessage) string {
	var buffer bytes.Buffer
	if json.Compact(&buffer, raw) != nil {
		return "null"
	}
	return buffer.String()
}
