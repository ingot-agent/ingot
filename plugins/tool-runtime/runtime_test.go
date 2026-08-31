package toolruntime

import (
	"context"
	"errors"
	"testing"

	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/pipeline"
	"github.com/ingot-agent/sdk/tool"
)

type fakeTool struct {
	definition tool.Definition
	calls      int
	received   []byte
	content    string
}

func (f *fakeTool) Definition() tool.Definition { return f.definition }
func (f *fakeTool) Invoke(_ context.Context, call tool.Call) (tool.Result, error) {
	f.calls++
	f.received = append([]byte(nil), call.Arguments...)
	return tool.Result{Content: content.FromText(f.content)}, nil
}

type recordingInterceptor struct {
	name   string
	events *[]string
}

type mutatingInterceptor struct{}

func (mutatingInterceptor) Invoke(ctx context.Context, call tool.Call, next pipeline.Next[tool.Call, tool.Result]) (tool.Result, error) {
	for i := range call.Arguments {
		if call.Arguments[i] == 'v' {
			call.Arguments[i] = 'x'
			break
		}
	}
	return next(ctx, call)
}

func (i recordingInterceptor) Invoke(ctx context.Context, call tool.Call, next pipeline.Next[tool.Call, tool.Result]) (tool.Result, error) {
	*i.events = append(*i.events, i.name+"-before")
	result, err := next(ctx, call)
	*i.events = append(*i.events, i.name+"-after")
	return result, err
}

func validDefinition(name string) tool.Definition {
	return tool.Definition{Name: name, Description: "test", InputSchema: []byte("{\"type\":\"object\",\"additionalProperties\":false,\"required\":[\"x\"],\"properties\":{\"x\":{\"type\":\"string\"}}}")}
}

func TestRuntimeValidatesAndSnapshotsDefinitions(t *testing.T) {
	fake := &fakeTool{definition: validDefinition("echo"), content: "ok"}
	exports, _, err := New(context.Background(), Config{}, Dependencies{Tools: []tool.Tool{fake}})
	if err != nil {
		t.Fatal(err)
	}
	definitions := exports.Runtime.Definitions()
	if len(definitions) != 1 || definitions[0].Name != "echo" {
		t.Fatalf("definitions=%#v", definitions)
	}
	definitions[0].InputSchema[0] = 'X'
	if exports.Runtime.Definitions()[0].InputSchema[0] == 'X' {
		t.Fatal("definition schema was not copied")
	}
	result, err := exports.Runtime.Call(context.Background(), tool.Call{Name: "echo", Arguments: []byte("{\"x\":\"value\"}")})
	if text, ok := content.TextOnly(result.Content); err != nil || !ok || text != "ok" || fake.calls != 1 {
		t.Fatalf("valid call result=%#v err=%v calls=%d", result, err, fake.calls)
	}
	_, err = exports.Runtime.Call(context.Background(), tool.Call{Name: "echo", Arguments: []byte("{\"x\":1}")})
	if !errors.Is(err, tool.ErrInvalidArguments) || fake.calls != 1 {
		t.Fatalf("schema error=%v calls=%d", err, fake.calls)
	}
	_, err = exports.Runtime.Call(context.Background(), tool.Call{Name: "missing", Arguments: []byte("{}")})
	if !errors.Is(err, tool.ErrNotFound) {
		t.Fatalf("not found=%v", err)
	}
}

