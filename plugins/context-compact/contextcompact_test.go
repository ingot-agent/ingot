package contextcompact

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ingot-agent/sdk/asset"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/contextwindow"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/session"
)

type memoryStore struct {
	mu        sync.Mutex
	entries   map[session.ID][]session.Entry
	loadErr   error
	appendErr error
}

func (s *memoryStore) Create(context.Context, session.Metadata) (session.ID, error) {
	return "", errors.New("not implemented")
}

func (s *memoryStore) Append(_ context.Context, id session.ID, entry session.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.appendErr != nil {
		return s.appendErr
	}
	entry.Payload = cloneRaw(entry.Payload)
	s.entries[id] = append(s.entries[id], entry)
	return nil
}

func (s *memoryStore) Load(_ context.Context, id session.ID) ([]session.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	entries := s.entries[id]
	result := make([]session.Entry, len(entries))
	for i, entry := range entries {
		entry.Payload = cloneRaw(entry.Payload)
		result[i] = entry
	}
	return result, nil
}

func (s *memoryStore) List(context.Context, session.Query) ([]session.Summary, error) {
	return nil, nil
}

type fakeModel struct {
	mu        sync.Mutex
	responses []model.Response
	err       error
	requests  []model.Request
}

type blockingSummaryModel struct {
	mu       sync.Mutex
	requests []model.Request
	entered  chan struct{}
	release  chan struct{}
}

func (m *blockingSummaryModel) Complete(ctx context.Context, request model.Request) (model.Response, error) {
	m.mu.Lock()
	m.requests = append(m.requests, cloneRequest(request))
	call := len(m.requests)
	m.mu.Unlock()
	if call == 1 {
		close(m.entered)
		select {
		case <-m.release:
		case <-ctx.Done():
			return model.Response{}, ctx.Err()
		}
	}
	return summaryResponse(`{"summary":"serialized","operations":[]}`), nil
}

func (m *fakeModel) Complete(_ context.Context, request model.Request) (model.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, cloneRequest(request))
	if m.err != nil {
		return model.Response{}, m.err
	}
	if len(m.responses) == 0 {
		return model.Response{}, errors.New("unexpected summary call")
	}
	response := m.responses[0]
	m.responses = m.responses[1:]
	return response, nil
}

func TestCompactNoOpReturnsOwnedMessages(t *testing.T) {
	t.Parallel()
	request := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: textContent("hello")}}}
	raw, err := canonicalRequestBytes(request)
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	models := &fakeModel{}
	exports, _, err := New(context.Background(), Config{TriggerRequestBytes: len(raw) + 2, TargetRequestBytes: len(raw) + 1}, Dependencies{Model: models, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	result, err := exports.Compactor.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: request})
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed || !reflect.DeepEqual(result.Messages, request.Messages) || len(models.requests) != 0 {
		t.Fatalf("result=%#v summary calls=%d", result, len(models.requests))
	}
	result.Messages[0].Content = textContent("changed")
	if messageText(request.Messages[0]) != "hello" {
		t.Fatal("result aliases invocation messages")
	}
}

