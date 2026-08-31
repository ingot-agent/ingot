package agentdefault

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ingot-agent/ingot-abi"
	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/asset"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/contextwindow"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/pipeline"
	"github.com/ingot-agent/sdk/prompt"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/tool"
)

type memoryStore struct {
	mu      sync.Mutex
	entries map[session.ID][]session.Entry
}

func (s *memoryStore) Create(context.Context, session.Metadata) (session.ID, error) {
	return "created", nil
}
func (s *memoryStore) Append(_ context.Context, id session.ID, entry session.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry.Payload = append([]byte(nil), entry.Payload...)
	s.entries[id] = append(s.entries[id], entry)
	return nil
}
func (s *memoryStore) Load(_ context.Context, id session.ID) ([]session.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries := append([]session.Entry(nil), s.entries[id]...)
	for i := range entries {
		entries[i].Payload = append([]byte(nil), entries[i].Payload...)
	}
	return entries, nil
}

type memoryAssets struct {
	mu    sync.Mutex
	data  map[string][]byte
	opens int
}

func newMemoryAssets() *memoryAssets { return &memoryAssets{data: make(map[string][]byte)} }

func (s *memoryAssets) Put(_ context.Context, request asset.PutRequest) (asset.Reference, asset.Info, error) {
	raw, err := io.ReadAll(io.LimitReader(request.Body, int64(request.Size)+1))
	if err != nil {
		return asset.Reference{}, asset.Info{}, err
	}
	if uint64(len(raw)) != request.Size {
		return asset.Reference{}, asset.Info{}, errors.New("size mismatch")
	}
	id := fmt.Sprintf("test-%x", sha256.Sum256(raw))
	s.mu.Lock()
	s.data[id] = append([]byte(nil), raw...)
	s.mu.Unlock()
	return asset.Reference{ID: id}, asset.Info{Size: request.Size}, nil
}

func (s *memoryAssets) Stat(_ context.Context, reference asset.Reference) (asset.Info, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, ok := s.data[reference.ID]
	if !ok {
		return asset.Info{}, errors.New("asset not found")
	}
	return asset.Info{Size: uint64(len(raw))}, nil
}

func (s *memoryAssets) Open(_ context.Context, reference asset.Reference) (io.ReadCloser, error) {
	s.mu.Lock()
	raw, ok := s.data[reference.ID]
	s.opens++
	s.mu.Unlock()
	if !ok {
		return nil, errors.New("asset not found")
	}
	return io.NopCloser(bytes.NewReader(raw)), nil
}

type imageTools struct{ value []byte }

func (t *imageTools) Definitions() []tool.Definition {
	return []tool.Definition{{Name: "image", Description: "image", InputSchema: json.RawMessage(`{"type":"object"}`)}}
}

func (t *imageTools) Call(context.Context, tool.Call) (tool.Result, error) {
	return tool.Result{Content: content.Content{content.Inline(content.KindImage, "image/png", "tool.png", t.value)}}, nil
}
func (s *memoryStore) List(context.Context, session.Query) ([]session.Summary, error) {
	return nil, nil
}

type sequenceModel struct {
	mu        sync.Mutex
	responses []model.Response
	requests  []model.Request
}

type recordingCompactor struct {
	mu       sync.Mutex
	requests []contextwindow.CompactionRequest
	outputs  [][]model.Message
	err      error
}

func (c *recordingCompactor) Compact(_ context.Context, request contextwindow.CompactionRequest) (contextwindow.CompactionResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	request.Invocation = cloneModelRequest(request.Invocation)
	c.requests = append(c.requests, request)
	if c.err != nil {
		return contextwindow.CompactionResult{}, c.err
	}
	index := len(c.requests) - 1
	if index >= len(c.outputs) {
		return contextwindow.CompactionResult{Messages: cloneMessages(request.Invocation.Messages)}, nil
	}
	return contextwindow.CompactionResult{Messages: cloneMessages(c.outputs[index])}, nil
}

