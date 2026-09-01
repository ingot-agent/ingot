package openaicompat

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/tool"
)

type streamResponse struct {
	Model   string         `json:"model"`
	Choices []streamChoice `json:"choices"`
	Usage   *chatUsage     `json:"usage"`
}

type streamChoice struct {
	Index        int         `json:"index"`
	Delta        streamDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

type streamDelta struct {
	Role             string            `json:"role"`
	Content          *string           `json:"content"`
	ReasoningContent *string           `json:"reasoning_content"`
	Reasoning        *string           `json:"reasoning"`
	ToolCalls        []streamToolDelta `json:"tool_calls"`
}

type streamToolDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type streamAccumulator struct {
	provider      string
	model         string
	role          model.Role
	content       strings.Builder
	contentOpen   bool
	reasoningOpen bool
	toolCalls     []tool.Call
	finishReason  string
	usage         model.Usage
}

func (p *provider) Stream(ctx context.Context, request model.Request, handler model.StreamHandler) (model.Response, error) {
	if err := p.validateRequest(ctx, request); err != nil {
		return model.Response{}, err
	}
	if handler == nil {
		return model.Response{}, fmt.Errorf("nil stream handler: %w", ErrInvalidRequest)
	}
	body, err := p.encodeChatRequest(ctx, request, true)
	if err != nil {
		return model.Response{}, err
	}
	response, err := p.do(ctx, body, "text/event-stream")
	if err != nil {
		return model.Response{}, err
	}
	stopBodyWatch := closeBodyOnCancel(ctx, response.Body)
	defer stopBodyWatch()
	if err := p.checkStatus(ctx, response); err != nil {
		return model.Response{}, err
	}
	return p.decodeStream(ctx, response, handler)
}

func (p *provider) decodeStream(ctx context.Context, response *http.Response, handler model.StreamHandler) (model.Response, error) {
	consumer := handler
	handler = func(event model.StreamEvent) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return consumer(event)
	}
	limited := &io.LimitedReader{R: response.Body, N: int64(p.maxResponseBytes) + 1}
	reader := bufio.NewReader(limited)
	dataLines := make([]string, 0, 1)
	accumulator := streamAccumulator{provider: p.name}
	done := false

	dispatch := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		eventData := dataLines
		payload := strings.Join(eventData, "\n")
		dataLines = dataLines[:0]
		for _, dataLine := range eventData {
			if strings.TrimSpace(dataLine) == "[DONE]" {
				if len(eventData) != 1 {
					return protocolError("[DONE] must be the only data line in its event")
				}
				done = true
				return nil
			}
		}
		if !utf8.ValidString(payload) {
			return protocolError("SSE data is not valid UTF-8")
		}
		var chunk streamResponse
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return protocolError("decode SSE data: %v", err)
		}
		return accumulator.add(chunk, handler)
	}

	for !done {
		if err := ctx.Err(); err != nil {
			return model.Response{}, err
		}
		line, err := reader.ReadString('\n')
		if ctxErr := ctx.Err(); ctxErr != nil {
			return model.Response{}, ctxErr
		}
		if limited.N <= 0 {
			return model.Response{}, &ResponseLimitError{Limit: p.maxResponseBytes, Kind: "stream response"}
		}
		if err != nil && err != io.EOF {
			return model.Response{}, fmt.Errorf("read model stream: %w", err)
		}
		line = strings.TrimSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\r")
		if !utf8.ValidString(line) {
			return model.Response{}, protocolError("SSE line is not valid UTF-8")
		}
		if line == "" {
			if dispatchErr := dispatch(); dispatchErr != nil {
				return model.Response{}, dispatchErr
			}
		} else if !strings.HasPrefix(line, ":") {
			field, value, found := strings.Cut(line, ":")
			if !found {
				field, value = line, ""
			}
			if strings.HasPrefix(value, " ") {
				value = value[1:]
			}
			if field == "data" {
				dataLines = append(dataLines, value)
			}
		}
		if err == io.EOF {
			if dispatchErr := dispatch(); dispatchErr != nil {
				return model.Response{}, dispatchErr
			}
			break
		}
	}
	if !done {
		return model.Response{}, protocolError("stream ended before [DONE]")
	}
	if err := accumulator.finish(handler); err != nil {
		return model.Response{}, err
	}
	if err := ctx.Err(); err != nil {
		return model.Response{}, err
	}
	return accumulator.response()
}

