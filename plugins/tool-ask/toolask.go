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

	"github.com/ingot-agent/sdk"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/tool"
)

const (
	defaultMaxPromptBytes   = 16 * 1024
	defaultMaxResponseBytes = 16 * 1024
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
)

// Config bounds prompt and response sizes.
type Config struct {
	MaxPromptBytes   int `toml:"max_prompt_bytes"`
	MaxResponseBytes int `toml:"max_response_bytes"`
}

// Dependencies contains the user interaction channel.
type Dependencies struct {
	Interaction interaction.Channel
}

// Exports contains the ask_user tool.
type Exports struct{ Tools []tool.Tool }

type askTool struct {
	channel                          interaction.Channel
	maxPromptBytes, maxResponseBytes int
}

// New validates dependencies and creates the ask_user tool.
func New(ctx context.Context, cfg Config, deps Dependencies) (Exports, sdk.Cleanup, error) {
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
	return Exports{Tools: []tool.Tool{&askTool{channel: deps.Interaction, maxPromptBytes: maxPrompt, maxResponseBytes: maxResponse}}}, nil, nil
}

func (t *askTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "ask_user",
		Description: "Ask the user a question and return the response.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["prompt"],"properties":{"prompt":{"type":"string","minLength":1}}}`),
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
	var args struct {
		Prompt *string `json:"prompt"`
	}
	if err := decodeObject(call.Arguments, &args); err != nil {
		return tool.Result{}, err
	}
	if args.Prompt == nil || *args.Prompt == "" || !utf8.ValidString(*args.Prompt) {
		return tool.Result{}, fmt.Errorf("prompt must be a non-empty UTF-8 string: %w", ErrInvalidArguments)
	}
	if len([]byte(*args.Prompt)) > t.maxPromptBytes {
		return tool.Result{}, ErrPromptLimit
	}
	response, err := t.channel.Ask(ctx, interaction.AskRequest{Prompt: *args.Prompt})
	if err != nil {
		return tool.Result{}, err
	}
	if !utf8.ValidString(response.Text) {
		return tool.Result{}, fmt.Errorf("response is not valid UTF-8")
	}
	if len([]byte(response.Text)) > t.maxResponseBytes {
		return tool.Result{}, ErrResponseLimit
	}
	return tool.Result{Content: response.Text}, nil
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