func (m *sequenceModel) Complete(_ context.Context, request model.Request) (model.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, request)
	response := m.responses[0]
	m.responses = m.responses[1:]
	return response, nil
}

type fakeTools struct{ calls []tool.Call }

func (t *fakeTools) Definitions() []tool.Definition {
	return []tool.Definition{{Name: "echo", Description: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)}}
}
func (t *fakeTools) Call(_ context.Context, call tool.Call) (tool.Result, error) {
	t.calls = append(t.calls, cloneCall(call))
	return tool.Result{Content: content.FromText("tool-ok")}, nil
}

type passthroughPrompt struct{}

func (passthroughPrompt) Render(_ context.Context, request prompt.Request) ([]model.Message, error) {
	result := cloneMessages(request.History)
	return append(result, model.Message{Role: model.RoleUser, Content: request.Input}), nil
}

type agentInterceptorFunc func(context.Context, agent.Turn, pipeline.Next[agent.Turn, agent.Result]) (agent.Result, error)

func (f agentInterceptorFunc) Invoke(ctx context.Context, turn agent.Turn, next pipeline.Next[agent.Turn, agent.Result]) (agent.Result, error) {
	return f(ctx, turn, next)
}

func TestAgentRunsToolLoopAndPersistsExactOrder(t *testing.T) {
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	models := &sequenceModel{responses: []model.Response{
		{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("working"), ToolCalls: []tool.Call{{ID: "c1", Name: "echo", Arguments: json.RawMessage(`{"x":1}`)}}}},
		{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("done")}},
	}}
	tools := &fakeTools{}
	exports, _, err := New(context.Background(), Config{}, Dependencies{
		Model: models, Tools: tools, Store: store, Assets: newMemoryAssets(), Prompt: passthroughPrompt{},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if textValue(result.Output) != "done" || len(tools.calls) != 1 || len(models.requests) != 2 {
		t.Fatalf("result=%#v tool calls=%d model calls=%d", result, len(tools.calls), len(models.requests))
	}
	entries, _ := store.Load(context.Background(), "s")
	wantRoles := []model.Role{model.RoleUser, model.RoleAssistant, model.RoleTool, model.RoleAssistant}
	if len(entries) != len(wantRoles) {
		t.Fatalf("entries=%d", len(entries))
	}
	for i, entry := range entries {
		message, decodeErr := decodePersistedMessage(entry.Payload)
		if decodeErr != nil || message.Role != wantRoles[i] {
			t.Fatalf("entry %d message=%#v err=%v", i, message, decodeErr)
		}
		if i == 1 && (textValue(message.Content) != "working" || len(message.ToolCalls) != 1) {
			t.Fatalf("assistant content and tool calls were not preserved: %#v", message)
		}
	}
}

func TestAgentMaterializesOrderedAttachmentsAndRestoresLazily(t *testing.T) {
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	assets := newMemoryAssets()
	models := &sequenceModel{responses: []model.Response{
		{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("first")}},
		{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("second")}},
	}}
	exports, _, err := New(context.Background(), Config{}, Dependencies{
		Model: models, Tools: &fakeTools{}, Store: store, Assets: assets, Prompt: passthroughPrompt{},
	})
	if err != nil {
		t.Fatal(err)
	}
	imageData := []byte("image-bytes")
	fileData := []byte("file-bytes")
	attachments := []content.Attachment{
		{Kind: content.KindImage, Media: content.Inline(content.KindImage, "image/png", "one.png", imageData).Media},
		{Kind: content.KindFile, Media: content.Inline(content.KindFile, "application/octet-stream", "two.bin", fileData).Media},
	}
	if _, err := exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "caption", Attachments: attachments}); err != nil {
		t.Fatal(err)
	}
	imageData[0] = 'X'
	fileData[0] = 'X'
	attachments[0].Media.Name = "mutated"

	models.mu.Lock()
	firstRequest := cloneModelRequest(models.requests[0])
	models.mu.Unlock()
	if len(firstRequest.Messages) != 1 {
		t.Fatalf("first request messages=%#v", firstRequest.Messages)
	}
	user := firstRequest.Messages[0]
	if len(user.Content) != 3 || textValue(user.Content[:1]) != "caption" ||
		user.Content[1].Kind != content.KindImage || user.Content[1].Media.Name != "one.png" ||
		user.Content[2].Kind != content.KindFile || user.Content[2].Media.Name != "two.bin" {
		t.Fatalf("ordered user content=%#v", user.Content)
	}
	for i := 1; i < len(user.Content); i++ {
		if user.Content[i].Media.Source.Kind != content.SourceAsset || user.Content[i].Media.Source.Asset.ID == "" || len(user.Content[i].Media.Source.Data) != 0 {
			t.Fatalf("part %d was not materialized: %#v", i, user.Content[i])
		}
	}
	entries, err := store.Load(context.Background(), "s")
	if err != nil {
		t.Fatal(err)
	}
	persistedUser, err := decodePersistedMessage(entries[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(persistedUser.Content, user.Content) {
		t.Fatalf("prompt content=%#v persisted=%#v", user.Content, persistedUser.Content)
	}
	for _, raw := range [][]byte{[]byte(base64.StdEncoding.EncodeToString([]byte("image-bytes"))), []byte(base64.StdEncoding.EncodeToString([]byte("file-bytes")))} {
		if bytes.Contains(entries[0].Payload, raw) {
			t.Fatalf("session payload contains inline asset bytes: %s", raw)
		}
	}
	if _, err := exports.History.Load(context.Background(), "s"); err != nil {
		t.Fatal(err)
	}
	if _, err := exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "next"}); err != nil {
		t.Fatal(err)
	}
	assets.mu.Lock()
	opens := assets.opens
	assets.mu.Unlock()
	if opens != 0 {
		t.Fatalf("history restore opened %d assets", opens)
	}
}