func TestRuntimeInterceptorOrderAndLimits(t *testing.T) {
	fake := &fakeTool{definition: validDefinition("echo"), content: "0123456789"}
	events := []string{}
	exports, _, err := New(context.Background(), Config{MaxTextBytes: 5}, Dependencies{Tools: []tool.Tool{fake}, Interceptors: []tool.Interceptor{recordingInterceptor{name: "outer", events: &events}, recordingInterceptor{name: "inner", events: &events}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = exports.Runtime.Call(context.Background(), tool.Call{Name: "echo", Arguments: []byte("{\"x\":\"value\"}")})
	if !errors.Is(err, ErrInvalidResult) {
		t.Fatalf("result limit=%v", err)
	}
	want := []string{"outer-before", "inner-before", "inner-after", "outer-after"}
	for i := range want {
		if i >= len(events) || events[i] != want[i] {
			t.Fatalf("events=%v want=%v", events, want)
		}
	}
}

type resultTool struct {
	result tool.Result
}

func (t *resultTool) Definition() tool.Definition { return validDefinition("media") }
func (t *resultTool) Invoke(context.Context, tool.Call) (tool.Result, error) {
	return t.result, nil
}

func TestRuntimeValidatesLimitsAndOwnsMultimodalResult(t *testing.T) {
	data := []byte{1, 2, 3}
	implementation := &resultTool{result: tool.Result{Content: content.Content{
		content.Text("ok"),
		content.Inline(content.KindImage, "image/png", "image.png", data),
	}}}
	exports, _, err := New(context.Background(), Config{MaxTextBytes: 2, MaxInlinePartBytes: 3, MaxInlineBytes: 3}, Dependencies{Tools: []tool.Tool{implementation}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := exports.Runtime.Call(context.Background(), tool.Call{Name: "media", Arguments: []byte(`{"x":"value"}`)})
	if err != nil {
		t.Fatal(err)
	}
	implementation.result.Content[1].Media.Source.Data[0] = 9
	if result.Content[1].Media.Source.Data[0] != 1 {
		t.Fatal("runtime returned aliased inline data")
	}

	implementation.result.Content = content.Content{content.Inline(content.KindImage, "image/png", "", []byte{1, 2, 3, 4})}
	if _, err := exports.Runtime.Call(context.Background(), tool.Call{Name: "media", Arguments: []byte(`{"x":"value"}`)}); !errors.Is(err, ErrInvalidResult) {
		t.Fatalf("inline part limit error = %v", err)
	}
	implementation.result.Content = content.Content{{Kind: content.KindText, Text: string([]byte{0xff})}}
	if _, err := exports.Runtime.Call(context.Background(), tool.Call{Name: "media", Arguments: []byte(`{"x":"value"}`)}); !errors.Is(err, content.ErrInvalidContent) {
		t.Fatalf("invalid content error = %v", err)
	}
}

func TestRuntimeCopiesArgumentsAndRejectsCallMutation(t *testing.T) {
	fake := &fakeTool{definition: validDefinition("echo"), content: "ok"}
	exports, _, err := New(context.Background(), Config{}, Dependencies{
		Tools:        []tool.Tool{fake},
		Interceptors: []tool.Interceptor{mutatingInterceptor{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"x":"value"}`)
	_, err = exports.Runtime.Call(context.Background(), tool.Call{Name: "echo", Arguments: original})
	if !errors.Is(err, ErrCallMutation) {
		t.Fatalf("mutation error = %v", err)
	}
	if string(original) != `{"x":"value"}` {
		t.Fatalf("caller arguments were mutated: %s", original)
	}
	if fake.calls != 0 {
		t.Fatalf("tool calls = %d, want 0", fake.calls)
	}
}

func TestRuntimeRejectsInvalidDefinitions(t *testing.T) {
	for _, definition := range []tool.Definition{
		{Name: "Bad", Description: "x", InputSchema: []byte("{}")},
		{Name: "good", Description: "", InputSchema: []byte("{}")},
		{Name: "good", Description: "x", InputSchema: []byte("[]")},
		{Name: "good", Description: "x", InputSchema: []byte("{\"$schema\":\"http://json-schema.org/draft-07/schema#\"}")},
	} {
		_, _, err := New(context.Background(), Config{}, Dependencies{Tools: []tool.Tool{&fakeTool{definition: definition}}})
		if !errors.Is(err, ErrInvalidDefinition) {
			t.Fatalf("definition %#v error=%v", definition, err)
		}
	}
}
