// Package interactioncomponent provides the terminal interaction component
// of the app.cli composite plugin.
package interactioncomponent

import (
	"context"
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
	ingotabi "github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/ingot-abi/invocation"
	"github.com/ingot-agent/ingot-abi/lifecycle"
	"github.com/ingot-agent/sdk/interaction"
)

const defaultMaxLineBytes = 64 * 1024

var (
	// ErrInvalidConfig indicates invalid terminal configuration.
	ErrInvalidConfig = errors.New("invalid app.cli interaction config")
	// ErrTerminalInUse indicates that another app.cli instance owns the process terminal.
	ErrTerminalInUse = errors.New("process terminal is already in use")
	// ErrInputLimit indicates that one complete input line exceeded its byte limit.
	ErrInputLimit = appcli.ErrInputLimit
	// ErrInvalidInput indicates invalid UTF-8 terminal input.
	ErrInvalidInput = appcli.ErrInvalidInput
)

// Dependencies contains the runtime host capabilities consumed by the
// terminal component.
type Dependencies struct {
	// Invocation is the runtime invocation metadata injected by the
	// generated runtime.
	Invocation invocation.Invocation
	// Lifecycle is the runtime shutdown request interface injected by the
	// generated runtime.
	Lifecycle lifecycle.Controller
}

// Exports contains the terminal interaction channel and CLI line input.
type Exports struct {
	Channel  interaction.Channel
	Frontend appcli.Frontend
}

type inputDriver interface {
	ReadLine(context.Context, int) (string, error)
	Close() error
}

type channel struct {
	runCtx       context.Context
	cancel       context.CancelFunc
	driver       inputDriver
	inputGate    chan struct{}
	outputMu     sync.Mutex
	stateMu      sync.Mutex
	states       map[string]interaction.State
	lifecycleMu  sync.Mutex
	closed       bool
	active       int
	activeDone   chan struct{}
	resourceOnce sync.Once
	resourceErr  error
	releaseLease func()
	stdout       io.Writer
	stderr       io.Writer
	color        bool
	maxLine      int
	askPrompt    string
}

var processTerminalLease struct {
	sync.Mutex
	held bool
}

// New initializes terminal state and returns promptly without reading input.
func New(ctx context.Context, cfg appcli.Config, deps Dependencies) (Exports, ingotabi.Cleanup, error) {
	if ctx == nil {
		return Exports{}, nil, fmt.Errorf("construct app.cli interaction: %w", ErrInvalidConfig)
	}
	if err := ctx.Err(); err != nil {
		return Exports{}, nil, err
	}
	if isNil(deps.Invocation) || isNil(deps.Lifecycle) {
		return Exports{}, nil, fmt.Errorf("construct app.cli interaction: %w", ErrInvalidConfig)
	}
	normalized, err := normalizeConfig(cfg.Interaction)
	if err != nil {
		return Exports{}, nil, err
	}
	if deps.Invocation.Mode() == invocation.ModeCheck {
		runCtx, cancel := context.WithCancel(ctx)
		instance := &channel{
			runCtx: runCtx, cancel: cancel, inputGate: make(chan struct{}, 1),
			stdout: io.Discard, stderr: io.Discard, maxLine: normalized.maxLine, askPrompt: normalized.askPrompt,
			states: make(map[string]interaction.State),
		}
		instance.inputGate <- struct{}{}
		return Exports{Channel: instance, Frontend: instance}, ingotabi.Cleanup(instance.cleanup), nil
	}
	mode, err := appcli.ParseArguments(deps.Invocation.Arguments())
	if err != nil {
		return Exports{}, nil, err
	}
	if mode != appcli.ModePlain {
		return newTUI(ctx, cfg, deps.Invocation, deps.Lifecycle)
	}
	releaseLease, ok := acquireTerminalLease()
	if !ok {
		return Exports{}, nil, ErrTerminalInUse
	}
	runCtx, cancel := context.WithCancel(ctx)
	instance := &channel{
		runCtx: runCtx, cancel: cancel, inputGate: make(chan struct{}, 1),
		stdout: os.Stdout, stderr: os.Stderr, color: false,
		maxLine: normalized.maxLine, askPrompt: normalized.askPrompt, states: make(map[string]interaction.State),
	}
	instance.inputGate <- struct{}{}
	driver, err := newTerminalInput(os.Stdin, os.Stdout)
	if err != nil {
		cancel()
		releaseLease()
		return Exports{}, nil, fmt.Errorf("initialize terminal input: %w", err)
	}
	instance.driver = driver
	instance.releaseLease = releaseLease
	cleanup := ingotabi.Cleanup(instance.cleanup)
	return Exports{Channel: instance, Frontend: instance}, cleanup, nil
}