func TestPersistedMessageRoundTripsOpaqueStringBytes(t *testing.T) {
	invalidMIME := string([]byte{'i', 'm', 'a', 'g', 'e', '/', 0xff})
	invalidURI := "https://example.test/" + string([]byte{0xfe})
	invalidAssetID := "asset-" + string([]byte{0xfd})
	want := model.Message{
		Role:      model.RoleUser,
		ToolCalls: []tool.Call{},
		Content: content.Content{
			content.URI(content.KindImage, invalidMIME, "remote.png", invalidURI),
			content.AssetPart(content.KindImage, "image/png", "stored.png", asset.Reference{ID: invalidAssetID}),
		},
	}
	raw, err := encodePersistedMessage(want)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`\ufffd`)) || bytes.Contains(raw, []byte("\uFFFD")) {
		t.Fatalf("payload replaced opaque bytes: %s", raw)
	}
	if !bytes.Contains(raw, []byte(`"mime_type":"image/png"`)) {
		t.Fatalf("UTF-8 opaque values are not readable: %s", raw)
	}
	got, err := decodePersistedMessage(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip=%#v want=%#v payload=%s", got, want, raw)
	}
}

func TestAgentMaterializesToolImageBeforeFollowupAndPersistence(t *testing.T) {
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	models := &sequenceModel{responses: []model.Response{
		{Message: model.Message{Role: model.RoleAssistant, ToolCalls: []tool.Call{{ID: "c1", Name: "image", Arguments: json.RawMessage(`{}`)}}}},
		{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("done")}},
	}}
	exports, _, err := New(context.Background(), Config{}, Dependencies{
		Model: models, Tools: &imageTools{value: []byte("tool-image")}, Store: store, Assets: newMemoryAssets(), Prompt: passthroughPrompt{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "draw"}); err != nil {
		t.Fatal(err)
	}
	models.mu.Lock()
	followup := cloneModelRequest(models.requests[1])
	models.mu.Unlock()
	toolMessage := followup.Messages[len(followup.Messages)-1]
	if toolMessage.Role != model.RoleTool || len(toolMessage.Content) != 1 || toolMessage.Content[0].Kind != content.KindImage ||
		toolMessage.Content[0].Media.Source.Kind != content.SourceAsset {
		t.Fatalf("tool followup=%#v", toolMessage)
	}
	entries, err := store.Load(context.Background(), "s")
	if err != nil {
		t.Fatal(err)
	}
	persistedTool, err := decodePersistedMessage(entries[2].Payload)
	if err != nil || !reflect.DeepEqual(persistedTool.Content, toolMessage.Content) {
		t.Fatalf("persisted tool=%#v error=%v", persistedTool, err)
	}
}

