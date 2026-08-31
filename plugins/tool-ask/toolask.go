// Package toolask exposes a tool for asking the user a question.
package toolask

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"unicode/utf8"

	"github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/tool"
)

const (
	defaultMaxPromptBytes   = 16 * 1024
	defaultMaxResponseBytes = 16 * 1024
	defaultMaxOptions       = 8
	defaultMaxOptionsBytes  = 16 * 1024
	requestName             = "ask_user"
	answerFieldName         = "answer"
)

var (
	// ErrInvalidConfig indicates invalid tool.ask configuration.
	ErrInvalidConfig = errors.New("invalid tool.ask config")
	// ErrInvalidArguments indicates malformed ask_user arguments.
	ErrInvalidArguments = errors.New("invalid tool.ask arguments")
	// ErrPromptLimit indicates that the prompt exceeds its configured bound.
	ErrPromptLimit = errors.New("ask prompt exceeds configured limit")
	// ErrResponseLimit indicates that the response exceeds its configured bound.
	ErrResponseLimit = errors.New("ask response exceeds configured limit")
	// ErrOptionsLimit indicates that the options exceed their configured bounds.
	ErrOptionsLimit = errors.New("ask options exceed configured limit")
	// ErrInvalidResponse indicates that the host returned no unique string answer.
	ErrInvalidResponse = errors.New("invalid tool.ask interaction response")
)

// Config bounds prompt, option, and response sizes.
type Config struct {
	MaxPromptBytes   int `toml:"max_prompt_bytes"`
	MaxResponseBytes int `toml:"max_response_bytes"`
	MaxOptions       int `toml:"max_options"`
	MaxOptionsBytes  int `toml:"max_options_bytes"`
}

// Dependencies contains the host interaction channel.
type Dependencies struct {
	Interaction interaction.Channel
}

// Exports contains the ask_user tool.
type Exports struct{ Tools []tool.Tool }

type askTool struct {
	channel                          interaction.Channel
	maxPromptBytes, maxResponseBytes int
	maxOptions, maxOptionsBytes      int
}

type askArguments struct {
	Prompt  *string            `json:"prompt"`
	Options askOptionArguments `json:"options"`
}

type askOptionArgument struct {
	Label       *string `json:"label"`
	Description string  `json:"description"`
}

type askOptionArguments struct {
	present bool
	values  []askOptionArgument
}

func (o *askOptionArguments) UnmarshalJSON(raw []byte) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("options must be an array")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var values []askOptionArgument
	if err := decoder.Decode(&values); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("options contain trailing JSON")
		}
		return err
	}
	o.present = true
	o.values = values
	return nil
}

// New validates dependencies and creates the ask_user tool.
func New(ctx context.Context, cfg Config, deps Dependencies) (Exports, ingotabi.Cleanup, error) {
	if ctx == nil {
		return Exports{}, nil, fmt.Errorf("construct tool.ask: %w", ErrInvalidConfig)
	}
	if err := ctx.Err(); err != nil {
		return Exports{}, nil, err
	}
	if isNil(deps.Interaction) {
		return Exports{}, nil, fmt.Errorf("interaction dependency is required: %w", ErrInvalidConfig)
	}
	maxPrompt := cfg.MaxPromptBytes
	if maxPrompt == 0 {
		maxPrompt = defaultMaxPromptBytes
	}
	if maxPrompt < 1 {
		return Exports{}, nil, fmt.Errorf("max_prompt_bytes must be positive: %w", ErrInvalidConfig)
	}
	maxResponse := cfg.MaxResponseBytes
	if maxResponse == 0 {
		maxResponse = defaultMaxResponseBytes
	}
	if maxResponse < 1 {
		return Exports{}, nil, fmt.Errorf("max_response_bytes must be positive: %w", ErrInvalidConfig)
	}
	maxOptions := cfg.MaxOptions
	if maxOptions == 0 {
		maxOptions = defaultMaxOptions
	}
	if maxOptions < 1 {
		return Exports{}, nil, fmt.Errorf("max_options must be positive: %w", ErrInvalidConfig)
	}
	maxOptionsBytes := cfg.MaxOptionsBytes
	if maxOptionsBytes == 0 {
		maxOptionsBytes = defaultMaxOptionsBytes
	}
	if maxOptionsBytes < 1 {
		return Exports{}, nil, fmt.Errorf("max_options_bytes must be positive: %w", ErrInvalidConfig)
	}
	return Exports{Tools: []tool.Tool{&askTool{
		channel: deps.Interaction, maxPromptBytes: maxPrompt, maxResponseBytes: maxResponse,
		maxOptions: maxOptions, maxOptionsBytes: maxOptionsBytes,
	}}}, nil, nil
}