func TestInspectRequestRejectsOpaqueInvalidUTF8(t *testing.T) {
	t.Parallel()
	invalid := string([]byte{0xff})
	tests := []struct {
		name    string
		content content.Content
	}{
		{name: "MIME type", content: content.Content{content.Inline(content.KindImage, invalid, "image.png", []byte("image"))}},
		{name: "URI", content: content.Content{content.URI(content.KindImage, "image/png", "image.png", invalid)}},
		{name: "asset ID", content: content.Content{content.AssetPart(content.KindImage, "image/png", "image.png", asset.Reference{ID: invalid})}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := inspectRequest(model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: test.content}}}, 0, 0)
			if !errors.Is(err, ErrInvalidHistory) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestCompactPreservesAnchorRecentAndPersistsDelta(t *testing.T) {
	t.Parallel()
	request := longRequest()
	raw, err := canonicalRequestBytes(request)
	if err != nil {
		t.Fatal(err)
	}
	response := model.Response{
		Message:  model.Message{Role: model.RoleAssistant, Content: textContent(`{"summary":"middle work was completed","operations":[{"op":"set","path":"/project/root","value":"D:\\ingot-local\\ingot"}]}`)},
		Provider: "main-provider", Model: "main-model",
	}
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	models := &fakeModel{responses: []model.Response{response}}
	cfg := Config{
		TriggerRequestBytes: len(raw) - 1, TargetRequestBytes: len(raw) - 500,
		AnchorTurns: 1, RecentTurns: 1, SummaryChunkBytes: 1,
	}
	exports, _, err := New(context.Background(), cfg, Dependencies{Model: models, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	result, err := exports.Compactor.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: request})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || len(models.requests) != 1 {
		t.Fatalf("changed=%v summary calls=%d", result.Changed, len(models.requests))
	}
	if len(result.Messages) != 7 || result.Messages[0].Role != model.RoleSystem || messageText(result.Messages[1]) != "anchor user" || messageText(result.Messages[2]) != "anchor assistant" {
		t.Fatalf("preserved prefix=%#v", result.Messages)
	}
	if !strings.Contains(messageText(result.Messages[3]), "middle work was completed") || !strings.Contains(messageText(result.Messages[4]), `"/project/root"`) {
		t.Fatalf("summary/delta=%#v", result.Messages[3:5])
	}
	if messageText(result.Messages[5]) != "recent user" || messageText(result.Messages[6]) != "recent assistant" {
		t.Fatalf("recent suffix=%#v", result.Messages[5:])
	}
	if models.requests[0].Tools == nil || len(models.requests[0].Tools) != 0 || models.requests[0].Temperature == nil || *models.requests[0].Temperature != 0 {
		t.Fatalf("summary request=%#v", models.requests[0])
	}
	store.mu.Lock()
	entries := append([]session.Entry(nil), store.entries["s"]...)
	store.mu.Unlock()
	if len(entries) != 1 || entries[0].Kind != checkpointEntryKind || entries[0].Version != checkpointEntryVersion {
		t.Fatalf("entries=%#v", entries)
	}
	checkpoint, err := decodeCheckpoint(entries[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.CoveredMessages != 4 || checkpoint.Revision != 1 || len(checkpoint.Operations) != 1 {
		t.Fatalf("checkpoint=%#v", checkpoint)
	}

	restartedModel := &fakeModel{err: errors.New("must reuse checkpoint")}
	restarted, _, err := New(context.Background(), cfg, Dependencies{Model: restartedModel, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	reused, err := restarted.Compactor.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: request})
	if err != nil {
		t.Fatal(err)
	}
	if !reused.Changed || len(restartedModel.requests) != 0 || !reflect.DeepEqual(reused.Messages, result.Messages) {
		t.Fatalf("reused=%#v calls=%d", reused, len(restartedModel.requests))
	}
}

func TestIncrementalSegmentsKeepFrozenPrefixAndUpdateState(t *testing.T) {
	t.Parallel()
	firstRequest := longRequest()
	raw, _ := canonicalRequestBytes(firstRequest)
	responses := []model.Response{
		summaryResponse(`{"summary":"first frozen segment","operations":[{"op":"set","path":"/project/root","value":"D:\\old"}]}`),
		summaryResponse(`{"summary":"second frozen segment","operations":[{"op":"set","path":"/project/root","value":"D:\\new"}]}`),
	}
	models := &fakeModel{responses: responses}
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	cfg := Config{TriggerRequestBytes: len(raw) - 1, TargetRequestBytes: len(raw) - 500, AnchorTurns: 1, RecentTurns: 1, SummaryChunkBytes: 1}
	exports, _, err := New(context.Background(), cfg, Dependencies{Model: models, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	first, err := exports.Compactor.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: firstRequest})
	if err != nil {
		t.Fatal(err)
	}

	secondRequest := model.Request{Provider: "main-provider", Model: "main-model", Messages: []model.Message{
		{Role: model.RoleSystem, Content: textContent("system")},
		{Role: model.RoleUser, Content: textContent("anchor user")},
		{Role: model.RoleAssistant, Content: textContent("anchor assistant")},
		{Role: model.RoleUser, Content: textContent("middle user " + strings.Repeat("x", 1200))},
		{Role: model.RoleAssistant, Content: textContent("middle assistant " + strings.Repeat("y", 1200))},
		{Role: model.RoleUser, Content: textContent("second middle " + strings.Repeat("m", 1200))},
		{Role: model.RoleAssistant, Content: textContent("second result " + strings.Repeat("n", 1200))},
		{Role: model.RoleUser, Content: textContent("recent user")},
		{Role: model.RoleAssistant, Content: textContent("recent assistant")},
	}}
	second, err := exports.Compactor.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: secondRequest})
	if err != nil {
		t.Fatal(err)
	}
	if len(models.requests) != 2 || len(first.Messages) < 5 || len(second.Messages) < 7 {
		t.Fatalf("calls=%d first=%#v second=%#v", len(models.requests), first.Messages, second.Messages)
	}
	if !reflect.DeepEqual(first.Messages[:5], second.Messages[:5]) {
		t.Fatalf("frozen prefix changed\nfirst=%#v\nsecond=%#v", first.Messages[:5], second.Messages[:5])
	}
	if !strings.Contains(messageText(second.Messages[5]), "second frozen segment") || !strings.Contains(messageText(second.Messages[6]), `"D:\\new"`) {
		t.Fatalf("incremental messages=%#v", second.Messages[5:7])
	}
	store.mu.Lock()
	entries := append([]session.Entry(nil), store.entries["s"]...)
	store.mu.Unlock()
	if len(entries) != 2 {
		t.Fatalf("checkpoint count=%d", len(entries))
	}
	latest, err := decodeCheckpoint(entries[1].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if latest.ParentSequence != 1 || latest.BaseRevision != 1 || latest.Revision != 2 || latest.Operations[0].Path != "/project/root" {
		t.Fatalf("latest=%#v", latest)
	}
}

func TestRollupBoundsFrozenSummaryChunksWithoutChangingState(t *testing.T) {
	t.Parallel()
	request := manyMiddleTurnsRequest(3)
	raw, _ := canonicalRequestBytes(request)
	models := &fakeModel{responses: []model.Response{
		summaryResponse(`{"summary":"segment one ` + strings.Repeat("a", 100) + `","operations":[{"op":"set","path":"/fact","value":1}]}`),
		summaryResponse(`{"summary":"segment two ` + strings.Repeat("b", 100) + `","operations":[]}`),
		summaryResponse(`{"summary":"rolled up one and two","operations":[]}`),
		summaryResponse(`{"summary":"segment three","operations":[]}`),
	}}
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	cfg := Config{
		TriggerRequestBytes: len(raw) - 1, TargetRequestBytes: 2500,
		AnchorTurns: 1, RecentTurns: 1, SummaryChunkBytes: 1,
		MaxSummaryChunks: 2, MaxSummaryPasses: 4,
	}
	exports, _, err := New(context.Background(), cfg, Dependencies{Model: models, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	result, err := exports.Compactor.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: request})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || len(models.requests) != 4 {
		t.Fatalf("changed=%v summary calls=%d", result.Changed, len(models.requests))
	}
	store.mu.Lock()
	entries := append([]session.Entry(nil), store.entries["s"]...)
	store.mu.Unlock()
	if len(entries) != 4 {
		t.Fatalf("checkpoint count=%d", len(entries))
	}
	rollup, err := decodeCheckpoint(entries[2].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if rollup.Mode != checkpointModeRollup || len(rollup.StateSnapshot) != 1 || string(rollup.StateSnapshot[0].Value) != "1" {
		t.Fatalf("rollup=%#v", rollup)
	}
	if strings.Contains(messagesText(result.Messages), "segment one") || strings.Contains(messagesText(result.Messages), "segment two") || !strings.Contains(messagesText(result.Messages), "rolled up one and two") {
		t.Fatalf("materialized result=%#v", result.Messages)
	}
}

func TestCompactRejectsInvalidHistoryAndOwnedCheckpointVersion(t *testing.T) {
	t.Parallel()
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	models := &fakeModel{}
	exports, _, err := New(context.Background(), Config{TriggerRequestBytes: 100, TargetRequestBytes: 50}, Dependencies{Model: models, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	_, err = exports.Compactor.Compact(context.Background(), contextwindow.CompactionRequest{
		SessionID: "s",
		Invocation: model.Request{Messages: []model.Message{
			{Role: model.RoleUser, Content: textContent("u")},
			{Role: model.RoleTool, Content: textContent("orphan"), ToolCallID: "c"},
		}},
	})
	if !errors.Is(err, ErrInvalidHistory) {
		t.Fatalf("invalid history error=%v", err)
	}

	store.entries["s"] = []session.Entry{{Kind: checkpointEntryKind, Version: 2, Payload: json.RawMessage(`{}`)}}
	_, err = exports.Compactor.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: textContent("u")}}}})
	if !errors.Is(err, ErrUnsupportedCheckpointVersion) {
		t.Fatalf("version error=%v", err)
	}
}