func TestAgentAllowsAttachmentOnlyTurn(t *testing.T) {
	models := &sequenceModel{responses: []model.Response{{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("seen")}}}}
	exports, _, err := New(context.Background(), Config{}, Dependencies{
		Model: models, Tools: &fakeTools{}, Store: &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}, Assets: newMemoryAssets(), Prompt: passthroughPrompt{},
	})
	if err != nil {
		t.Fatal(err)
	}
	turn := agent.Turn{SessionID: "s", Attachments: []content.Attachment{{
		Kind: content.KindImage, Media: content.Inline(content.KindImage, "image/png", "only.png", []byte("only")).Media,
	}}}
	if _, err := exports.Runtime.Run(context.Background(), turn); err != nil {
		t.Fatal(err)
	}
	models.mu.Lock()
	request := cloneModelRequest(models.requests[0])
	models.mu.Unlock()
	if len(request.Messages) != 1 || len(request.Messages[0].Content) != 1 || request.Messages[0].Content[0].Kind != content.KindImage {
		t.Fatalf("attachment-only request=%#v", request)
	}
}

func TestAgentValidatesCompleteResponseBeforePersisting(t *testing.T) {
	t.Parallel()
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	models := &sequenceModel{responses: []model.Response{{Message: model.Message{Role: model.RoleUser, Content: content.FromText("must not render")}}}}
	exports, _, err := New(context.Background(), Config{}, Dependencies{
		Model: models, Tools: &fakeTools{}, Store: store, Assets: newMemoryAssets(), Prompt: passthroughPrompt{},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "hello"})
	if !errors.Is(err, ErrInvalidModelMessage) {
		t.Fatalf("error=%v", err)
	}
	entries, err := store.Load(context.Background(), "s")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries=%d, want committed user only", len(entries))
	}
	message, err := decodePersistedMessage(entries[0].Payload)
	if err != nil || message.Role != model.RoleUser || textValue(message.Content) != "hello" {
		t.Fatalf("committed message=%#v error=%v", message, err)
	}
}

