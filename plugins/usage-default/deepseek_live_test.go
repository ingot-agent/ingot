package usagedefault

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/tool"
)

func TestDeepSeekV4LiveCalibration(t *testing.T) {
	key := strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY"))
	if key == "" {
		t.Skip("DEEPSEEK_API_KEY is not set")
	}
	request := model.Request{Messages: []model.Message{
		{Role: model.RoleSystem, Content: "You are a helpful assistant."},
		{Role: model.RoleUser, Content: "Hello"},
	}}
	profile, err := newDeepSeekV4Profile()
	if err != nil {
		t.Fatal(err)
	}
	localCount, err := profile.CountInput(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	remoteCount, err := requestDeepSeekPromptTokens(key, request)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("local_input_tokens=%d remote_prompt_tokens=%d", localCount, remoteCount)
	if localCount != remoteCount {
		t.Fatalf("DeepSeek V4 token calibration mismatch: local=%d remote=%d", localCount, remoteCount)
	}
}

func TestDeepSeekV4LiveCalibrationVectors(t *testing.T) {
	key := strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY"))
	if key == "" {
		t.Skip("DEEPSEEK_API_KEY is not set")
	}
	vectors := []struct {
		name    string
		request model.Request
	}{
		{
			name: "user-only",
			request: model.Request{Messages: []model.Message{
				{Role: model.RoleUser, Content: "Count this user-only calibration request."},
			}},
		},
		{
			name: "multilingual",
			request: model.Request{Messages: []model.Message{
				{Role: model.RoleSystem, Content: "你是一个严谨的助手。"},
				{Role: model.RoleUser, Content: "请计算 token：你好，世界！こんにちは、世界！こんにちは。"},
			}},
		},
		{
			name: "multi-turn",
			request: model.Request{Messages: []model.Message{
				{Role: model.RoleSystem, Content: "Answer concisely."},
				{Role: model.RoleUser, Content: "What is 2 + 2?"},
				{Role: model.RoleAssistant, Content: "It is 4."},
				{Role: model.RoleUser, Content: "Now explain why in one sentence."},
			}},
		},
		{
			name: "long-text",
			request: model.Request{Messages: []model.Message{
				{Role: model.RoleSystem, Content: "Summarize the supplied text."},
				{Role: model.RoleUser, Content: strings.Repeat("Token calibration keeps the request deterministic. ", 80)},
			}},
		},
		{
			name: "tool-schema",
			request: model.Request{
				Messages: []model.Message{
					{Role: model.RoleSystem, Content: "You are a helpful assistant."},
					{Role: model.RoleUser, Content: "What is the weather in Beijing?"},
				},
				Tools: []tool.Definition{{
					Name:        "get_weather",
					Description: "Get current weather for a city.",
					InputSchema: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`),
				}},
			},
		},
		{
			name: "tool-call-and-result",
			request: model.Request{
				Messages: []model.Message{
					{Role: model.RoleSystem, Content: "You are helpful."},
					{Role: model.RoleUser, Content: "Weather?"},
					{Role: model.RoleAssistant, ToolCalls: []tool.Call{{
						ID: "call_a", Name: "weather", Arguments: []byte(`{"city":"北京","days":2}`),
					}}},
					{Role: model.RoleTool, ToolCallID: "call_a", Content: "晴，25°C"},
					{Role: model.RoleUser, Content: "Thanks"},
				},
				Tools: []tool.Definition{{
					Name:        "weather",
					Description: "Get weather",
					InputSchema: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"},"days":{"type":"integer"}},"required":["city"]}`),
				}},
			},
		},
	}
	profile, err := newDeepSeekV4Profile()
	if err != nil {
		t.Fatal(err)
	}
	for _, vector := range vectors {
		vector := vector
		t.Run(vector.name, func(t *testing.T) {
			localCount, err := profile.CountInput(context.Background(), vector.request)
			if err != nil {
				t.Fatal(err)
			}
			remoteCount, err := requestDeepSeekPromptTokens(key, vector.request)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("local_input_tokens=%d remote_prompt_tokens=%d", localCount, remoteCount)
			if localCount != remoteCount {
				t.Fatalf("DeepSeek V4 token calibration mismatch: local=%d remote=%d", localCount, remoteCount)
			}
		})
	}
}

func requestDeepSeekPromptTokens(key string, request model.Request) (int64, error) {
	type wireFunctionCall struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	type wireToolCall struct {
		ID       string           `json:"id"`
		Type     string           `json:"type"`
		Function wireFunctionCall `json:"function"`
	}
	type wireMessage struct {
		Role       model.Role     `json:"role"`
		Content    string         `json:"content"`
		ToolCallID string         `json:"tool_call_id,omitempty"`
		ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	}
	type wireFunction struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters"`
	}
	type wireTool struct {
		Type     string       `json:"type"`
		Function wireFunction `json:"function"`
	}
	messages := make([]wireMessage, len(request.Messages))
	for i, message := range request.Messages {
		wire := wireMessage{Role: message.Role, Content: message.Content, ToolCallID: message.ToolCallID}
		if len(message.ToolCalls) != 0 {
			wire.ToolCalls = make([]wireToolCall, len(message.ToolCalls))
			for callIndex, call := range message.ToolCalls {
				wire.ToolCalls[callIndex] = wireToolCall{ID: call.ID, Type: "function", Function: wireFunctionCall{
					Name: call.Name, Arguments: string(call.Arguments),
				}}
			}
		}
		messages[i] = wire
	}
	tools := make([]wireTool, len(request.Tools))
	for i, definition := range request.Tools {
		tools[i] = wireTool{Type: "function", Function: wireFunction{
			Name: definition.Name, Description: definition.Description, Parameters: definition.InputSchema,
		}}
	}
	payload, err := json.Marshal(struct {
		Model     string        `json:"model"`
		Messages  []wireMessage `json:"messages"`
		Tools     []wireTool    `json:"tools,omitempty"`
		MaxTokens int           `json:"max_tokens"`
	}{Model: "deepseek-v4-flash", Messages: messages, Tools: tools, MaxTokens: 1})
	if err != nil {
		return 0, fmt.Errorf("encode calibration request: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.deepseek.com/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return 0, fmt.Errorf("construct calibration request: %w", err)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+key)
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(httpRequest)
	if err != nil {
		return 0, fmt.Errorf("call DeepSeek calibration endpoint: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return 0, fmt.Errorf("DeepSeek calibration endpoint returned HTTP %d", response.StatusCode)
	}
	var decoded struct {
		Usage struct {
			PromptTokens *int64 `json:"prompt_tokens"`
		} `json:"usage"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4<<20))
	if err := decoder.Decode(&decoded); err != nil {
		return 0, fmt.Errorf("decode DeepSeek calibration response: %w", err)
	}
	if decoded.Usage.PromptTokens == nil || *decoded.Usage.PromptTokens < 0 {
		return 0, fmt.Errorf("DeepSeek calibration response omitted a valid prompt_tokens value")
	}
	return *decoded.Usage.PromptTokens, nil
}
