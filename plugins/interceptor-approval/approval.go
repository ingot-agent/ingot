// Package approval provides a fail-closed tool approval interceptor.
package approval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ingot-agent/sdk"
	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/pipeline"
	"github.com/ingot-agent/sdk/tool"
)

const (
	actionAllow            = "allow"
	actionAsk              = "ask"
	actionDeny             = "deny"
	displayFull            = "full"
	displayNamesOnly       = "name-only"
	defaultMaxDisplayBytes = 4096
	maxAttempts            = 3
)

var (
	// ErrInvalidConfig indicates invalid approval policy configuration.
	ErrInvalidConfig = errors.New("invalid interceptor.approval config")
	// ErrApprovalDenied indicates that a call was denied or not approved.
	ErrApprovalDenied = errors.New("tool call approval denied")
	// ErrApprovalUnavailable indicates that an ask decision has no interaction channel.
	ErrApprovalUnavailable = errors.New("tool approval unavailable")
)

// Rule overrides the configured default action for one exact tool name.
type Rule struct {
	Tool   string `toml:"tool"`
	Action string `toml:"action"`
}

// Config controls approval decisions and prompt rendering.
type Config struct {
	DefaultAction   string `toml:"default_action"`
	ArgumentDisplay string `toml:"argument_display"`
	MaxDisplayBytes int    `toml:"max_display_bytes"`
	Rules           []Rule `toml:"rules"`
}

// Dependencies contains an optional interaction channel.
type Dependencies struct {
	Interaction sdk.Optional[interaction.Channel]
}

// Exports contains the approval interceptor.
type Exports struct{ Interceptors []tool.Interceptor }

type approvalInterceptor struct {
	defaultAction string
	display       string
	maxDisplay    int
	rules         map[string]string
	interaction   sdk.Optional[interaction.Channel]
}

// New validates immutable configuration. A missing interaction channel is
// allowed at startup so allow-only configurations remain usable; ask fails closed at call time.
func New(ctx context.Context, cfg Config, deps Dependencies) (Exports, sdk.Cleanup, error) {
	if ctx == nil {
		return Exports{}, nil, fmt.Errorf("construct interceptor.approval: %w", ErrInvalidConfig)
	}
	if err := ctx.Err(); err != nil {
		return Exports{}, nil, err
	}
	defaultAction := cfg.DefaultAction
	if defaultAction == "" {
		defaultAction = actionAsk
	}
	if !validAction(defaultAction) {
		return Exports{}, nil, fmt.Errorf("invalid default_action %q: %w", defaultAction, ErrInvalidConfig)
	}
	display := cfg.ArgumentDisplay
	if display == "" {
		display = displayFull
	}
	if display != displayFull && display != displayNamesOnly {
		return Exports{}, nil, fmt.Errorf("invalid argument_display %q: %w", display, ErrInvalidConfig)
	}
	maxDisplay := cfg.MaxDisplayBytes
	if maxDisplay == 0 {
		maxDisplay = defaultMaxDisplayBytes
	}
	if maxDisplay < 1 {
		return Exports{}, nil, fmt.Errorf("max_display_bytes must be positive: %w", ErrInvalidConfig)
	}
	rules := make(map[string]string, len(cfg.Rules))
	for i, rule := range cfg.Rules {
		if rule.Tool == "" || !utf8.ValidString(rule.Tool) {
			return Exports{}, nil, fmt.Errorf("rules[%d].tool must be non-empty UTF-8: %w", i, ErrInvalidConfig)
		}
		if !validAction(rule.Action) {
			return Exports{}, nil, fmt.Errorf("rules[%d].action is invalid: %w", i, ErrInvalidConfig)
		}
		if _, exists := rules[rule.Tool]; exists {
			return Exports{}, nil, fmt.Errorf("duplicate rule for %q: %w", rule.Tool, ErrInvalidConfig)
		}
		rules[rule.Tool] = rule.Action
	}
	return Exports{Interceptors: []tool.Interceptor{&approvalInterceptor{defaultAction: defaultAction, display: display, maxDisplay: maxDisplay, rules: rules, interaction: deps.Interaction}}}, nil, nil
}

func validAction(action string) bool {
	return action == actionAllow || action == actionAsk || action == actionDeny
}

func (a *approvalInterceptor) Invoke(ctx context.Context, call tool.Call, next pipeline.Next[tool.Call, tool.Result]) (tool.Result, error) {
	if ctx == nil {
		return tool.Result{}, fmt.Errorf("approval: nil context")
	}
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	action := a.defaultAction
	if override, ok := a.rules[call.Name]; ok {
		action = override
	}
	switch action {
	case actionAllow:
		if next == nil {
			return tool.Result{}, errors.New("approval: nil next")
		}
		return next(ctx, call)
	case actionDeny:
		return tool.Result{}, fmt.Errorf("tool %q: %w", call.Name, ErrApprovalDenied)
	case actionAsk:
		return a.ask(ctx, call, next)
	default:
		return tool.Result{}, fmt.Errorf("unknown approval action %q: %w", action, ErrInvalidConfig)
	}
}

func (a *approvalInterceptor) ask(ctx context.Context, call tool.Call, next pipeline.Next[tool.Call, tool.Result]) (tool.Result, error) {
	if next == nil {
		return tool.Result{}, errors.New("approval: nil next")
	}
	if !a.interaction.Valid || isNil(a.interaction.Value) {
		return tool.Result{}, fmt.Errorf("tool %q: %w: %w", call.Name, ErrApprovalUnavailable, interaction.ErrUnavailable)
	}
	prompt := a.prompt(call)
	for attempt := 0; attempt < maxAttempts; attempt++ {
		response, err := a.interaction.Value.Ask(ctx, interaction.AskRequest{Prompt: prompt})
		if err != nil {
			return tool.Result{}, err
		}
		switch strings.ToLower(strings.TrimSpace(response.Text)) {
		case "y", "yes":
			return next(ctx, call)
		case "n", "no":
			return tool.Result{}, fmt.Errorf("tool %q: %w", call.Name, ErrApprovalDenied)
		}
	}
	return tool.Result{}, fmt.Errorf("tool %q: %w", call.Name, ErrApprovalDenied)
}

func (a *approvalInterceptor) prompt(call tool.Call) string {
	arguments := displayArguments(call.Arguments, a.display)
	arguments = truncate(arguments, a.maxDisplay)
	return fmt.Sprintf("Approval required for tool %q (call %q).\nArguments: %s\nApprove? [y/N]", call.Name, call.ID, arguments)
}

func displayArguments(raw json.RawMessage, mode string) string {
	if mode == displayNamesOnly {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err == nil {
			names := make([]string, 0, len(object))
			for name := range object {
				names = append(names, name)
			}
			sort.Strings(names)
			encoded, _ := json.Marshal(names)
			return string(encoded)
		}
		return "[]"
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err == nil {
		return compact.String()
	}
	return "null"
}

func truncate(value string, maxBytes int) string {
	if len([]byte(value)) <= maxBytes {
		return value
	}
	const marker = "...[truncated]"
	markerBytes := []byte(marker)
	if maxBytes <= len(markerBytes) {
		return string(markerBytes[:maxBytes])
	}
	prefix := []byte(value)[:maxBytes-len(markerBytes)]
	for !utf8.Valid(prefix) && len(prefix) > 0 {
		prefix = prefix[:len(prefix)-1]
	}
	return string(prefix) + marker
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

var _ tool.Interceptor = (*approvalInterceptor)(nil)