func TestAgentCompactsEveryModelInvocationWithoutReplacingRawMessages(t *testing.T) {
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	models := &sequenceModel{responses: []model.Response{
		{Message: model.Message{Role: model.RoleAssistant, ToolCalls: []tool.Call{{ID: "c1", Name: "echo", Arguments: json.RawMessage(`{"x":1}`)}}}},
		{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("done")}},
	}}
	compactor := &recordingCompactor{outputs: [][]model.Message{
		{{Role: model.RoleUser, Content: content.FromText("compact-view-1")}},
		{{Role: model.RoleUser, Content: content.FromText("compact-view-2")}},
	}}
	temperature := 0.25
	maxTokens := 321
	exports, _, err := New(context.Background(), Config{
		Provider: "provider", Model: "model", Temperature: &temperature, MaxTokens: &maxTokens,
	}, Dependencies{
		Model: models, Tools: &fakeTools{}, Store: store, Assets: newMemoryAssets(), Prompt: passthroughPrompt{},
		Compactor: ingotabi.Some[contextwindow.Compactor](compactor),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if textValue(result.Output) != "done" {
		t.Fatalf("result=%#v", result)
	}

	compactor.mu.Lock()
	requests := append([]contextwindow.CompactionRequest(nil), compactor.requests...)
	compactor.mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("compactor calls=%d", len(requests))
	}
	first := requests[0]
	if first.SessionID != "s" || first.Invocation.Provider != "provider" || first.Invocation.Model != "model" ||
		first.Invocation.Temperature == nil || *first.Invocation.Temperature != temperature ||
		first.Invocation.MaxTokens == nil || *first.Invocation.MaxTokens != maxTokens || len(first.Invocation.Tools) != 1 {
		t.Fatalf("first compaction request=%#v", first)
	}
	secondMessages := requests[1].Invocation.Messages
	if len(secondMessages) != 3 || textValue(secondMessages[0].Content) != "hello" ||
		secondMessages[1].Role != model.RoleAssistant || len(secondMessages[1].ToolCalls) != 1 ||
		secondMessages[2].Role != model.RoleTool || textValue(secondMessages[2].Content) != "tool-ok" {
		t.Fatalf("second raw invocation messages=%#v", secondMessages)
	}
	if textValue(secondMessages[0].Content) == "compact-view-1" {
		t.Fatal("compacted view replaced the agent's raw in-memory messages")
	}

	models.mu.Lock()
	modelRequests := append([]model.Request(nil), models.requests...)
	models.mu.Unlock()
	if len(modelRequests) != 2 || len(modelRequests[0].Messages) != 1 || textValue(modelRequests[0].Messages[0].Content) != "compact-view-1" ||
		len(modelRequests[1].Messages) != 1 || textValue(modelRequests[1].Messages[0].Content) != "compact-view-2" {
		t.Fatalf("model requests=%#v", modelRequests)
	}
}

func TestAgentCompactorErrorStopsModelAndPreservesCommittedUser(t *testing.T) {
	compactErr := errors.New("compact failed")
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	models := &sequenceModel{}
	compactor := &recordingCompactor{err: compactErr}
	exports, _, err := New(context.Background(), Config{}, Dependencies{
		Model: models, Tools: &fakeTools{}, Store: store, Assets: newMemoryAssets(), Prompt: passthroughPrompt{},
		Compactor: ingotabi.Some[contextwindow.Compactor](compactor),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "hello"})
	if !errors.Is(err, compactErr) {
		t.Fatalf("error=%v", err)
	}
	models.mu.Lock()
	modelCalls := len(models.requests)
	models.mu.Unlock()
	if modelCalls != 0 {
		t.Fatalf("model calls=%d", modelCalls)
	}
	entries, err := store.Load(context.Background(), "s")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries=%d", len(entries))
	}
	message, err := decodePersistedMessage(entries[0].Payload)
	if err != nil || message.Role != model.RoleUser || textValue(message.Content) != "hello" {
		t.Fatalf("committed message=%#v error=%v", message, err)
	}
}

func TestAgentRejectsTypedNilCompactor(t *testing.T) {
	var compactor *recordingCompactor
	_, _, err := New(context.Background(), Config{}, Dependencies{
		Model: &sequenceModel{}, Tools: &fakeTools{}, Store: &memoryStore{entries: map[session.ID][]session.Entry{}}, Assets: newMemoryAssets(),
		Prompt: passthroughPrompt{}, Compactor: ingotabi.Some[contextwindow.Compactor](compactor),
	})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error=%v", err)
	}
}

func TestAgentRejectsNonFiniteTemperature(t *testing.T) {
	t.Parallel()
	for _, temperature := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		temperature := temperature
		_, _, err := New(context.Background(), Config{Temperature: &temperature}, Dependencies{
			Model: &sequenceModel{}, Tools: &fakeTools{}, Store: &memoryStore{entries: map[session.ID][]session.Entry{}}, Assets: newMemoryAssets(), Prompt: passthroughPrompt{},
		})
		if !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("temperature=%v error=%v", temperature, err)
		}
	}
}

