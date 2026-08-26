package openaicompat_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	openaicompat "github.com/ingot-agent/model-openai-compatible"
	"github.com/ingot-agent/sdk/httpx"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/tool"
)

type clientFunc func(context.Context, *http.Request) (*http.Response, error)

func (f clientFunc) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	return f(ctx, request)
}

func TestCompleteMapsRequestHeadersAndResponse(t *testing.T) {
	var captured *http.Request
	var body []byte
	client := clientFunc(func(_ context.Context, request *http.Request) (*http.Response, error) {
		captured = request.Clone(context.Background())
		body, _ = io.ReadAll(request.Body)
		return response(http.StatusOK, `{"model":"actual-model","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`), nil
	})
	headers := map[string]string{"X-Tenant": "one"}
	exports, _, err := openaicompat.New(context.Background(), openaicompat.Config{Providers: []openaicompat.ProviderConfig{{
		Name: "primary", BaseURL: "https://example.test/v1/", APIKey: "secret", Organization: "org", Project: "project",
		Models: []string{"requested-model"}, DefaultHeaders: headers,
	}}}, openaicompat.Dependencies{HTTP: httpx.Client(client)})
	if err != nil {
		t.Fatal(err)
	}
	headers["X-Tenant"] = "mutated"
	result, err := exports.Providers[0].Value.Complete(context.Background(), model.Request{Model: "requested-model", Messages: []model.Message{{Role: model.RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Provider != "primary" || result.Model != "actual-model" || result.Message.Content != "hello" {
		t.Fatalf("result=%#v", result)
	}
	if captured.URL.String() != "https://example.test/v1/chat/completions" {
		t.Fatalf("url=%s", captured.URL)
	}
	if captured.Header.Get("Authorization") != "Bearer secret" || captured.Header.Get("OpenAI-Organization") != "org" || captured.Header.Get("OpenAI-Project") != "project" || captured.Header.Get("X-Tenant") != "one" {
		t.Fatalf("headers=%v", captured.Header)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil || payload["stream"] != false || payload["model"] != "requested-model" {
		t.Fatalf("payload=%s err=%v", body, err)
	}
}

func TestCompleteMapsMessagesToolsAndOptionalFields(t *testing.T) {
	var requestBody string
	client := clientFunc(func(_ context.Context, request *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(request.Body)
		requestBody = string(raw)
		return response(http.StatusOK, `{"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`), nil
	})
	provider := newProvider(t, openaicompat.ProviderConfig{Name: "p", BaseURL: "https://example.test"}, client)
	temperature := 0.25
	maxTokens := 128
	_, err := provider.Complete(context.Background(), model.Request{
		Model: "m",
		Messages: []model.Message{
			{Role: model.RoleSystem, Content: "system"},
			{Role: model.RoleUser, Content: "question", Name: "user-name"},
			{Role: model.RoleAssistant, ToolCalls: []tool.Call{{ID: "call-1", Name: "read_file", Arguments: json.RawMessage(`{"path":"README.md"}`)}}},
			{Role: model.RoleTool, Content: "contents", ToolCallID: "call-1"},
		},
		Tools:       []tool.Definition{{Name: "read_file", Description: "Read one file.", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		Temperature: &temperature,
		MaxTokens:   &maxTokens,
		Stop:        []string{"END"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		`"role":"system"`, `"name":"user-name"`, `"tool_calls":[{"id":"call-1","type":"function"`,
		`"arguments":"{\"path\":\"README.md\"}"`, `"tool_call_id":"call-1"`, `"parameters":{"type":"object"}`,
		`"temperature":0.25`, `"max_tokens":128`, `"stop":["END"]`, `"stream":false`,
	} {
		if !strings.Contains(requestBody, fragment) {
			t.Fatalf("request body %s does not contain %s", requestBody, fragment)
		}
	}
}

func TestCompleteDecodesStringToolArgumentsIntoSDKRawJSON(t *testing.T) {
	client := clientFunc(func(_ context.Context, _ *http.Request) (*http.Response, error) {
		return response(http.StatusOK, `{"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"call-1","type":"function","function":{"name":"fs_list","arguments":"{\"path\":\".\"}"}}]},"finish_reason":"tool_calls"}]}`), nil
	})
	provider := newProvider(t, openaicompat.ProviderConfig{Name: "p", BaseURL: "https://example.test"}, client)
	result, err := provider.Complete(context.Background(), model.Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Message.ToolCalls) != 1 || result.Message.ToolCalls[0].Name != "fs_list" || string(result.Message.ToolCalls[0].Arguments) != `{"path":"."}` {
		t.Fatalf("result=%#v", result)
	}
}

func TestStreamingDeliversOrderedTextAndRequiresDone(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"model":"m","choices":[{"index":0,"delta":{"role":"assistant","content":"he"},"finish_reason":null}]}`,
		"",
		`data: {"model":"m","choices":[{"index":0,"delta":{"content":"llo"},"finish_reason":"stop"}]}`,
		"",
		`data: {"model":"m","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
		"",
		"data: [DONE]",
		"",
	}, "\r\n")
	client := clientFunc(func(_ context.Context, _ *http.Request) (*http.Response, error) {
		return response(http.StatusOK, sse), nil
	})
	exports, _, err := openaicompat.New(context.Background(), openaicompat.Config{Providers: []openaicompat.ProviderConfig{{Name: "p", BaseURL: "https://example.test"}}}, openaicompat.Dependencies{HTTP: client})
	if err != nil {
		t.Fatal(err)
	}
	streaming := exports.Providers[0].Value.(model.StreamingProvider)
	var chunks []string
	result, err := streaming.Stream(context.Background(), model.Request{Model: "m"}, func(chunk model.StreamChunk) error {
		chunks = append(chunks, chunk.TextDelta)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(chunks, "") != "hello" || result.Message.Content != "hello" || result.FinishReason != "stop" || result.Usage.TotalTokens != 2 {
		t.Fatalf("chunks=%v result=%#v", chunks, result)
	}

	client = clientFunc(func(_ context.Context, _ *http.Request) (*http.Response, error) {
		return response(http.StatusOK, "data: {\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":\"stop\"}]}\n\n"), nil
	})
	exports, _, _ = openaicompat.New(context.Background(), openaicompat.Config{Providers: []openaicompat.ProviderConfig{{Name: "p", BaseURL: "https://example.test"}}}, openaicompat.Dependencies{HTTP: client})
	_, err = exports.Providers[0].Value.(model.StreamingProvider).Stream(context.Background(), model.Request{Model: "m"}, func(model.StreamChunk) error { return nil })
	if !errors.Is(err, openaicompat.ErrProtocol) {
		t.Fatalf("missing DONE error=%v", err)
	}
}

func TestConfigRejectsOwnedHeadersAndResponseLimit(t *testing.T) {
	client := clientFunc(func(_ context.Context, _ *http.Request) (*http.Response, error) {
		return response(http.StatusOK, strings.Repeat("x", 20)), nil
	})
	_, _, err := openaicompat.New(context.Background(), openaicompat.Config{Providers: []openaicompat.ProviderConfig{{Name: "p", BaseURL: "https://example.test", DefaultHeaders: map[string]string{"authorization": "bad"}}}}, openaicompat.Dependencies{HTTP: client})
	if !errors.Is(err, openaicompat.ErrInvalidConfig) {
		t.Fatalf("owned header error=%v", err)
	}
	exports, _, err := openaicompat.New(context.Background(), openaicompat.Config{Providers: []openaicompat.ProviderConfig{{Name: "p", BaseURL: "https://example.test", MaxResponseBytes: 5}}}, openaicompat.Dependencies{HTTP: client})
	if err != nil {
		t.Fatal(err)
	}
	_, err = exports.Providers[0].Value.Complete(context.Background(), model.Request{Model: "m"})
	if !errors.Is(err, openaicompat.ErrResponseLimit) {
		t.Fatalf("limit error=%v", err)
	}
}

func TestConfigRejectsUnsafeProviderIdentityURLAndHeaders(t *testing.T) {
	client := clientFunc(func(context.Context, *http.Request) (*http.Response, error) {
		return nil, errors.New("must not be called")
	})
	tests := []struct {
		name string
		cfg  openaicompat.ProviderConfig
	}{
		{name: "provider name", cfg: openaicompat.ProviderConfig{Name: "Bad Name", BaseURL: "https://example.test"}},
		{name: "provider name length", cfg: openaicompat.ProviderConfig{Name: "a" + strings.Repeat("b", 64), BaseURL: "https://example.test"}},
		{name: "URL userinfo", cfg: openaicompat.ProviderConfig{Name: "p", BaseURL: "https://user:secret@example.test/v1"}},
		{name: "API key newline", cfg: openaicompat.ProviderConfig{Name: "p", BaseURL: "https://example.test", APIKey: "secret\nX-Evil: yes"}},
		{name: "header name", cfg: openaicompat.ProviderConfig{Name: "p", BaseURL: "https://example.test", DefaultHeaders: map[string]string{"Bad Header": "x"}}},
		{name: "header value", cfg: openaicompat.ProviderConfig{Name: "p", BaseURL: "https://example.test", DefaultHeaders: map[string]string{"X-Test": "x\r\ny"}}},
		{name: "duplicate header", cfg: openaicompat.ProviderConfig{Name: "p", BaseURL: "https://example.test", DefaultHeaders: map[string]string{"X-Test": "x", "x-test": "y"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := openaicompat.New(context.Background(), openaicompat.Config{Providers: []openaicompat.ProviderConfig{test.cfg}}, openaicompat.Dependencies{HTTP: client})
			if !errors.Is(err, openaicompat.ErrInvalidConfig) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestNewPreservesProviderOrderAndRejectsDuplicateNames(t *testing.T) {
	client := clientFunc(func(context.Context, *http.Request) (*http.Response, error) {
		return nil, errors.New("must not be called")
	})
	exports, _, err := openaicompat.New(context.Background(), openaicompat.Config{Providers: []openaicompat.ProviderConfig{
		{Name: "primary", BaseURL: "https://one.example"},
		{Name: "fallback", BaseURL: "https://two.example"},
	}}, openaicompat.Dependencies{HTTP: client})
	if err != nil {
		t.Fatal(err)
	}
	if len(exports.Providers) != 2 || exports.Providers[0].Name != "primary" || exports.Providers[1].Name != "fallback" {
		t.Fatalf("providers=%#v", exports.Providers)
	}
	_, _, err = openaicompat.New(context.Background(), openaicompat.Config{Providers: []openaicompat.ProviderConfig{
		{Name: "same", BaseURL: "https://one.example"},
		{Name: "same", BaseURL: "https://two.example"},
	}}, openaicompat.Dependencies{HTTP: client})
	if !errors.Is(err, openaicompat.ErrInvalidConfig) {
		t.Fatalf("duplicate error=%v", err)
	}
}

func TestCompletePreservesHTTPStatusWhenErrorBodyIsTruncated(t *testing.T) {
	client := clientFunc(func(context.Context, *http.Request) (*http.Response, error) {
		result := response(http.StatusTooManyRequests, "abcdef")
		result.Header.Set("X-Request-Id", "request-1")
		return result, nil
	})
	provider := newProvider(t, openaicompat.ProviderConfig{Name: "p", BaseURL: "https://example.test", MaxErrorBodyBytes: 4}, client)
	_, err := provider.Complete(context.Background(), model.Request{Model: "m"})
	var httpErr *openaicompat.ProviderHTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error=%v", err)
	}
	if httpErr.StatusCode != http.StatusTooManyRequests || httpErr.RequestID != "request-1" || httpErr.Body != "abcd" || !httpErr.Truncated {
		t.Fatalf("HTTP error=%#v", httpErr)
	}
	if errors.Is(err, openaicompat.ErrResponseLimit) {
		t.Fatalf("truncated HTTP error must retain HTTP classification: %v", err)
	}
}

func TestProviderErrorsRedactConfiguredAPIKey(t *testing.T) {
	const secret = "super-secret-key"
	provider := newProvider(t, openaicompat.ProviderConfig{Name: "p", BaseURL: "https://example.test", APIKey: secret}, clientFunc(func(context.Context, *http.Request) (*http.Response, error) {
		return response(http.StatusUnauthorized, "rejected "+secret), nil
	}))
	_, err := provider.Complete(context.Background(), model.Request{Model: "m"})
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("HTTP error leaked API key: %v", err)
	}

	transportErr := errors.New("transport included " + secret)
	provider = newProvider(t, openaicompat.ProviderConfig{Name: "p", BaseURL: "https://example.test", APIKey: secret}, clientFunc(func(context.Context, *http.Request) (*http.Response, error) {
		return nil, transportErr
	}))
	_, err = provider.Complete(context.Background(), model.Request{Model: "m"})
	if !errors.Is(err, transportErr) || strings.Contains(err.Error(), secret) {
		t.Fatalf("transport error=%v", err)
	}
}

func TestCompleteRejectsMissingUsageFieldsAndInvalidUTF8(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "missing usage fields", body: `{"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{}}`},
		{name: "invalid UTF-8", body: `{"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"` + string([]byte{0xff}) + `"},"finish_reason":"stop"}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := clientFunc(func(context.Context, *http.Request) (*http.Response, error) {
				return response(http.StatusOK, test.body), nil
			})
			provider := newProvider(t, openaicompat.ProviderConfig{Name: "p", BaseURL: "https://example.test"}, client)
			_, err := provider.Complete(context.Background(), model.Request{Model: "m"})
			if !errors.Is(err, openaicompat.ErrProtocol) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestCompleteRejectsNonFiniteTemperature(t *testing.T) {
	provider := newProvider(t, openaicompat.ProviderConfig{Name: "p", BaseURL: "https://example.test"}, clientFunc(func(context.Context, *http.Request) (*http.Response, error) {
		return nil, errors.New("must not be called")
	}))
	temperature := math.NaN()
	_, err := provider.Complete(context.Background(), model.Request{Model: "m", Temperature: &temperature})
	if !errors.Is(err, openaicompat.ErrInvalidRequest) {
		t.Fatalf("error=%v", err)
	}
}

func TestStreamingAccumulatesToolCallsAndRejectsUnsupportedType(t *testing.T) {
	valid := strings.Join([]string{
		`data: {"model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{\"x\":"}}]},"finish_reason":null}]}`,
		"",
		`data: {"model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]},"finish_reason":"tool_calls"}]}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	provider := newProvider(t, openaicompat.ProviderConfig{Name: "p", BaseURL: "https://example.test"}, clientFunc(func(context.Context, *http.Request) (*http.Response, error) {
		return response(http.StatusOK, valid), nil
	})).(model.StreamingProvider)
	result, err := provider.Stream(context.Background(), model.Request{Model: "m"}, func(model.StreamChunk) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Message.ToolCalls) != 1 || result.Message.ToolCalls[0].ID != "call-1" || result.Message.ToolCalls[0].Name != "lookup" || string(result.Message.ToolCalls[0].Arguments) != `{"x":1}` {
		t.Fatalf("result=%#v", result)
	}

	invalid := "data: {\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"type\":\"custom\"}]},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	provider = newProvider(t, openaicompat.ProviderConfig{Name: "p", BaseURL: "https://example.test"}, clientFunc(func(context.Context, *http.Request) (*http.Response, error) {
		return response(http.StatusOK, invalid), nil
	})).(model.StreamingProvider)
	_, err = provider.Stream(context.Background(), model.Request{Model: "m"}, func(model.StreamChunk) error { return nil })
	if !errors.Is(err, openaicompat.ErrProtocol) {
		t.Fatalf("unsupported type error=%v", err)
	}
}

func TestStreamingRequiresDoneAsOnlyDataLine(t *testing.T) {
	body := "data: [DONE]\ndata: \n\n"
	provider := newProvider(t, openaicompat.ProviderConfig{Name: "p", BaseURL: "https://example.test"}, clientFunc(func(context.Context, *http.Request) (*http.Response, error) {
		return response(http.StatusOK, body), nil
	})).(model.StreamingProvider)
	_, err := provider.Stream(context.Background(), model.Request{Model: "m"}, func(model.StreamChunk) error { return nil })
	if !errors.Is(err, openaicompat.ErrProtocol) {
		t.Fatalf("error=%v", err)
	}
}

func TestStreamingAppliesTotalResponseLimit(t *testing.T) {
	provider := newProvider(t, openaicompat.ProviderConfig{Name: "p", BaseURL: "https://example.test", MaxResponseBytes: 8}, clientFunc(func(context.Context, *http.Request) (*http.Response, error) {
		return response(http.StatusOK, strings.Repeat("x", 9)), nil
	})).(model.StreamingProvider)
	_, err := provider.Stream(context.Background(), model.Request{Model: "m"}, func(model.StreamChunk) error { return nil })
	if !errors.Is(err, openaicompat.ErrResponseLimit) {
		t.Fatalf("error=%v", err)
	}
}

func TestStreamingCancellationClosesBlockedBody(t *testing.T) {
	body := newBlockingBody()
	provider := newProvider(t, openaicompat.ProviderConfig{Name: "p", BaseURL: "https://example.test"}, clientFunc(func(context.Context, *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body}, nil
	})).(model.StreamingProvider)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := provider.Stream(ctx, model.Request{Model: "m"}, func(model.StreamChunk) error { return nil })
		result <- err
	}()
	select {
	case <-body.started:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not start reading")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not stop after cancellation")
	}
}