func (t *askTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "ask_user",
		Description: "Ask the host environment a question, optionally with suggested choices and a free-form response.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["prompt"],"properties":{"prompt":{"type":"string","minLength":1},"options":{"type":"array","minItems":1,"items":{"type":"object","additionalProperties":false,"required":["label"],"properties":{"label":{"type":"string","minLength":1},"description":{"type":"string"}}}}}}`),
	}
}

func (t *askTool) Invoke(ctx context.Context, call tool.Call) (tool.Result, error) {
	if ctx == nil {
		return tool.Result{}, fmt.Errorf("ask_user: nil context")
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	if call.Name != "" && call.Name != "ask_user" {
		return tool.Result{}, fmt.Errorf("call name %q: %w", call.Name, ErrInvalidArguments)
	}
	var args askArguments
	if err := decodeObject(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	if args.Prompt == nil || *args.Prompt == "" || !utf8.ValidString(*args.Prompt) {
		return tool.Result{}, fmt.Errorf("prompt must be a non-empty UTF-8 string: %w", ErrInvalidArguments)
	}
	if len([]byte(*args.Prompt)) > t.maxPromptBytes {
		return tool.Result{}, ErrPromptLimit
	}
	options, err := t.validateOptions(args.Options)
	if err != nil {
		return tool.Result{}, err
	}
	response, err := t.channel.Request(ctx, interaction.Request{
		Name:        requestName,
		Description: *args.Prompt,
		Fields: []interaction.Field{{
			Name:     answerFieldName,
			Label:    "Answer",
			Kind:     interaction.FieldString,
			Required: true,
			Options:  options,
		}},
	})
	if err != nil {
		return tool.Result{}, err
	}
	answer, ok := responseString(response, answerFieldName)
	if !ok {
		return tool.Result{}, ErrInvalidResponse
	}
	if !utf8.ValidString(answer) {
		return tool.Result{}, fmt.Errorf("response is not valid UTF-8")
	}
	if len([]byte(answer)) > t.maxResponseBytes {
		return tool.Result{}, ErrResponseLimit
	}
	return tool.Result{Content: content.FromText(answer)}, nil
}

func (t *askTool) validateOptions(raw askOptionArguments) ([]interaction.Option, error) {
	if !raw.present {
		return nil, nil
	}
	if len(raw.values) == 0 {
		return nil, fmt.Errorf("options must not be empty: %w", ErrInvalidArguments)
	}
	if len(raw.values) > t.maxOptions {
		return nil, ErrOptionsLimit
	}
	options := make([]interaction.Option, 0, len(raw.values))
	labels := make(map[string]struct{}, len(raw.values))
	totalBytes := 0
	for index, option := range raw.values {
		if option.Label == nil || *option.Label == "" || !utf8.ValidString(*option.Label) {
			return nil, fmt.Errorf("option %d label must be a non-empty UTF-8 string: %w", index, ErrInvalidArguments)
		}
		if !utf8.ValidString(option.Description) {
			return nil, fmt.Errorf("option %d description must be UTF-8: %w", index, ErrInvalidArguments)
		}
		if _, exists := labels[*option.Label]; exists {
			return nil, fmt.Errorf("option %d duplicates label %q: %w", index, *option.Label, ErrInvalidArguments)
		}
		labels[*option.Label] = struct{}{}
		totalBytes += len([]byte(*option.Label)) + len([]byte(option.Description))
		if totalBytes > t.maxOptionsBytes {
			return nil, ErrOptionsLimit
		}
		options = append(options, interaction.Option{Value: *option.Label, Label: *option.Label, Description: option.Description})
	}
	return options, nil
}

func responseString(response interaction.Response, name string) (string, bool) {
	var result string
	found := false
	for _, answer := range response.Values {
		if answer.Name != name {
			continue
		}
		if found || answer.Value.Kind != interaction.ValueString {
			return "", false
		}
		result = answer.Value.String
		found = true
	}
	return result, found
}

func decodeObject(raw json.RawMessage, target any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return fmt.Errorf("arguments are required: %w", ErrInvalidArguments)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode arguments: %w: %w", ErrInvalidArguments, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("arguments contain trailing JSON: %w", ErrInvalidArguments)
		}
		return fmt.Errorf("decode trailing arguments: %w: %w", ErrInvalidArguments, err)
	}
	return nil
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	}
	return false
}