func TestAgentRecoversTrailingToolRoundWithoutRetry(t *testing.T) {
	assistant := model.Message{Role: model.RoleAssistant, ToolCalls: []tool.Call{
		{ID: "c1", Name: "echo", Arguments: json.RawMessage(`{}`)},
		{ID: "c2", Name: "echo", Arguments: json.RawMessage(`{}`)},
	}}
	firstResult := model.Message{Role: model.RoleTool, Content: content.FromText("ok"), ToolCallID: "c1"}
	assistantPayload, _ := encodePersistedMessage(assistant)
	resultPayload, _ := encodePersistedMessage(firstResult)
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {
		{Kind: agentMessageKind, Version: agentMessageVersion, Payload: assistantPayload},
		{Kind: agentMessageKind, Version: agentMessageVersion, Payload: resultPayload},
	}}}
	models := &sequenceModel{responses: []model.Response{{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("continued")}}}}
	tools := &fakeTools{}
	exports, _, err := New(context.Background(), Config{}, Dependencies{Model: models, Tools: tools, Store: store, Assets: newMemoryAssets(), Prompt: passthroughPrompt{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "next"}); err != nil {
		t.Fatal(err)
	}
	if len(tools.calls) != 0 {
		t.Fatalf("recovery retried tools: %d", len(tools.calls))
	}
	entries, _ := store.Load(context.Background(), "s")
	if len(entries) != 5 {
		t.Fatalf("entries=%d", len(entries))
	}
	recovered, err := decodePersistedMessage(entries[2].Payload)
	if err != nil || recovered.Role != model.RoleTool || recovered.ToolCallID != "c2" || textValue(recovered.Content) != interruptedContent {
		t.Fatalf("recovered=%#v err=%v", recovered, err)
	}
}

type blockingModel struct {
	entered chan struct{}
	release chan struct{}
	mu      sync.Mutex
	calls   int
}

func (m *blockingModel) Complete(ctx context.Context, _ model.Request) (model.Response, error) {
	m.mu.Lock()
	m.calls++
	call := m.calls
	m.mu.Unlock()
	if call == 1 {
		close(m.entered)
		select {
		case <-m.release:
		case <-ctx.Done():
			return model.Response{}, ctx.Err()
		}
	}
	return model.Response{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("ok")}}, nil
}

func TestAgentSerializesSameSession(t *testing.T) {
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	models := &blockingModel{entered: make(chan struct{}), release: make(chan struct{})}
	exports, _, err := New(context.Background(), Config{}, Dependencies{Model: models, Tools: &fakeTools{}, Store: store, Assets: newMemoryAssets(), Prompt: passthroughPrompt{}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	go func() {
		_, runErr := exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "one"})
		done <- runErr
	}()
	<-models.entered
	go func() {
		_, runErr := exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "two"})
		done <- runErr
	}()
	time.Sleep(50 * time.Millisecond)
	models.mu.Lock()
	callsBeforeRelease := models.calls
	models.mu.Unlock()
	if callsBeforeRelease != 1 {
		t.Fatalf("same-session calls before release=%d", callsBeforeRelease)
	}
	close(models.release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestHistoryReturnsDeepOwnedMessages(t *testing.T) {
	assistant := model.Message{Role: model.RoleAssistant, Content: content.FromText("done"), ToolCalls: []tool.Call{{
		ID: "c1", Name: "echo", Arguments: json.RawMessage(`{"value":1}`),
	}}}
	payload, err := encodePersistedMessage(assistant)
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {{
		Kind: agentMessageKind, Version: agentMessageVersion, Payload: payload,
	}}}}
	exports, _, err := New(context.Background(), Config{}, Dependencies{
		Model: &sequenceModel{}, Tools: &fakeTools{}, Store: store, Assets: newMemoryAssets(), Prompt: passthroughPrompt{},
	})
	if err != nil {
		t.Fatal(err)
	}
	messages, err := exports.History.Load(context.Background(), "s")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || textValue(messages[0].Content) != "done" || len(messages[0].ToolCalls) != 1 {
		t.Fatalf("messages=%#v", messages)
	}
	messages[0].Content = content.FromText("changed")
	messages[0].ToolCalls[0].Arguments[0] = '['
	again, err := exports.History.Load(context.Background(), "s")
	if err != nil {
		t.Fatal(err)
	}
	if textValue(again[0].Content) != "done" || string(again[0].ToolCalls[0].Arguments) != `{"value":1}` {
		t.Fatalf("second load retained caller mutation: %#v", again)
	}
	if _, err := exports.History.Load(context.Background(), ""); !errors.Is(err, ErrInvalidTurn) {
		t.Fatalf("empty session error=%v", err)
	}
}