func TestStreamingHandlerErrorIsPreservedAndBodyClosed(t *testing.T) {
	body := &trackingBody{Reader: strings.NewReader("data: {\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"x\"},\"finish_reason\":null}]}\n\n")}
	provider := newProvider(t, openaicompat.ProviderConfig{Name: "p", BaseURL: "https://example.test"}, clientFunc(func(context.Context, *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body}, nil
	})).(model.StreamingProvider)
	handlerErr := errors.New("handler stopped")
	_, err := provider.Stream(context.Background(), model.Request{Model: "m"}, func(model.StreamChunk) error { return handlerErr })
	if !errors.Is(err, handlerErr) {
		t.Fatalf("error=%v", err)
	}
	if !body.isClosed() {
		t.Fatal("response body was not closed")
	}
}

func TestProviderSupportsConcurrentCompleteAndStream(t *testing.T) {
	client := clientFunc(func(_ context.Context, request *http.Request) (*http.Response, error) {
		if request.Header.Get("Accept") == "text/event-stream" {
			return response(http.StatusOK, "data: {\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"), nil
		}
		return response(http.StatusOK, `{"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`), nil
	})
	provider := newProvider(t, openaicompat.ProviderConfig{Name: "p", BaseURL: "https://example.test"}, client)
	streaming := provider.(model.StreamingProvider)
	errorsFound := make(chan error, 20)
	var wait sync.WaitGroup
	for i := 0; i < 10; i++ {
		wait.Add(2)
		go func() {
			defer wait.Done()
			_, err := provider.Complete(context.Background(), model.Request{Model: "m"})
			errorsFound <- err
		}()
		go func() {
			defer wait.Done()
			_, err := streaming.Stream(context.Background(), model.Request{Model: "m"}, func(model.StreamChunk) error { return nil })
			errorsFound <- err
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func newProvider(t *testing.T, cfg openaicompat.ProviderConfig, client httpx.Client) model.Provider {
	t.Helper()
	exports, _, err := openaicompat.New(context.Background(), openaicompat.Config{Providers: []openaicompat.ProviderConfig{cfg}}, openaicompat.Dependencies{HTTP: client})
	if err != nil {
		t.Fatal(err)
	}
	return exports.Providers[0].Value
}

type blockingBody struct {
	started   chan struct{}
	closed    chan struct{}
	startOnce sync.Once
	closeOnce sync.Once
}

func newBlockingBody() *blockingBody {
	return &blockingBody{started: make(chan struct{}), closed: make(chan struct{})}
}

func (b *blockingBody) Read([]byte) (int, error) {
	b.startOnce.Do(func() { close(b.started) })
	<-b.closed
	return 0, io.ErrClosedPipe
}

func (b *blockingBody) Close() error {
	b.closeOnce.Do(func() { close(b.closed) })
	return nil
}

type trackingBody struct {
	io.Reader
	mu     sync.Mutex
	closed bool
}

func (b *trackingBody) Close() error {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	return nil
}

func (b *trackingBody) isClosed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed
}

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
