// Package projection maps SDK aggregates onto the browser's JSON vocabulary.
package projection

import (
	"bytes"
	"encoding/json"

	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/tool"
)

// Part is one ordered text or media part.
type Part struct {
	Kind     string  `json:"kind"`
	Text     string  `json:"text,omitempty"`
	MIMEType string  `json:"mimeType,omitempty"`
	Name     string  `json:"name,omitempty"`
	Source   *Source `json:"source,omitempty"`
}

// Source preserves how an output media part is represented without resolving it.
type Source struct {
	Kind    string `json:"kind"`
	Data    []byte `json:"data,omitempty"`
	URI     string `json:"uri,omitempty"`
	AssetID string `json:"assetId,omitempty"`
}

// Kind maps a content modality to its stable name.
func Kind(kind content.Kind) string {
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
		return "unknown"
	}
}

// Content preserves part order and owns nested binary data.
func Content(value content.Content) []Part {
	result := make([]Part, len(value))
	for i, part := range value {
		result[i] = Part{Kind: Kind(part.Kind)}
		if part.Kind == content.KindText {
			result[i].Text = part.Text
			continue
		}
		result[i].MIMEType, result[i].Name = part.Media.MIMEType, part.Media.Name
		source := part.Media.Source
		projected := &Source{}
		switch source.Kind {
		case content.SourceInline:
			projected.Kind, projected.Data = "inline", bytes.Clone(source.Data)
		case content.SourceURI:
			projected.Kind, projected.URI = "uri", source.URI
		case content.SourceAsset:
			projected.Kind, projected.AssetID = "asset", source.Asset.ID
		}
		result[i].Source = projected
	}
	return result
}

// Message is a conversation message with explicit Web content and tool DTOs.
type Message struct {
	Role       string `json:"role"`
	Content    []Part `json:"content"`
	Name       string `json:"name,omitempty"`
	ToolCallID string `json:"toolCallId,omitempty"`
	ToolCalls  []Call `json:"toolCalls,omitempty"`
}

// Call is one canonical tool call.
type Call struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// ToolCall detaches raw tool arguments.
func ToolCall(call tool.Call) Call {
	return Call{ID: call.ID, Name: call.Name, Arguments: bytes.Clone(call.Arguments)}
}

// Messages maps conversation history without exposing Go struct names or enums.
func Messages(messages []model.Message) []Message {
	result := make([]Message, len(messages))
	for i, message := range messages {
		calls := make([]Call, len(message.ToolCalls))
		for j, call := range message.ToolCalls {
			calls[j] = ToolCall(call)
		}
		result[i] = Message{Role: string(message.Role), Content: Content(message.Content), Name: message.Name, ToolCallID: message.ToolCallID, ToolCalls: calls}
	}
	return result
}

// ModelRequest retains the actual request submitted to a model runtime.
func ModelRequest(request model.Request) map[string]any {
	definitions := make([]map[string]any, len(request.Tools))
	for i, definition := range request.Tools {
		definitions[i] = map[string]any{"name": definition.Name, "description": definition.Description, "inputSchema": json.RawMessage(bytes.Clone(definition.InputSchema))}
	}
	return map[string]any{"provider": request.Provider, "model": request.Model, "messages": Messages(request.Messages), "tools": definitions,
		"temperature": request.Temperature, "maxTokens": request.MaxTokens, "stop": append([]string{}, request.Stop...)}
}

// ModelResponse includes provider-reported usage without estimating missing data.
func ModelResponse(response model.Response) map[string]any {
	return map[string]any{"message": Messages([]model.Message{response.Message})[0], "finishReason": response.FinishReason, "provider": response.Provider, "model": response.Model,
		"usage": map[string]any{"inputTokens": response.Usage.InputTokens, "outputTokens": response.Usage.OutputTokens, "totalTokens": response.Usage.TotalTokens, "reported": response.Usage.Reported}}
}

// ModelProgress keeps stream semantic and part lifecycle separate.
func ModelProgress(event model.StreamEvent) map[string]any {
	kind := map[model.StreamEventKind]string{model.StreamPartStart: "part.start", model.StreamPartDelta: "part.delta", model.StreamPartEnd: "part.end"}[event.Kind]
	semantic := "content"
	if event.Semantic == model.StreamSemanticReasoning {
		semantic = "reasoning"
	}
	result := map[string]any{"kind": kind, "semantic": semantic, "partIndex": event.PartIndex}
	if event.Kind == model.StreamPartStart {
		result["partKind"], result["mimeType"], result["name"] = Kind(event.PartKind), event.MIMEType, event.Name
	}
	if event.Kind == model.StreamPartDelta {
		result["textDelta"], result["dataDelta"] = event.TextDelta, bytes.Clone(event.DataDelta)
	}
	return result
}

// Outcome preserves accounting and explicit coverage, including on failure.
func Outcome(outcome agent.Outcome) any {
	if outcome.Status == 0 {
		return nil
	}
	models := make([]map[string]any, len(outcome.Accounting.Models))
	for i, item := range outcome.Accounting.Models {
		models[i] = map[string]any{"provider": item.Provider, "model": item.Model, "completedInvocations": item.CompletedInvocations, "usage": tokenUsage(item.Usage)}
	}
	result := map[string]any{"status": map[agent.OutcomeStatus]string{agent.OutcomeSucceeded: "succeeded", agent.OutcomeFailed: "failed", agent.OutcomeCanceled: "canceled"}[outcome.Status], "durationNs": outcome.Duration.Nanoseconds(),
		"accounting": map[string]any{"rounds": outcome.Accounting.Rounds, "modelInvocations": outcome.Accounting.ModelInvocations, "toolCalls": outcome.Accounting.ToolCalls, "usage": tokenUsage(outcome.Accounting.Usage), "models": models}}
	if failure := outcome.Failure; failure != nil {
		stage := map[agent.FailureStage]string{
			agent.FailureSessionGate: "session_gate", agent.FailureHistoryLoad: "history_load", agent.FailureRecovery: "recovery", agent.FailureUserPersistence: "user_persistence",
			agent.FailurePrompt: "prompt", agent.FailureCompaction: "compaction", agent.FailureModel: "model", agent.FailureRoundControl: "round_control", agent.FailureAssistantPersistence: "assistant_persistence",
			agent.FailureTool: "tool", agent.FailureToolResultPersistence: "tool_result_persistence", agent.FailureTurnControl: "turn_control", agent.FailureStreamConsumer: "stream_consumer",
		}[failure.Stage]
		result["failure"] = map[string]any{"stage": stage, "roundIndex": failure.RoundIndex, "toolCallId": failure.ToolCallID}
	}
	return result
}

func tokenUsage(usage agent.TokenUsage) map[string]any {
	return map[string]any{"inputTokens": usage.InputTokens, "outputTokens": usage.OutputTokens, "totalTokens": usage.TotalTokens,
		"coverage": map[agent.UsageCoverage]string{agent.UsageUnavailable: "unavailable", agent.UsagePartial: "partial", agent.UsageComplete: "complete"}[usage.Coverage]}
}
