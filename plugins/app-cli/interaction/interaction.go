// Package interactioncomponent provides the terminal interaction component
// of the app.cli composite plugin.
package interactioncomponent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	appcli "github.com/ingot-agent/app-cli"
	"github.com/ingot-agent/sdk"
	"github.com/ingot-agent/sdk/interaction"
)

const defaultMaxLineBytes = 64 * 1024

var (
	// ErrInvalidConfig indicates invalid terminal configuration.
	ErrInvalidConfig = errors.New("invalid app.cli interaction config")
	// ErrInputLimit indicates that one complete input line exceeded its byte limit.
	ErrInputLimit = appcli.ErrInputLimit
	// ErrInvalidInput indicates invalid UTF-8 terminal input.
	ErrInvalidInput = appcli.ErrInvalidInput
)

// Dependencies contains no consumed capabilities.
type Dependencies struct{}

// Exports contains the terminal interaction channel.
type Exports struct {
	Channel interaction.Channel
}

type inputDriver interface {
	ReadLine(context.Context, int) (string, error)
	Close() error
}

type channel struct {
	runCtx      context.Context
	cancel      context.CancelFunc
	driver      inputDriver
	inputGate   chan struct{}
	outputMu    sync.Mutex
	lifecycleMu sync.Mutex
	closed      bool
	active      sync.WaitGroup
	stdout      io.Writer
	stderr      io.Writer
	color       bool
	maxLine     int
	askPrompt   string
}

// New initializes terminal state and returns promptly without reading input.
func New(ctx context.Context, cfg appcli.Config, _ Dependencies) (Exports, sdk.Cleanup, error) {
	if ctx == nil {
		return Exports{}, nil, fmt.Errorf("construct app.cli interaction: %w", ErrInvalidConfig)
	}
	if err := ctx.Err(); err != nil {
		return Exports{}, nil, err
	}
	normalized, err := normalizeConfig(cfg.Interaction)
	if err != nil {
		return Exports{}, nil, err
	}
	runCtx, cancel := context.WithCancel(ctx)
	instance := &channel{
		runCtx: runCtx, cancel: cancel, inputGate: make(chan struct{}, 1),
		stdout: os.Stdout, stderr: os.Stderr, color: normalized.color,
		maxLine: normalized.maxLine, askPrompt: normalized.askPrompt,
	}
	instance.inputGate <- struct{}{}
	driver, err := newTerminalInput(os.Stdin, os.Stdout)
	if err != nil {
		cancel()
		return Exports{}, nil, fmt.Errorf("initialize terminal input: %w", err)
	}
	instance.driver = driver
	cleanup := sdk.Cleanup(func(cleanupCtx context.Context) error {
		instance.lifecycleMu.Lock()
		instance.closed = true
		instance.cancel()
		instance.lifecycleMu.Unlock()
		instance.active.Wait()
		closeErr := instance.driver.Close()
		if cleanupCtx == nil {
			return errors.Join(closeErr, context.Canceled)
		}
		if cleanupCtx.Err() != nil {
			return errors.Join(closeErr, cleanupCtx.Err())
		}
		return closeErr
	})
	return Exports{Channel: instance}, cleanup, nil
}

type normalizedConfig struct {
	askPrompt string
	color     bool
	maxLine   int
}

func normalizeConfig(cfg appcli.InteractionConfig) (normalizedConfig, error) {
	if !utf8.ValidString(cfg.InputPrompt) || !utf8.ValidString(cfg.AskPrompt) {
		return normalizedConfig{}, fmt.Errorf("prompts must be valid UTF-8: %w", ErrInvalidConfig)
	}
	if cfg.InputPrompt == "" {
		cfg.InputPrompt = "> "
	}
	if cfg.AskPrompt == "" {
		cfg.AskPrompt = "? "
	}
	mode := cfg.Color
	if mode == "" {
		mode = "auto"
	}
	if mode != "auto" && mode != "always" && mode != "never" {
		return normalizedConfig{}, fmt.Errorf("color must be auto, always, or never: %w", ErrInvalidConfig)
	}
	maxLine := cfg.MaxLineBytes
	if maxLine == 0 {
		maxLine = defaultMaxLineBytes
	}
	if maxLine < 1 {
		return normalizedConfig{}, fmt.Errorf("max_line_bytes must be positive: %w", ErrInvalidConfig)
	}
	color := mode == "always" || (mode == "auto" && isTerminal(os.Stdout))
	return normalizedConfig{askPrompt: cfg.AskPrompt, color: color, maxLine: maxLine}, nil
}

func (c *channel) Ask(ctx context.Context, request interaction.AskRequest) (interaction.AskResponse, error) {
	line, err := c.withInput(ctx, func(callCtx context.Context) (string, error) {
		if len(request.Options) == 0 {
			prompt := request.Prompt
			if prompt != "" {
				prompt += "\n"
			}
			return c.readLineHeld(callCtx, prompt+c.askPrompt)
		}
		if err := validateAskRequest(request); err != nil {
			return "", err
		}
		return c.readChoiceHeld(callCtx, request)
	})
	if err != nil {
		return interaction.AskResponse{}, err
	}
	return interaction.AskResponse{Text: line}, nil
}

func (c *channel) ReadLine(ctx context.Context, prompt string) (string, error) {
	return c.withInput(ctx, func(callCtx context.Context) (string, error) {
		return c.readLineHeld(callCtx, prompt)
	})
}