func (a *streamAccumulator) add(chunk streamResponse, handler model.StreamHandler) error {
	if chunk.Model != "" {
		if a.model != "" && a.model != chunk.Model {
			return protocolError("stream model changed from %q to %q", a.model, chunk.Model)
		}
		a.model = chunk.Model
	}
	if len(chunk.Choices) == 0 {
		if chunk.Usage == nil {
			return protocolError("stream chunk has neither choice nor usage")
		}
		usage, err := decodeUsage(chunk.Usage)
		if err != nil {
			return err
		}
		a.usage = usage
		return nil
	}
	if len(chunk.Choices) != 1 || chunk.Choices[0].Index != 0 {
		return protocolError("stream chunk requires one choice with index 0")
	}
	choice := chunk.Choices[0]
	if choice.Delta.Role != "" {
		role := model.Role(choice.Delta.Role)
		if role != model.RoleAssistant || (a.role != "" && a.role != role) {
			return protocolError("invalid or conflicting stream role %q", role)
		}
		a.role = role
	}
	// Some compatible endpoints expose an alias. Prefer reasoning_content
	// when both are present to avoid duplicating the same reasoning text.
	reasoning := choice.Delta.ReasoningContent
	if reasoning == nil {
		reasoning = choice.Delta.Reasoning
	}
	if reasoning != nil && *reasoning != "" {
		if !utf8.ValidString(*reasoning) {
			return protocolError("stream reasoning delta is invalid UTF-8")
		}
		if !a.reasoningOpen {
			if err := handler(model.StreamEvent{Kind: model.StreamPartStart, PartKind: content.KindText, Semantic: model.StreamSemanticReasoning}); err != nil {
				return err
			}
			a.reasoningOpen = true
		}
		if err := handler(model.StreamEvent{Kind: model.StreamPartDelta, Semantic: model.StreamSemanticReasoning, TextDelta: *reasoning}); err != nil {
			return err
		}
	}
	if choice.Delta.Content != nil {
		if !utf8.ValidString(*choice.Delta.Content) {
			return protocolError("stream text delta is invalid UTF-8")
		}
		if !a.contentOpen {
			if err := handler(model.StreamEvent{Kind: model.StreamPartStart, PartIndex: 0, PartKind: content.KindText}); err != nil {
				return err
			}
			a.contentOpen = true
		}
		a.content.WriteString(*choice.Delta.Content)
		if *choice.Delta.Content != "" {
			if err := handler(model.StreamEvent{Kind: model.StreamPartDelta, PartIndex: 0, TextDelta: *choice.Delta.Content}); err != nil {
				return err
			}
		}
	}
	for _, delta := range choice.Delta.ToolCalls {
		if delta.Type != "" && delta.Type != "function" {
			return protocolError("unsupported tool call type %q at index %d", delta.Type, delta.Index)
		}
		if delta.Index < 0 || delta.Index > len(a.toolCalls) {
			return protocolError("non-contiguous tool call index %d", delta.Index)
		}
		if delta.Index == len(a.toolCalls) {
			a.toolCalls = append(a.toolCalls, tool.Call{})
		}
		call := &a.toolCalls[delta.Index]
		if delta.ID != "" {
			if call.ID != "" && call.ID != delta.ID {
				return protocolError("conflicting tool call id at index %d", delta.Index)
			}
			call.ID = delta.ID
		}
		if delta.Function.Name != "" {
			if call.Name != "" && call.Name != delta.Function.Name {
				return protocolError("conflicting tool call name at index %d", delta.Index)
			}
			call.Name = delta.Function.Name
		}
		call.Arguments = append(call.Arguments, delta.Function.Arguments...)
	}
	if choice.FinishReason != nil {
		if *choice.FinishReason == "" || (a.finishReason != "" && a.finishReason != *choice.FinishReason) {
			return protocolError("invalid or conflicting finish reason")
		}
		a.finishReason = *choice.FinishReason
	}
	if chunk.Usage != nil {
		usage, err := decodeUsage(chunk.Usage)
		if err != nil {
			return err
		}
		a.usage = usage
	}
	return nil
}

func (a *streamAccumulator) finish(handler model.StreamHandler) error {
	if a.reasoningOpen {
		if err := handler(model.StreamEvent{Kind: model.StreamPartEnd, Semantic: model.StreamSemanticReasoning}); err != nil {
			return err
		}
	}
	if !a.contentOpen {
		return nil
	}
	return handler(model.StreamEvent{Kind: model.StreamPartEnd, PartIndex: 0})
}

func (a *streamAccumulator) response() (model.Response, error) {
	if a.model == "" || a.role != model.RoleAssistant || a.finishReason == "" {
		return model.Response{}, protocolError("stream is missing model, assistant role, or finish reason")
	}
	calls := make([]tool.Call, len(a.toolCalls))
	for i, call := range a.toolCalls {
		if call.ID == "" || call.Name == "" || !json.Valid(call.Arguments) {
			return model.Response{}, protocolError("invalid accumulated tool call at index %d", i)
		}
		calls[i] = tool.Call{ID: call.ID, Name: call.Name, Arguments: append(json.RawMessage(nil), call.Arguments...)}
	}
	var messageContent content.Content
	if a.contentOpen {
		messageContent = content.FromText(a.content.String())
	}
	return model.Response{
		Message:      model.Message{Role: model.RoleAssistant, Content: messageContent, ToolCalls: calls},
		FinishReason: a.finishReason, Usage: a.usage, Provider: a.provider, Model: a.model,
	}, nil
}
