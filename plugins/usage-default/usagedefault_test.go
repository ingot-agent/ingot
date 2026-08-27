package usagedefault

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/tool"
	"github.com/ingot-agent/sdk/usage"
)

type resolverFunc func(context.Context, model.Request) (model.Request, error)

func (f resolverFunc) ResolveRequest(ctx context.Context, request model.Request) (model.Request, error) {
	return f(ctx, request)
}

type testProfile struct {
	source   string
	accuracy usage.Accuracy
	count    int64
	err      error
	calls    atomic.Int32
	entered  chan struct{}
	release  chan struct{}
}

func (p *testProfile) CountInput(ctx context.Context, _ model.Request) (int64, error) {
	p.calls.Add(1)
	if p.entered != nil {
		select {
		case p.entered <- struct{}{}:
		default:
		}
	}
	if p.release != nil {
		select {
		case <-p.release:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	return p.count, p.err
}

func (p *testProfile) Accuracy() usage.Accuracy { return p.accuracy }
func (p *testProfile) Source() string           { return p.source }

func passthroughResolver(_ context.Context, request model.Request) (model.Request, error) {
	return cloneRequest(request), nil
}

func TestCountResolvesCompleteRequestAndPreservesOwnership(t *testing.T) {
	t.Parallel()
	arguments := json.RawMessage(`{"path":"README.md"}`)
	schema := json.RawMessage(`{"type":"object"}`)
	request := model.Request{
		Messages: []model.Message{
			{Role: model.RoleSystem, Content: "你是助手"},
			{Role: model.RoleAssistant, ToolCalls: []tool.Call{{ID: "call-1", Name: "read", Arguments: arguments}}},
			{Role: model.RoleTool, Content: "contents", ToolCallID: "call-1"},
		},
		Tools: []tool.Definition{{Name: "read", Description: "Read a file", InputSchema: schema}},
	}
	resolver := resolverFunc(func(_ context.Context, received model.Request) (model.Request, error) {
		received.Provider = "deepseek"
		received.Model = "deepseek-chat"
		received.Messages[1].ToolCalls[0].Arguments[0] = 'X'
		received.Tools[0].InputSchema[0] = 'X'
		return requestWithDefaults(request, "deepseek", "deepseek-chat"), nil
	})
	exports, cleanup, err := New(context.Background(), Config{Routes: []Route{{
		Provider: "deepseek", ModelPattern: `deepseek-.*`, Profile: unicodeEstimateSource,
	}}}, Dependencies{Resolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup(context.Background())
	result, err := exports.Counter.CountInput(context.Background(), usage.CountRequest{Invocation: request})
	if err != nil {
		t.Fatal(err)
	}
	if result.InputTokens <= 0 || result.Accuracy != usage.AccuracyEstimate || result.Source != unicodeEstimateSource || result.Provider != "deepseek" || result.Model != "deepseek-chat" {
		t.Fatalf("result=%#v", result)
	}
	if string(arguments) != `{"path":"README.md"}` || string(schema) != `{"type":"object"}` {
		t.Fatalf("caller request mutated: arguments=%s schema=%s", arguments, schema)
	}
}

func TestUnicodeEstimateProfileFixedVector(t *testing.T) {
	t.Parallel()
	profile := unicodeEstimateProfile{}
	count, err := profile.CountInput(context.Background(), model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Content: "hello世界"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// reply priming 3 + message framing 4 + role 1 + "hello" 2 + CJK 2
	if count != 12 {
		t.Fatalf("count=%d, want 12", count)
	}
	if profile.Accuracy() != usage.AccuracyEstimate || profile.Source() != unicodeEstimateSource {
		t.Fatalf("accuracy=%q source=%q", profile.Accuracy(), profile.Source())
	}
}

func TestRoutesUseProviderExactModelFullMatchAndFirstMatch(t *testing.T) {
	t.Parallel()
	first := &testProfile{source: "first", accuracy: usage.AccuracyEstimate}
	second := &testProfile{source: "second", accuracy: usage.AccuracyEstimate}
	routes := []compiledRoute{
		{provider: "p", pattern: mustRegexp(t, `model-.*`), profile: first},
		{provider: "p", pattern: mustRegexp(t, `.*`), profile: second},
	}
	selected, index, ok := selectProfile(routes, "p", "model-one")
	if !ok || index != 0 || selected.Source() != "first" {
		t.Fatalf("selected=%v index=%d ok=%v", selected, index, ok)
	}
	if _, _, ok = selectProfile(routes, "other", "model-one"); ok {
		t.Fatal("provider match was not exact")
	}
	partial := []compiledRoute{{provider: "p", pattern: mustRegexp(t, `model`), profile: first}}
	if _, _, ok = selectProfile(partial, "p", "model-one"); ok {
		t.Fatal("model regexp matched only a substring")
	}
}

func TestNewRejectsInvalidConfigAndDependencies(t *testing.T) {
	t.Parallel()
	validResolver := resolverFunc(passthroughResolver)
	var typedNil resolverFunc
	tests := []struct {
		name string
		cfg  Config
		deps Dependencies
	}{
		{name: "nil resolver", cfg: validConfig()},
		{name: "typed nil resolver", cfg: validConfig(), deps: Dependencies{Resolver: typedNil}},
		{name: "no routes", deps: Dependencies{Resolver: validResolver}},
		{name: "empty route", cfg: Config{Routes: []Route{{}}}, deps: Dependencies{Resolver: validResolver}},
		{name: "invalid regexp", cfg: Config{Routes: []Route{{Provider: "p", ModelPattern: "(", Profile: unicodeEstimateSource}}}, deps: Dependencies{Resolver: validResolver}},
		{name: "unknown profile", cfg: Config{Routes: []Route{{Provider: "p", ModelPattern: ".*", Profile: "missing"}}}, deps: Dependencies{Resolver: validResolver}},
		{name: "negative cache", cfg: Config{Routes: validConfig().Routes, CacheEntries: -1}, deps: Dependencies{Resolver: validResolver}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := New(context.Background(), test.cfg, test.deps)
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestUnsupportedModelAndResolverErrorsRemainClassified(t *testing.T) {
	t.Parallel()
	resolverErr := errors.New("resolver failed")
	exports, cleanup, err := New(context.Background(), validConfig(), Dependencies{Resolver: resolverFunc(func(context.Context, model.Request) (model.Request, error) {
		return model.Request{}, resolverErr
	})})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup(context.Background())
	if _, err = exports.Counter.CountInput(context.Background(), usage.CountRequest{}); !errors.Is(err, resolverErr) {
		t.Fatalf("resolver error=%v", err)
	}

	exports, cleanup, err = New(context.Background(), validConfig(), Dependencies{Resolver: resolverFunc(func(_ context.Context, request model.Request) (model.Request, error) {
		return requestWithDefaults(request, "p", "other"), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup(context.Background())
	if _, err = exports.Counter.CountInput(context.Background(), usage.CountRequest{}); !errors.Is(err, usage.ErrUnsupportedModel) {
		t.Fatalf("unsupported error=%v", err)
	}
}

func TestInvalidRequestsAreRejectedWithoutLeakingContent(t *testing.T) {
	t.Parallel()
	invalidUTF8 := string([]byte{0xff})
	temperature := math.Inf(1)
	zero := 0
	tests := []model.Request{
		{Messages: []model.Message{{Role: "unknown"}}},
		{Messages: []model.Message{{Role: model.RoleUser, Content: invalidUTF8}}},
		{Messages: []model.Message{{Role: model.RoleAssistant, Content: "TOP-SECRET", ToolCalls: []tool.Call{{ID: "", Name: "tool", Arguments: json.RawMessage(`{}`)}}}}},
		{Messages: []model.Message{{Role: model.RoleAssistant, ToolCalls: []tool.Call{{ID: "call", Name: "tool", Arguments: json.RawMessage(`{`)}}}}},
		{Tools: []tool.Definition{{Name: "tool", InputSchema: json.RawMessage(`{`)}}},
		{Temperature: &temperature},
		{MaxTokens: &zero},
	}
	for i, request := range tests {
		exports, cleanup, err := New(context.Background(), validConfig(), Dependencies{Resolver: resolverFunc(passthroughResolver)})
		if err != nil {
			t.Fatal(err)
		}
		_, err = exports.Counter.CountInput(context.Background(), usage.CountRequest{Invocation: requestWithDefaults(request, "p", "model")})
		_ = cleanup(context.Background())
		if !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("case %d error=%v", i, err)
		}
		if err != nil && strings.Contains(err.Error(), "TOP-SECRET") {
			t.Fatalf("case %d leaked message content: %v", i, err)
		}
	}
}

func TestCacheHitEvictionAndClosedState(t *testing.T) {
	t.Parallel()
	profile := &testProfile{source: "test-v1", accuracy: usage.AccuracyExact, count: 42}
	counter := newCounter(resolverFunc(passthroughResolver), []compiledRoute{{provider: "p", pattern: mustRegexp(t, `.*`), profile: profile}}, 1)
	first := usage.CountRequest{Invocation: validRequest("one")}
	result, err := counter.CountInput(context.Background(), first)
	if err != nil || result.InputTokens != 42 || result.Accuracy != usage.AccuracyExact {
		t.Fatalf("first result=%#v error=%v", result, err)
	}
	if _, err = counter.CountInput(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if profile.calls.Load() != 1 {
		t.Fatalf("cache calls=%d", profile.calls.Load())
	}
	if _, err = counter.CountInput(context.Background(), usage.CountRequest{Invocation: validRequest("two")}); err != nil {
		t.Fatal(err)
	}
	if _, err = counter.CountInput(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if profile.calls.Load() != 3 {
		t.Fatalf("eviction calls=%d", profile.calls.Load())
	}
	counter.close()
	if _, err = counter.CountInput(context.Background(), first); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed error=%v", err)
	}
}

func TestProfileFailuresAndNegativeCountsAreClassified(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("tokenizer failed")
	for _, profile := range []*testProfile{
		{source: "error-v1", accuracy: usage.AccuracyEstimate, err: wantErr},
		{source: "negative-v1", accuracy: usage.AccuracyEstimate, count: -1},
	} {
		counter := newCounter(resolverFunc(passthroughResolver), []compiledRoute{{provider: "p", pattern: mustRegexp(t, `.*`), profile: profile}}, 1)
		_, err := counter.CountInput(context.Background(), usage.CountRequest{Invocation: validRequest("secret-prompt")})
		if !errors.Is(err, ErrCountFailed) {
			t.Fatalf("source=%q error=%v", profile.source, err)
		}
		if profile.err != nil && !errors.Is(err, wantErr) {
			t.Fatalf("source=%q lost profile error: %v", profile.source, err)
		}
		if strings.Contains(err.Error(), "secret-prompt") {
			t.Fatalf("source=%q leaked prompt: %v", profile.source, err)
		}
	}
}

func TestSameKeySingleFlightAndCanceledWaiter(t *testing.T) {
	t.Parallel()
	profile := &testProfile{
		source: "blocking-v1", accuracy: usage.AccuracyEstimate, count: 7,
		entered: make(chan struct{}, 1), release: make(chan struct{}),
	}
	counter := newCounter(resolverFunc(passthroughResolver), []compiledRoute{{provider: "p", pattern: mustRegexp(t, `.*`), profile: profile}}, 2)
	request := usage.CountRequest{Invocation: validRequest("same")}
	firstDone := make(chan error, 1)
	go func() {
		_, err := counter.CountInput(context.Background(), request)
		firstDone <- err
	}()
	select {
	case <-profile.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("profile did not start")
	}
	waitCtx, cancel := context.WithCancel(context.Background())
	waitDone := make(chan error, 1)
	go func() {
		_, err := counter.CountInput(waitCtx, request)
		waitDone <- err
	}()
	cancel()
	select {
	case err := <-waitDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waiter error=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled waiter did not return")
	}
	close(profile.release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if profile.calls.Load() != 1 {
		t.Fatalf("profile calls=%d", profile.calls.Load())
	}
}

func TestDifferentKeysCountConcurrently(t *testing.T) {
	t.Parallel()
	profile := &testProfile{source: "parallel-v1", accuracy: usage.AccuracyEstimate, count: 1, entered: make(chan struct{}, 2), release: make(chan struct{})}
	counter := newCounter(resolverFunc(passthroughResolver), []compiledRoute{{provider: "p", pattern: mustRegexp(t, `.*`), profile: profile}}, 2)
	var wait sync.WaitGroup
	wait.Add(2)
	for _, content := range []string{"one", "two"} {
		go func() {
			defer wait.Done()
			_, _ = counter.CountInput(context.Background(), usage.CountRequest{Invocation: validRequest(content)})
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-profile.entered:
		case <-time.After(2 * time.Second):
			t.Fatal("different keys did not count concurrently")
		}
	}
	close(profile.release)
	wait.Wait()
	if profile.calls.Load() != 2 {
		t.Fatalf("profile calls=%d", profile.calls.Load())
	}
}

func validConfig() Config {
	return Config{Routes: []Route{{Provider: "p", ModelPattern: "model", Profile: unicodeEstimateSource}}}
}

func validRequest(content string) model.Request {
	return model.Request{Provider: "p", Model: "model", Messages: []model.Message{{Role: model.RoleUser, Content: content}}}
}

func requestWithDefaults(request model.Request, provider, modelName string) model.Request {
	request = cloneRequest(request)
	request.Provider = provider
	request.Model = modelName
	return request
}

func mustRegexp(t *testing.T, pattern string) *regexp.Regexp {
	t.Helper()
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}