func TestCompactPreservesModelAndStoreErrors(t *testing.T) {
	t.Parallel()
	request := longRequest()
	raw, _ := canonicalRequestBytes(request)
	modelErr := errors.New("model failed")
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	exports, _, err := New(context.Background(), Config{TriggerRequestBytes: len(raw) - 1, TargetRequestBytes: len(raw) - 500, AnchorTurns: 1, RecentTurns: 1, SummaryChunkBytes: 1}, Dependencies{Model: &fakeModel{err: modelErr}, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	_, err = exports.Compactor.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: request})
	if !errors.Is(err, modelErr) {
		t.Fatalf("model error=%v", err)
	}

	loadErr := errors.New("load failed")
	store.loadErr = loadErr
	_, err = exports.Compactor.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: request})
	if !errors.Is(err, loadErr) {
		t.Fatalf("load error=%v", err)
	}

	appendErr := errors.New("append failed")
	store.loadErr = nil
	store.appendErr = appendErr
	models := &fakeModel{responses: []model.Response{summaryResponse(`{"summary":"valid","operations":[]}`)}}
	exports, _, err = New(context.Background(), Config{TriggerRequestBytes: len(raw) - 1, TargetRequestBytes: len(raw) - 500, AnchorTurns: 1, RecentTurns: 1, SummaryChunkBytes: 1}, Dependencies{Model: models, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	_, err = exports.Compactor.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: request})
	if !errors.Is(err, appendErr) {
		t.Fatalf("append error=%v", err)
	}
}

