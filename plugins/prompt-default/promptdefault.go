// Package promptdefault deterministically combines system configuration,
// contributor blocks, session history, and the current input.
package promptdefault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/prompt"
	"github.com/ingot-agent/sdk/tool"
)

const (
	defaultMaxBlockBytes  = 64 * 1024
	defaultMaxSystemBytes = 256 * 1024
)

var (
	// ErrInvalidConfig indicates invalid prompt limits or configured text.
	ErrInvalidConfig = errors.New("invalid prompt.default config")
	// ErrInvalidRequest indicates invalid input or history data.
	ErrInvalidRequest = errors.New("invalid prompt request")
	// ErrInvalidBlock indicates invalid contributor output.
	ErrInvalidBlock = errors.New("invalid prompt block")
	// ErrSystemLimit indicates that formatted system content exceeded its total limit.
	ErrSystemLimit = errors.New("system prompt limit exceeded")
)

// Config controls static prompt content and byte limits.
type Config struct {
	SystemPrompt   string `toml:"system_prompt"`
	MaxBlockBytes  int    `toml:"max_block_bytes"`
	MaxSystemBytes int    `toml:"max_system_bytes"`
}

// Dependencies contains contributors in stable MANY order.
type Dependencies struct {
	Contributors []prompt.Contributor
}

// Exports contains the renderer capability.
type Exports struct {
	Renderer prompt.Renderer
}

type renderer struct {
	systemPrompt string
	maxBlock     int
	maxSystem    int
	contributors []prompt.Contributor
}

// New validates configuration and snapshots the contributor collection.
func New(ctx context.Context, cfg Config, deps Dependencies) (Exports, ingotabi.Cleanup, error) {
	if ctx == nil {
		return Exports{}, nil, fmt.Errorf("construct prompt.default: %w", ErrInvalidConfig)
	}
	if err := ctx.Err(); err != nil {
		return Exports{}, nil, err
	}
	if !utf8.ValidString(cfg.SystemPrompt) {
		return Exports{}, nil, fmt.Errorf("system_prompt is invalid UTF-8: %w", ErrInvalidConfig)
	}
	maxBlock, err := positiveDefault(cfg.MaxBlockBytes, defaultMaxBlockBytes, "max_block_bytes")
	if err != nil {
		return Exports{}, nil, err
	}
	maxSystem, err := positiveDefault(cfg.MaxSystemBytes, defaultMaxSystemBytes, "max_system_bytes")
	if err != nil {
		return Exports{}, nil, err
	}
	if len([]byte(cfg.SystemPrompt)) > maxSystem {
		return Exports{}, nil, fmt.Errorf("system_prompt exceeds max_system_bytes: %w: %w", ErrInvalidConfig, ErrSystemLimit)
	}
	contributors := make([]prompt.Contributor, len(deps.Contributors))
	for i, contributor := range deps.Contributors {
		if isNil(contributor) {
			return Exports{}, nil, fmt.Errorf("contributors[%d] is nil: %w", i, ErrInvalidConfig)
		}
		contributors[i] = contributor
	}
	return Exports{Renderer: &renderer{systemPrompt: cfg.SystemPrompt, maxBlock: maxBlock, maxSystem: maxSystem, contributors: contributors}}, nil, nil
}

func positiveDefault(value, fallback int, field string) (int, error) {
	if value == 0 {
		return fallback, nil
	}
	if value < 1 {
		return 0, fmt.Errorf("%s must be positive: %w", field, ErrInvalidConfig)
	}
	return value, nil
}

func (r *renderer) Render(ctx context.Context, request prompt.Request) ([]model.Message, error) {
	if ctx == nil {
		return nil, fmt.Errorf("render prompt: nil context: %w", ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := content.Validate(request.Input); err != nil {
		return nil, fmt.Errorf("input: %w: %w", ErrInvalidRequest, err)
	}
	for i, message := range request.History {
		if err := content.Validate(message.Content); err != nil {
			return nil, fmt.Errorf("history[%d]: %w: %w", i, ErrInvalidRequest, err)
		}
	}
	original := cloneRequest(request)
	blocks := make([]prompt.Block, 0)
	for i, contributor := range r.contributors {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		produced, err := contributor.Contribute(ctx, cloneRequest(original))
		if err != nil {
			return nil, fmt.Errorf("contributor %d: %w", i, err)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for j, block := range produced {
			if block.Name == "" || !utf8.ValidString(block.Name) || strings.ContainsAny(block.Name, "\r\n") {
				return nil, fmt.Errorf("contributor %d block %d name: %w", i, j, ErrInvalidBlock)
			}
			if err := content.Validate(block.Content); err != nil {
				return nil, fmt.Errorf("contributor %d block %d content: %w: %w", i, j, ErrInvalidBlock, err)
			}
			if contentBytes(block.Content) > r.maxBlock {
				return nil, fmt.Errorf("contributor %d block %d content: %w", i, j, ErrInvalidBlock)
			}
			blocks = append(blocks, prompt.Block{Name: block.Name, Content: content.Clone(block.Content)})
		}
	}

	system, err := r.formatSystem(blocks)
	if err != nil {
		return nil, err
	}
	result := make([]model.Message, 0, len(original.History)+2)
	if len(system) != 0 {
		result = append(result, model.Message{Role: model.RoleSystem, Content: system})
	}
	result = append(result, cloneMessages(original.History)...)
	result = append(result, model.Message{Role: model.RoleUser, Content: original.Input})
	return result, nil
}

func (r *renderer) formatSystem(blocks []prompt.Block) (content.Content, error) {
	result := make(content.Content, 0, len(blocks)*2+1)
	total := 0
	if r.systemPrompt != "" {
		value := r.systemPrompt
		if len(blocks) != 0 {
			value += "\n\n"
		}
		result = append(result, content.Text(value))
		total += len(value)
	}
	for i, block := range blocks {
		title := "## " + block.Name + "\n"
		if i > 0 {
			title = "\n\n" + title
		}
		if total > r.maxSystem-len(title) {
			return nil, ErrSystemLimit
		}
		result = append(result, content.Text(title))
		total += len(title)
		blockBytes := contentBytes(block.Content)
		if blockBytes > r.maxSystem-total {
			return nil, ErrSystemLimit
		}
		result = append(result, content.Clone(block.Content)...)
		total += blockBytes
	}
	return result, nil
}

func cloneRequest(request prompt.Request) prompt.Request {
	request.Input = content.Clone(request.Input)
	request.History = cloneMessages(request.History)
	return request
}

func cloneMessages(messages []model.Message) []model.Message {
	result := make([]model.Message, len(messages))
	for i, message := range messages {
		message.Content = content.Clone(message.Content)
		calls := message.ToolCalls
		if calls != nil {
			message.ToolCalls = make([]tool.Call, len(calls))
			for j, call := range calls {
				message.ToolCalls[j] = tool.Call{ID: call.ID, Name: call.Name, Arguments: append(json.RawMessage(nil), call.Arguments...)}
			}
		}
		result[i] = message
	}
	return result
}

func contentBytes(value content.Content) int {
	total := 0
	for _, part := range value {
		if part.Kind == content.KindText {
			total += len(part.Text)
		} else if part.Media.Source.Kind == content.SourceInline {
			total += len(part.Media.Source.Data)
		}
	}
	return total
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

var _ prompt.Renderer = (*renderer)(nil)