func (c *channel) withInput(ctx context.Context, operation func(context.Context) (string, error)) (string, error) {
	if ctx == nil {
		return "", interaction.ErrUnavailable
	}
	c.lifecycleMu.Lock()
	if c.closed {
		c.lifecycleMu.Unlock()
		return "", interaction.ErrUnavailable
	}
	c.active.Add(1)
	c.lifecycleMu.Unlock()
	defer c.active.Done()
	callCtx, cancel := mergeContext(ctx, c.runCtx)
	defer cancel()
	select {
	case <-callCtx.Done():
		return "", callCtx.Err()
	case <-c.inputGate:
	}
	defer func() { c.inputGate <- struct{}{} }()
	return operation(callCtx)
}

func (c *channel) readLineHeld(ctx context.Context, prompt string) (string, error) {
	if prompt != "" {
		c.outputMu.Lock()
		_, err := io.WriteString(c.stdout, prompt)
		c.outputMu.Unlock()
		if err != nil {
			return "", fmt.Errorf("write input prompt: %w", err)
		}
	}
	line, err := c.driver.ReadLine(ctx, c.maxLine)
	if err != nil {
		return "", err
	}
	if !utf8.ValidString(line) {
		return "", ErrInvalidInput
	}
	return line, nil
}

func (c *channel) readChoiceHeld(ctx context.Context, request interaction.AskRequest) (string, error) {
	prompt := formatChoicePrompt(request, c.askPrompt)
	for {
		line, err := c.readLineHeld(ctx, prompt)
		if err != nil {
			return "", err
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			prompt = "Please choose an option or enter your own response.\n" + c.askPrompt
			continue
		}
		if selected, err := strconv.Atoi(trimmed); err == nil {
			switch {
			case selected >= 1 && selected <= len(request.Options):
				return request.Options[selected-1].Label, nil
			case request.AllowTextInput && selected == len(request.Options)+1:
				return c.readLineHeld(ctx, "Enter your response:\n"+c.askPrompt)
			default:
				prompt = "Please choose one of the listed numbers.\n" + c.askPrompt
				continue
			}
		}
		for _, option := range request.Options {
			if line == option.Label {
				return option.Label, nil
			}
		}
		if request.AllowTextInput {
			return line, nil
		}
		prompt = "Please choose one of the listed options.\n" + c.askPrompt
	}
}

func validateAskRequest(request interaction.AskRequest) error {
	if !utf8.ValidString(request.Prompt) {
		return fmt.Errorf("ask prompt must be valid UTF-8")
	}
	labels := make(map[string]struct{}, len(request.Options))
	for index, option := range request.Options {
		if option.Label == "" || !utf8.ValidString(option.Label) {
			return fmt.Errorf("ask option %d label must be a non-empty UTF-8 string", index)
		}
		if !utf8.ValidString(option.Description) {
			return fmt.Errorf("ask option %d description must be valid UTF-8", index)
		}
		if _, exists := labels[option.Label]; exists {
			return fmt.Errorf("ask option %d duplicates label %q", index, option.Label)
		}
		labels[option.Label] = struct{}{}
	}
	return nil
}

func formatChoicePrompt(request interaction.AskRequest, askPrompt string) string {
	var prompt strings.Builder
	if request.Prompt != "" {
		prompt.WriteString(request.Prompt)
		prompt.WriteByte('\n')
	}
	for index, option := range request.Options {
		fmt.Fprintf(&prompt, "%d. %s\n", index+1, option.Label)
		if option.Description != "" {
			fmt.Fprintf(&prompt, "   %s\n", option.Description)
		}
	}
	if request.AllowTextInput {
		fmt.Fprintf(&prompt, "%d. Other (enter your own response)\n", len(request.Options)+1)
	}
	prompt.WriteString(askPrompt)
	return prompt.String()
}

func (c *channel) Render(ctx context.Context, event interaction.Event) error {
	if ctx == nil || isNil(event) {
		return interaction.ErrUnavailable
	}
	callCtx, cancel := mergeContext(ctx, c.runCtx)
	defer cancel()
	if err := callCtx.Err(); err != nil {
		return err
	}
	c.outputMu.Lock()
	defer c.outputMu.Unlock()
	switch value := event.(type) {
	case interaction.TextEvent:
		_, err := io.WriteString(c.stdout, value.Text)
		return err
	case interaction.StatusEvent:
		return c.writeLine(c.stdout, value.Text, "36")
	case interaction.ErrorEvent:
		if value.Err == nil {
			return nil
		}
		return c.writeLine(c.stderr, "error: "+value.Err.Error(), "31")
	case interaction.ToolCallEvent:
		arguments := compactJSON(value.Call.Arguments)
		return c.writeLine(c.stdout, fmt.Sprintf("tool %s (%s): %s", value.Call.Name, value.Call.ID, arguments), "36")
	case interaction.ToolResultEvent:
		return c.writeLine(c.stdout, fmt.Sprintf("tool %s (%s) => %s", value.Call.Name, value.Call.ID, value.Result.Content), "36")
	default:
		return fmt.Errorf("unsupported interaction event %T", event)
	}
}

func (c *channel) writeLine(writer io.Writer, text, color string) error {
	if c.color {
		_, err := fmt.Fprintf(writer, "\x1b[%sm%s\x1b[0m\n", color, text)
		return err
	}
	_, err := fmt.Fprintln(writer, text)
	return err
}

func compactJSON(raw json.RawMessage) string {
	var buffer bytes.Buffer
	if json.Compact(&buffer, raw) != nil {
		return "null"
	}
	return buffer.String()
}

func mergeContext(caller, owner context.Context) (context.Context, context.CancelFunc) {
	merged, cancel := context.WithCancel(caller)
	stop := context.AfterFunc(owner, cancel)
	return merged, func() {
		stop()
		cancel()
	}
}

func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

var _ interaction.Channel = (*channel)(nil)