func TestCompactDoesNotPersistSummaryWithoutSizeBenefit(t *testing.T) {
	t.Parallel()
	request := longRequest()
	raw, _ := canonicalRequestBytes(request)
	models := &fakeModel{responses: []model.Response{summaryResponse(`{"summary":"` + strings.Repeat("z", 4000) + `","operations":[]}`)}}
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	exports, _, err := New(context.Background(), Config{
		TriggerRequestBytes: len(raw) - 1, TargetRequestBytes: len(raw) - 500,
		AnchorTurns: 1, RecentTurns: 1, SummaryChunkBytes: 1,
	}, Dependencies{Model: models, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	_, err = exports.Compactor.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: request})
	if !errors.Is(err, ErrContextUncompactable) {
		t.Fatalf("error=%v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.entries["s"]) != 0 {
		t.Fatalf("persisted checkpoints=%d, want 0", len(store.entries["s"]))
	}
}

func TestCompactPreservesRecentMediaAndDoesNotSummarizeIt(t *testing.T) {
	request := longRequest()
	recent := content.Content{
		content.Text("recent user"),
		content.AssetPart(content.KindImage, "image/png", "recent.png", asset.Reference{ID: "asset-recent"}),
	}
	request.Messages[len(request.Messages)-2].Content = content.Clone(recent)
	raw, err := canonicalRequestBytes(request)
	if err != nil {
		t.Fatal(err)
	}
	models := &fakeModel{responses: []model.Response{summaryResponse(`{"summary":"middle summarized","operations":[]}`)}}
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	exports, _, err := New(context.Background(), Config{
		TriggerRequestBytes: len(raw) - 1, TargetRequestBytes: len(raw) - 500,
		AnchorTurns: 1, RecentTurns: 1, SummaryChunkBytes: 1,
	}, Dependencies{Model: models, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	result, err := exports.Compactor.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: request})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || len(result.Messages) < 2 || !reflect.DeepEqual(result.Messages[len(result.Messages)-2].Content, recent) {
		t.Fatalf("media was not preserved: %#v", result.Messages)
	}
	if len(models.requests) != 1 || strings.Contains(messagesText(models.requests[0].Messages), "recent.png") {
		t.Fatalf("summary request included recent media: %#v", models.requests)
	}
}

func TestCompactReturnsUncompactableWhenMiddleTurnContainsMedia(t *testing.T) {
	request := model.Request{Provider: "p", Model: "m", Messages: []model.Message{
		{Role: model.RoleSystem, Content: textContent("system")},
		{Role: model.RoleUser, Content: textContent("anchor user")},
		{Role: model.RoleAssistant, Content: textContent("anchor assistant")},
		{Role: model.RoleUser, Content: content.Content{
			content.Text(strings.Repeat("middle", 300)),
			content.AssetPart(content.KindImage, "image/png", "middle.png", asset.Reference{ID: "asset-middle"}),
		}},
		{Role: model.RoleAssistant, Content: textContent(strings.Repeat("answer", 300))},
		{Role: model.RoleUser, Content: textContent("recent user")},
		{Role: model.RoleAssistant, Content: textContent("recent assistant")},
	}}
	raw, err := canonicalRequestBytes(request)
	if err != nil {
		t.Fatal(err)
	}
	models := &fakeModel{}
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	exports, _, err := New(context.Background(), Config{
		TriggerRequestBytes: len(raw) - 1, TargetRequestBytes: len(raw) - 100,
		AnchorTurns: 1, RecentTurns: 1, SummaryChunkBytes: 1,
	}, Dependencies{Model: models, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	_, err = exports.Compactor.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: request})
	if !errors.Is(err, ErrContextUncompactable) {
		t.Fatalf("error=%v", err)
	}
	if len(models.requests) != 0 || len(store.entries["s"]) != 0 {
		t.Fatalf("media was summarized or checkpointed: requests=%d entries=%d", len(models.requests), len(store.entries["s"]))
	}
}

func TestCanonicalDigestIncludesMediaRepresentation(t *testing.T) {
	base := []model.Message{{Role: model.RoleUser, Content: content.Content{
		content.Inline(content.KindImage, "image/png", "x.png", []byte("one")),
	}}}
	first, err := messageDigest(base)
	if err != nil {
		t.Fatal(err)
	}
	changedData := cloneMessages(base)
	changedData[0].Content[0].Media.Source.Data = []byte("two")
	second, err := messageDigest(changedData)
	if err != nil {
		t.Fatal(err)
	}
	changedSource := cloneMessages(base)
	changedSource[0].Content[0] = content.URI(content.KindImage, "image/png", "x.png", "https://example.test/x.png")
	third, err := messageDigest(changedSource)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || first == third || second == third {
		t.Fatalf("media digests were not distinct: %q %q %q", first, second, third)
	}
}

func TestConfigAndContextValidation(t *testing.T) {
	t.Parallel()
	store := &memoryStore{entries: map[session.ID][]session.Entry{}}
	models := &fakeModel{}
	for _, cfg := range []Config{
		{},
		{TriggerRequestBytes: 10, TargetRequestBytes: 10},
		{TriggerRequestBytes: 10, TargetRequestBytes: 5, RecentTurns: -1},
	} {
		if _, _, err := New(context.Background(), cfg, Dependencies{Model: models, Store: store}); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("config=%#v error=%v", cfg, err)
		}
	}
	exports, _, err := New(context.Background(), Config{TriggerRequestBytes: 10, TargetRequestBytes: 5}, Dependencies{Model: models, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = exports.Compactor.Compact(canceled, contextwindow.CompactionRequest{SessionID: "s"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("context error=%v", err)
	}
}

func TestCompactSerializesSameSessionAndSecondCallReusesCheckpoint(t *testing.T) {
	t.Parallel()
	request := longRequest()
	raw, _ := canonicalRequestBytes(request)
	models := &blockingSummaryModel{entered: make(chan struct{}), release: make(chan struct{})}
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	exports, _, err := New(context.Background(), Config{
		TriggerRequestBytes: len(raw) - 1, TargetRequestBytes: len(raw) - 500,
		AnchorTurns: 1, RecentTurns: 1, SummaryChunkBytes: 1,
	}, Dependencies{Model: models, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		result contextwindow.CompactionResult
		err    error
	}
	results := make(chan outcome, 2)
	call := func() {
		result, callErr := exports.Compactor.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: request})
		results <- outcome{result: result, err: callErr}
	}
	go call()
	select {
	case <-models.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first summary call did not start")
	}
	go call()
	select {
	case result := <-results:
		t.Fatalf("same-session call completed before first released: %#v", result)
	case <-time.After(100 * time.Millisecond):
	}
	close(models.release)
	for i := 0; i < 2; i++ {
		select {
		case result := <-results:
			if result.err != nil || !result.result.Changed {
				t.Fatalf("result=%#v err=%v", result.result, result.err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("compaction call did not finish")
		}
	}
	models.mu.Lock()
	calls := len(models.requests)
	models.mu.Unlock()
	if calls != 1 {
		t.Fatalf("summary calls=%d, want 1", calls)
	}
}

func TestCheckpointReuseRequiresMatchingResolvedModelIdentity(t *testing.T) {
	t.Parallel()
	request := longRequest()
	raw, _ := canonicalRequestBytes(request)
	models := &fakeModel{responses: []model.Response{
		{Message: model.Message{Role: model.RoleAssistant, Content: textContent(`{"summary":"stale identity","operations":[]}`)}, Provider: "other-provider", Model: "other-model"},
		{Message: model.Message{Role: model.RoleAssistant, Content: textContent(`{"summary":"matching identity","operations":[]}`)}, Provider: request.Provider, Model: request.Model},
	}}
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	cfg := Config{
		TriggerRequestBytes: len(raw) - 1, TargetRequestBytes: len(raw) - 500,
		AnchorTurns: 1, RecentTurns: 1, SummaryChunkBytes: 1,
	}
	exports, _, err := New(context.Background(), cfg, Dependencies{Model: models, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exports.Compactor.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: request}); err != nil {
		t.Fatal(err)
	}
	second, err := exports.Compactor.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: request})
	if err != nil {
		t.Fatal(err)
	}
	if len(models.requests) != 2 || !strings.Contains(messagesText(second.Messages), "matching identity") {
		t.Fatalf("summary calls=%d messages=%#v", len(models.requests), second.Messages)
	}

	restarted, _, err := New(context.Background(), cfg, Dependencies{Model: &fakeModel{err: errors.New("must reuse matching checkpoint")}, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	reused, err := restarted.Compactor.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: request})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(messagesText(reused.Messages), "matching identity") {
		t.Fatalf("reused messages=%#v", reused.Messages)
	}
}

func TestCheckpointWithRuntimeDefaultSelectionIsNotReused(t *testing.T) {
	t.Parallel()
	request := longRequest()
	request.Provider = ""
	request.Model = ""
	raw, _ := canonicalRequestBytes(request)
	models := &fakeModel{responses: []model.Response{
		{Message: model.Message{Role: model.RoleAssistant, Content: textContent(`{"summary":"default one","operations":[]}`)}, Provider: "provider-one", Model: "model-one"},
		{Message: model.Message{Role: model.RoleAssistant, Content: textContent(`{"summary":"default two","operations":[]}`)}, Provider: "provider-two", Model: "model-two"},
	}}
	store := &memoryStore{entries: map[session.ID][]session.Entry{"s": {}}}
	cfg := Config{
		TriggerRequestBytes: len(raw) - 1, TargetRequestBytes: len(raw) - 500,
		AnchorTurns: 1, RecentTurns: 1, SummaryChunkBytes: 1,
	}
	exports, _, err := New(context.Background(), cfg, Dependencies{Model: models, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exports.Compactor.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: request}); err != nil {
		t.Fatal(err)
	}
	second, err := exports.Compactor.Compact(context.Background(), contextwindow.CompactionRequest{SessionID: "s", Invocation: request})
	if err != nil {
		t.Fatal(err)
	}
	if len(models.requests) != 2 || !strings.Contains(messagesText(second.Messages), "default two") {
		t.Fatalf("summary calls=%d messages=%#v", len(models.requests), second.Messages)
	}
}

func longRequest() model.Request {
	return model.Request{
		Provider: "main-provider", Model: "main-model",
		Messages: []model.Message{
			{Role: model.RoleSystem, Content: textContent("system")},
			{Role: model.RoleUser, Content: textContent("anchor user")},
			{Role: model.RoleAssistant, Content: textContent("anchor assistant")},
			{Role: model.RoleUser, Content: textContent("middle user " + strings.Repeat("x", 1200))},
			{Role: model.RoleAssistant, Content: textContent("middle assistant " + strings.Repeat("y", 1200))},
			{Role: model.RoleUser, Content: textContent("recent user")},
			{Role: model.RoleAssistant, Content: textContent("recent assistant")},
		},
	}
}

func manyMiddleTurnsRequest(middle int) model.Request {
	messages := []model.Message{
		{Role: model.RoleSystem, Content: textContent("system")},
		{Role: model.RoleUser, Content: textContent("anchor user")},
		{Role: model.RoleAssistant, Content: textContent("anchor assistant")},
	}
	for i := 0; i < middle; i++ {
		messages = append(messages,
			model.Message{Role: model.RoleUser, Content: textContent("middle user " + strings.Repeat(string(rune('a'+i)), 1200))},
			model.Message{Role: model.RoleAssistant, Content: textContent("middle assistant " + strings.Repeat(string(rune('k'+i)), 1200))},
		)
	}
	messages = append(messages,
		model.Message{Role: model.RoleUser, Content: textContent("recent user")},
		model.Message{Role: model.RoleAssistant, Content: textContent("recent assistant")},
	)
	return model.Request{Provider: "main-provider", Model: "main-model", Messages: messages}
}

func summaryResponse(responseContent string) model.Response {
	return model.Response{Message: model.Message{Role: model.RoleAssistant, Content: textContent(responseContent)}, Provider: "main-provider", Model: "main-model"}
}

func messagesText(messages []model.Message) string {
	var builder strings.Builder
	for _, message := range messages {
		builder.WriteString(messageText(message))
		builder.WriteByte('\n')
	}
	return builder.String()
}

func textContent(value string) content.Content { return content.FromText(value) }

func messageText(message model.Message) string {
	value, _ := content.TextOnly(message.Content)
	return value
}