func TestHistoryWaitsForSameSessionTurn(t *testing.T) {
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	models := &blockingModel{entered: make(chan struct{}), release: make(chan struct{})}
	exports, _, err := New(context.Background(), Config{}, Dependencies{
		Model: models, Tools: &fakeTools{}, Store: store, Assets: newMemoryAssets(), Prompt: passthroughPrompt{},
	})
	if err != nil {
		t.Fatal(err)
	}
	runDone := make(chan error, 1)
	go func() {
		_, runErr := exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "s", Input: "hello"})
		runDone <- runErr
	}()
	<-models.entered
	historyDone := make(chan []model.Message, 1)
	go func() {
		messages, loadErr := exports.History.Load(context.Background(), "s")
		if loadErr != nil {
			t.Errorf("Load() error=%v", loadErr)
		}
		historyDone <- messages
	}()
	select {
	case <-historyDone:
		t.Fatal("History.Load completed during the active same-session turn")
	case <-time.After(20 * time.Millisecond):
	}
	close(models.release)
	if err := <-runDone; err != nil {
		t.Fatal(err)
	}
	messages := <-historyDone
	if len(messages) != 2 || messages[0].Role != model.RoleUser || messages[1].Role != model.RoleAssistant {
		t.Fatalf("messages=%#v", messages)
	}
}

func TestAgentRejectsInterceptorSessionIDRewriteBeforeTerminal(t *testing.T) {
	t.Parallel()
	store := &memoryStore{entries: map[session.ID][]session.Entry{"original": {}, "rewritten": {}}}
	models := &sequenceModel{responses: []model.Response{{Message: model.Message{Role: model.RoleAssistant, Content: content.FromText("unused")}}}}
	rewrite := agentInterceptorFunc(func(ctx context.Context, turn agent.Turn, next pipeline.Next[agent.Turn, agent.Result]) (agent.Result, error) {
		turn.SessionID = "rewritten"
		return next(ctx, turn)
	})
	exports, _, err := New(context.Background(), Config{}, Dependencies{
		Model: models, Tools: &fakeTools{}, Store: store, Assets: newMemoryAssets(), Prompt: passthroughPrompt{}, Interceptors: []agent.Interceptor{rewrite},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = exports.Runtime.Run(context.Background(), agent.Turn{SessionID: "original", Input: "hello"})
	if !errors.Is(err, ErrInvalidTurn) {
		t.Fatalf("error=%v", err)
	}
	models.mu.Lock()
	modelCalls := len(models.requests)
	models.mu.Unlock()
	if modelCalls != 0 {
		t.Fatalf("model calls=%d, want 0", modelCalls)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.entries["original"]) != 0 || len(store.entries["rewritten"]) != 0 {
		t.Fatalf("entries=%#v", store.entries)
	}
}

func textValue(value content.Content) string {
	result, _ := content.TextOnly(value)
	return result
}