func (c *channel) cleanup(ctx context.Context) error {
	c.lifecycleMu.Lock()
	c.closed = true
	c.cancel()
	activeDone := c.activeDone
	c.lifecycleMu.Unlock()

	if activeDone != nil {
		if ctx == nil {
			return context.Canceled
		}
		select {
		case <-activeDone:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	c.resourceOnce.Do(func() {
		if c.driver != nil {
			c.resourceErr = c.driver.Close()
		}
		if c.releaseLease != nil {
			c.releaseLease()
		}
	})
	if ctx == nil {
		return errors.Join(c.resourceErr, context.Canceled)
	}
	return errors.Join(c.resourceErr, ctx.Err())
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
	color := mode == "always" || (mode == "auto" && supportsColor(os.Stdout))
	return normalizedConfig{askPrompt: cfg.AskPrompt, color: color, maxLine: maxLine}, nil
}

func (c *channel) Request(ctx context.Context, request interaction.Request) (interaction.Response, error) {
	return collectResponse(ctx, request, c.ask)
}

func (c *channel) ask(ctx context.Context, request askRequest) (string, error) {
	line, err := c.withInput(ctx, func(callCtx context.Context) (string, error) {
		if err := validateAskRequest(request); err != nil {
			return "", err
		}
		if len(request.Options) == 0 {
			prompt := request.Prompt
			if prompt != "" {
				prompt += "\n"
			}
			return c.readLineHeld(callCtx, prompt+c.askPrompt)
		}
		return c.readChoiceHeld(callCtx, request)
	})
	if err != nil {
		return "", err
	}
	return line, nil
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
	if c.active == 0 {
		c.activeDone = make(chan struct{})
	}
	c.active++
	c.lifecycleMu.Unlock()
	defer c.finishInput()
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

func (c *channel) finishInput() {
	c.lifecycleMu.Lock()
	c.active--
	if c.active == 0 {
		close(c.activeDone)
		c.activeDone = nil
	}
	c.lifecycleMu.Unlock()
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

func (c *channel) readChoiceHeld(ctx context.Context, request askRequest) (string, error) {
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
				return request.Options[selected-1].Value, nil
			case request.AllowTextInput && selected == len(request.Options)+1:
				return c.readNonEmptyLineHeld(ctx, "Enter your response:\n"+c.askPrompt)
			default:
				prompt = "Please choose one of the listed numbers.\n" + c.askPrompt
				continue
			}
		}
		for _, option := range request.Options {
			if line == option.Label || line == option.Value {
				return option.Value, nil
			}
		}
		if request.AllowTextInput {
			return line, nil
		}
		prompt = "Please choose one of the listed options.\n" + c.askPrompt
	}
}

func (c *channel) readNonEmptyLineHeld(ctx context.Context, prompt string) (string, error) {
	for {
		line, err := c.readLineHeld(ctx, prompt)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(line) != "" {
			return line, nil
		}
		prompt = "Please enter a response.\n" + c.askPrompt
	}
}

func formatChoicePrompt(request askRequest, askPrompt string) string {
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

func (c *channel) Emit(ctx context.Context, event interaction.Event) error {
	if ctx == nil || !utf8.ValidString(event.Name) || !utf8.ValidString(event.Message) {
		return interaction.ErrUnavailable
	}
	callCtx, cancel := mergeContext(ctx, c.runCtx)
	defer cancel()
	if err := callCtx.Err(); err != nil {
		return err
	}
	c.outputMu.Lock()
	defer c.outputMu.Unlock()
	message := event.Message
	if message == "" {
		message = event.Name
	}
	if event.Level == interaction.LevelError {
		return c.writeLine(c.stderr, "error: "+message, "31")
	}
	return c.writeLine(c.stdout, message, "36")
}

func (c *channel) Set(ctx context.Context, value interaction.State) error {
	if ctx == nil || value.Name == "" || !utf8.ValidString(value.Name) {
		return interaction.ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	c.stateMu.Lock()
	if c.states == nil {
		c.states = make(map[string]interaction.State)
	}
	c.states[value.Name] = cloneState(value)
	c.stateMu.Unlock()
	return nil
}

func (c *channel) Clear(ctx context.Context, name string) error {
	if ctx == nil || name == "" || !utf8.ValidString(name) {
		return interaction.ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	c.stateMu.Lock()
	delete(c.states, name)
	c.stateMu.Unlock()
	return nil
}

func (c *channel) Sync(ctx context.Context, _ appcli.SessionView) error {
	if ctx == nil {
		return interaction.ErrUnavailable
	}
	return ctx.Err()
}

func (c *channel) StartTurn(ctx context.Context, _ string) error {
	if ctx == nil {
		return interaction.ErrUnavailable
	}
	return ctx.Err()
}

func (c *channel) FinishTurn(ctx context.Context, _ appcli.TurnState) error {
	if ctx == nil {
		return interaction.ErrUnavailable
	}
	return ctx.Err()
}

func (*channel) Interrupts() <-chan appcli.Interrupt { return nil }

func (c *channel) writeLine(writer io.Writer, text, color string) error {
	if c.color {
		_, err := fmt.Fprintf(writer, "\x1b[%sm%s\x1b[0m\n", color, text)
		return err
	}
	_, err := fmt.Fprintln(writer, text)
	return err
}

func mergeContext(caller, owner context.Context) (context.Context, context.CancelFunc) {
	merged, cancel := context.WithCancel(caller)
	stop := context.AfterFunc(owner, cancel)
	return merged, func() {
		stop()
		cancel()
	}
}

func acquireTerminalLease() (func(), bool) {
	processTerminalLease.Lock()
	defer processTerminalLease.Unlock()
	if processTerminalLease.held {
		return nil, false
	}
	processTerminalLease.held = true
	var once sync.Once
	return func() {
		once.Do(func() {
			processTerminalLease.Lock()
			processTerminalLease.held = false
			processTerminalLease.Unlock()
		})
	}, true
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
var _ appcli.LineInput = (*channel)(nil)
var _ appcli.Frontend = (*channel)(nil)
