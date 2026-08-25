package script

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ingot-agent/sdk/agent"
	"github.com/ingot-agent/sdk/model"
	"github.com/ingot-agent/sdk/session"
	"github.com/ingot-agent/sdk/tool"
)

func TestHookHelper(t *testing.T) {
	separator := -1
	for i, argument := range os.Args {
		if argument == "--" {
			separator = i
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		return
	}
	mode := os.Args[separator+1]
	raw, _ := io.ReadAll(os.Stdin)
	var envelope struct {
		Phase string `json:"phase"`
	}
	_ = json.Unmarshal(raw, &envelope)
	switch mode {
	case "record":
		if os.Getenv("INGOT_SCRIPT_PARENT_SECRET") != "" {
			fmt.Fprint(os.Stderr, "inherited parent environment")
			os.Exit(2)
		}
		trace := os.Args[separator+2]
		file, err := os.OpenFile(trace, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			os.Exit(2)
		}
		_, _ = file.Write(append(raw, '\n'))
		_ = file.Close()
		fmt.Fprint(os.Stdout, `{"protocol_version":1,"action":"continue"}`)
		os.Exit(0)
	case "reject":
		fmt.Fprint(os.Stdout, `{"protocol_version":1,"action":"reject","message":"denied"}`)
		os.Exit(0)
	case "fail-after":
		if envelope.Phase == "after" {
			fmt.Fprint(os.Stderr, "after failed")
			os.Exit(3)
		}
		fmt.Fprint(os.Stdout, `{"protocol_version":1,"action":"continue"}`)
		os.Exit(0)
	case "sleep":
		time.Sleep(5 * time.Second)
	case "sleep-after":
		if envelope.Phase == "after" {
			time.Sleep(5 * time.Second)
		}
		fmt.Fprint(os.Stdout, `{"protocol_version":1,"action":"continue"}`)
		os.Exit(0)
	case "stderr":
		fmt.Fprint(os.Stdout, `{"protocol_version":1,"action":"continue"}`)
		fmt.Fprint(os.Stderr, "warning")
		os.Exit(0)
	case "nonzero":
		fmt.Fprint(os.Stderr, "failed")
		os.Exit(7)
	case "oversize":
		fmt.Fprint(os.Stdout, strings.Repeat("x", 1024))
		os.Exit(0)
	default:
		os.Exit(2)
	}
}

func TestExportsAllTypedTargets(t *testing.T) {
	targets := []string{"tool", "model", "model-stream", "agent"}
	hooks := make([]Hook, len(targets))
	for i, target := range targets {
		hooks[i] = helperHook(t, "reject")
		hooks[i].Name = target
		hooks[i].Target = target
	}
	exports, _, err := New(context.Background(), Config{Hooks: hooks}, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	if len(exports.ToolInterceptors) != 1 || len(exports.ModelInterceptors) != 1 || len(exports.StreamInterceptors) != 1 || len(exports.AgentInterceptors) != 1 {
		t.Fatalf("exports=%#v", exports)
	}
}

func helperHook(t *testing.T, mode string, extra ...string) Hook {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"-test.run=^TestHookHelper$", "--", mode}
	args = append(args, extra...)
	return Hook{
		Name: "policy", Target: "tool", Executable: executable, Args: args, TimeoutSeconds: 5,
		Environment: map[string]string{"GOCOVERDIR": t.TempDir()},
	}
}

func TestDecodeHookResponseIsStrict(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		raw  []byte
	}{
		{name: "not object", raw: []byte(`[]`)},
		{name: "unknown", raw: []byte(`{"protocol_version":1,"action":"continue","extra":true}`)},
		{name: "duplicate", raw: []byte(`{"protocol_version":1,"action":"continue","action":"continue"}`)},
		{name: "null message", raw: []byte(`{"protocol_version":1,"action":"continue","message":null}`)},
		{name: "missing version", raw: []byte(`{"action":"continue"}`)},
		{name: "missing action", raw: []byte(`{"protocol_version":1}`)},
		{name: "wrong type", raw: []byte(`{"protocol_version":1,"action":1}`)},
		{name: "multiple values", raw: []byte(`{"protocol_version":1,"action":"continue"}{}`)},
		{name: "invalid utf8", raw: []byte{'{', '"', 0xff, '"', ':', '1', '}'}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := decodeHookResponse(test.raw); err == nil {
				t.Fatal("expected protocol error")
			}
		})
	}
	if response, err := decodeHookResponse([]byte(" \n{\"protocol_version\":1,\"action\":\"continue\"}\t")); err != nil || response.Action != "continue" {
		t.Fatalf("response=%#v error=%v", response, err)
	}
}

func TestProjectionRejectsInvalidUTF8AndNonFiniteNumbers(t *testing.T) {
	t.Parallel()
	invalid := string([]byte{0xff})
	invalidJSON := json.RawMessage([]byte{'"', 0xff, '"'})
	temperature := math.NaN()
	tests := []struct {
		name    string
		project func() error
	}{
		{name: "tool id", project: func() error {
			_, err := projectToolRequest(tool.Call{ID: invalid, Arguments: json.RawMessage(`{}`)})
			return err
		}},
		{name: "tool arguments", project: func() error { _, err := projectToolRequest(tool.Call{Arguments: invalidJSON}); return err }},
		{name: "tool result", project: func() error { _, err := projectToolResponse(tool.Result{Content: invalid}); return err }},
		{name: "model provider", project: func() error { _, err := projectModelRequest(model.Request{Provider: invalid}); return err }},
		{name: "message content", project: func() error { _, err := projectMessage(model.Message{Content: invalid}); return err }},
		{name: "definition schema", project: func() error {
			_, err := projectModelRequest(model.Request{Tools: []tool.Definition{{InputSchema: invalidJSON}}})
			return err
		}},
		{name: "stop", project: func() error { _, err := projectModelRequest(model.Request{Stop: []string{invalid}}); return err }},
		{name: "temperature", project: func() error { _, err := projectModelRequest(model.Request{Temperature: &temperature}); return err }},
		{name: "agent request", project: func() error { _, err := projectAgentRequest(agent.Turn{Input: invalid}); return err }},
		{name: "agent response", project: func() error { _, err := projectAgentResponse(agent.Result{Output: invalid}); return err }},
		{name: "error", project: func() error { _, err := describeError(errors.New(invalid)); return err }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := test.project(); err == nil {
				t.Fatal("expected projection error")
			}
		})
	}
}

func TestToolHookProjectsBeforeAfterAndIsolatesEnvironment(t *testing.T) {
	t.Setenv("INGOT_SCRIPT_PARENT_SECRET", "must-not-leak")
	trace := t.TempDir() + string(os.PathSeparator) + "trace.jsonl"
	exports, _, err := New(context.Background(), Config{Hooks: []Hook{helperHook(t, "record", trace)}}, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	result, err := exports.ToolInterceptors[0].Invoke(context.Background(), tool.Call{ID: "c1", Name: "echo", Arguments: json.RawMessage(`{"x":1}`)}, func(_ context.Context, call tool.Call) (tool.Result, error) {
		called = true
		return tool.Result{Content: "ok"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called || result.Content != "ok" {
		t.Fatalf("called=%v result=%#v", called, result)
	}
	raw, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("trace=%s", raw)
	}
	for i, phase := range []string{"before", "after"} {
		var envelope map[string]any
		if err := json.Unmarshal([]byte(lines[i]), &envelope); err != nil || envelope["phase"] != phase || envelope["target"] != "tool" {
			t.Fatalf("line %d=%s envelope=%v err=%v", i, lines[i], envelope, err)
		}
	}
}

func TestModelStreamAndAgentProjectionGolden(t *testing.T) {
	t.Run("model", func(t *testing.T) {
		trace := tracePath(t)
		hook := helperHook(t, "record", trace)
		hook.Target = "model"
		exports, _, err := New(context.Background(), Config{Hooks: []Hook{hook}}, Dependencies{})
		if err != nil {
			t.Fatal(err)
		}
		request := model.Request{
			Provider: "p", Model: "m", Messages: []model.Message{{Role: model.RoleUser, Content: "hello"}},
			Tools: []tool.Definition{{Name: "echo", Description: "Echo", InputSchema: json.RawMessage(`{"type":"object"}`)}}, Stop: []string{},
		}
		response := model.Response{Message: model.Message{Role: model.RoleAssistant, Content: "hi"}, FinishReason: "stop", Usage: model.Usage{InputTokens: 1, OutputTokens: 2, TotalTokens: 3}, Provider: "p", Model: "m"}
		got, err := exports.ModelInterceptors[0].Invoke(context.Background(), request, func(context.Context, model.Request) (model.Response, error) { return response, nil })
		if err != nil || !reflect.DeepEqual(got, response) {
			t.Fatalf("response=%#v error=%v", got, err)
		}
		assertTraceJSON(t, trace, []string{
			`{"protocol_version":1,"hook":"policy","target":"model","phase":"before","request":{"provider":"p","model":"m","messages":[{"role":"user","content":"hello","name":"","tool_call_id":"","tool_calls":[]}],"tools":[{"name":"echo","description":"Echo","input_schema":{"type":"object"}}],"temperature":null,"max_tokens":null,"stop":[]}}`,
			`{"protocol_version":1,"hook":"policy","target":"model","phase":"after","request":{"provider":"p","model":"m","messages":[{"role":"user","content":"hello","name":"","tool_call_id":"","tool_calls":[]}],"tools":[{"name":"echo","description":"Echo","input_schema":{"type":"object"}}],"temperature":null,"max_tokens":null,"stop":[]},"outcome":{"response":{"message":{"role":"assistant","content":"hi","name":"","tool_call_id":"","tool_calls":[]},"finish_reason":"stop","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3},"provider":"p","model":"m"},"error":null}}`,
		})
	})

	t.Run("model-stream", func(t *testing.T) {
		trace := tracePath(t)
		hook := helperHook(t, "record", trace)
		hook.Target = "model-stream"
		exports, _, err := New(context.Background(), Config{Hooks: []Hook{hook}}, Dependencies{})
		if err != nil {
			t.Fatal(err)
		}
		request := model.Request{Provider: "p", Model: "m"}
		response := model.Response{Message: model.Message{Role: model.RoleAssistant, Content: "done"}}
		got, err := exports.StreamInterceptors[0].InvokeStream(context.Background(), request, nil, func(context.Context, model.Request, model.StreamHandler) (model.Response, error) {
			return response, nil
		})
		if err != nil || !reflect.DeepEqual(got, response) {
			t.Fatalf("response=%#v error=%v", got, err)
		}
		assertTraceJSON(t, trace, []string{
			`{"protocol_version":1,"hook":"policy","target":"model-stream","phase":"before","request":{"provider":"p","model":"m","messages":[],"tools":[],"temperature":null,"max_tokens":null,"stop":[]}}`,
			`{"protocol_version":1,"hook":"policy","target":"model-stream","phase":"after","request":{"provider":"p","model":"m","messages":[],"tools":[],"temperature":null,"max_tokens":null,"stop":[]},"outcome":{"response":{"message":{"role":"assistant","content":"done","name":"","tool_call_id":"","tool_calls":[]},"finish_reason":"","usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0},"provider":"","model":""},"error":null}}`,
		})
	})

	t.Run("agent", func(t *testing.T) {
		trace := tracePath(t)
		hook := helperHook(t, "record", trace)
		hook.Target = "agent"
		exports, _, err := New(context.Background(), Config{Hooks: []Hook{hook}}, Dependencies{})
		if err != nil {
			t.Fatal(err)
		}
		request := agent.Turn{SessionID: session.ID("s1"), Input: "hello"}
		response := agent.Result{Output: "world"}
		got, err := exports.AgentInterceptors[0].Invoke(context.Background(), request, func(context.Context, agent.Turn) (agent.Result, error) { return response, nil })
		if err != nil || got != response {
			t.Fatalf("response=%#v error=%v", got, err)
		}
		assertTraceJSON(t, trace, []string{
			`{"protocol_version":1,"hook":"policy","target":"agent","phase":"before","request":{"session_id":"s1","input":"hello"}}`,
			`{"protocol_version":1,"hook":"policy","target":"agent","phase":"after","request":{"session_id":"s1","input":"hello"},"outcome":{"response":{"output":"world"},"error":null}}`,
		})
	})
}

func tracePath(t *testing.T) string {
	t.Helper()
	return t.TempDir() + string(os.PathSeparator) + "trace.jsonl"
}

func assertTraceJSON(t *testing.T, path string, expected []string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	actual := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(actual) != len(expected) {
		t.Fatalf("trace lines=%d, want %d: %s", len(actual), len(expected), raw)
	}
	for i := range expected {
		var gotValue, wantValue any
		if err := json.Unmarshal([]byte(actual[i]), &gotValue); err != nil {
			t.Fatalf("actual line %d: %v", i, err)
		}
		if err := json.Unmarshal([]byte(expected[i]), &wantValue); err != nil {
			t.Fatalf("expected line %d: %v", i, err)
		}
		if !reflect.DeepEqual(gotValue, wantValue) {
			t.Fatalf("line %d\ngot:  %s\nwant: %s", i, actual[i], expected[i])
		}
	}
}

func TestRejectShortCircuitsAndAfterFailureMarksUnknown(t *testing.T) {
	exports, _, err := New(context.Background(), Config{Hooks: []Hook{helperHook(t, "reject")}}, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	_, err = exports.ToolInterceptors[0].Invoke(context.Background(), tool.Call{Arguments: json.RawMessage(`{}`)}, func(context.Context, tool.Call) (tool.Result, error) {
		called = true
		return tool.Result{}, nil
	})
	if !errors.Is(err, ErrHookRejected) || called {
		t.Fatalf("reject error=%v called=%v", err, called)
	}

	exports, _, err = New(context.Background(), Config{Hooks: []Hook{helperHook(t, "fail-after")}}, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := exports.ToolInterceptors[0].Invoke(context.Background(), tool.Call{Arguments: json.RawMessage(`{}`)}, func(context.Context, tool.Call) (tool.Result, error) {
		return tool.Result{Content: "committed"}, nil
	})
	if result.Content != "committed" || !errors.Is(err, ErrAfterHookFailed) || !errors.Is(err, ErrCompletionUnknown) {
		t.Fatalf("result=%#v error=%v", result, err)
	}
}

func TestHookTimeoutAndOutputLimitFailClosed(t *testing.T) {
	hook := helperHook(t, "sleep")
	hook.TimeoutSeconds = 1
	exports, _, err := New(context.Background(), Config{Hooks: []Hook{hook}}, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = exports.ToolInterceptors[0].Invoke(context.Background(), tool.Call{Arguments: json.RawMessage(`{}`)}, func(context.Context, tool.Call) (tool.Result, error) {
		return tool.Result{}, nil
	})
	if !errors.Is(err, ErrHookFailed) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error=%v", err)
	}

	hook = helperHook(t, "oversize")
	hook.MaxOutputBytes = 8
	exports, _, err = New(context.Background(), Config{Hooks: []Hook{hook}}, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = exports.ToolInterceptors[0].Invoke(context.Background(), tool.Call{Arguments: json.RawMessage(`{}`)}, func(context.Context, tool.Call) (tool.Result, error) {
		return tool.Result{}, nil
	})
	if !errors.Is(err, ErrHookFailed) {
		t.Fatalf("output error=%v", err)
	}
}

func TestCancellationAfterDownstreamIsNotSwallowed(t *testing.T) {
	trace := tracePath(t)
	exports, _, err := New(context.Background(), Config{Hooks: []Hook{helperHook(t, "record", trace)}}, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result, err := exports.ToolInterceptors[0].Invoke(ctx, tool.Call{Arguments: json.RawMessage(`{}`)}, func(context.Context, tool.Call) (tool.Result, error) {
		cancel()
		return tool.Result{Content: "committed"}, nil
	})
	if result.Content != "committed" || !errors.Is(err, context.Canceled) {
		t.Fatalf("result=%#v error=%v", result, err)
	}
	raw, readErr := os.ReadFile(trace)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if lines := strings.Split(strings.TrimSpace(string(raw)), "\n"); len(lines) != 1 || !strings.Contains(lines[0], `"phase":"before"`) {
		t.Fatalf("trace=%s", raw)
	}
}

func TestResponseProjectionFailureIsAfterFailure(t *testing.T) {
	trace := tracePath(t)
	hook := helperHook(t, "record", trace)
	hook.Target = "model"
	exports, _, err := New(context.Background(), Config{Hooks: []Hook{hook}}, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	response := model.Response{Message: model.Message{Content: string([]byte{0xff})}}
	got, err := exports.ModelInterceptors[0].Invoke(context.Background(), model.Request{}, func(context.Context, model.Request) (model.Response, error) {
		return response, nil
	})
	if !reflect.DeepEqual(got, response) || !errors.Is(err, ErrAfterHookFailed) || !errors.Is(err, ErrCompletionUnknown) || !errors.Is(err, ErrHookFailed) {
		t.Fatalf("response=%#v error=%v", got, err)
	}
	raw, readErr := os.ReadFile(trace)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if lines := strings.Split(strings.TrimSpace(string(raw)), "\n"); len(lines) != 1 {
		t.Fatalf("trace=%s", raw)
	}
}

func TestAfterFailurePreservesTimeoutAndDownstreamErrors(t *testing.T) {
	hook := helperHook(t, "sleep-after")
	// The race detector can make a fresh Windows helper process take more than
	// one second to start. Keep the timeout below the helper's five-second sleep
	// while leaving enough headroom for the before phase to complete.
	hook.TimeoutSeconds = 3
	exports, _, err := New(context.Background(), Config{Hooks: []Hook{hook}}, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := exports.ToolInterceptors[0].Invoke(context.Background(), tool.Call{Arguments: json.RawMessage(`{}`)}, func(context.Context, tool.Call) (tool.Result, error) {
		return tool.Result{Content: "committed"}, nil
	})
	if result.Content != "committed" || !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrAfterHookFailed) || !errors.Is(err, ErrCompletionUnknown) || !errors.Is(err, ErrHookFailed) {
		t.Fatalf("result=%#v error=%v", result, err)
	}

	downstreamErr := errors.New("downstream failed")
	exports, _, err = New(context.Background(), Config{Hooks: []Hook{helperHook(t, "fail-after")}}, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = exports.ToolInterceptors[0].Invoke(context.Background(), tool.Call{Arguments: json.RawMessage(`{}`)}, func(context.Context, tool.Call) (tool.Result, error) {
		return tool.Result{}, downstreamErr
	})
	if !errors.Is(err, downstreamErr) || !errors.Is(err, ErrAfterHookFailed) || !errors.Is(err, ErrCompletionUnknown) || !errors.Is(err, ErrHookFailed) {
		t.Fatalf("joined error=%v", err)
	}
}

func TestSuccessfulStderrAndNonzeroExitFailClosed(t *testing.T) {
	for _, mode := range []string{"stderr", "nonzero"} {
		t.Run(mode, func(t *testing.T) {
			exports, _, err := New(context.Background(), Config{Hooks: []Hook{helperHook(t, mode)}}, Dependencies{})
			if err != nil {
				t.Fatal(err)
			}
			_, err = exports.ToolInterceptors[0].Invoke(context.Background(), tool.Call{Arguments: json.RawMessage(`{}`)}, func(context.Context, tool.Call) (tool.Result, error) {
				return tool.Result{}, nil
			})
			if !errors.Is(err, ErrHookFailed) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestTimeoutSecondsOverflowIsRejected(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("timeout overflow value cannot be represented by int on this platform")
	}
	hook := helperHook(t, "reject")
	maximum := int64((1<<63 - 1) / int64(time.Second))
	hook.TimeoutSeconds = int(maximum + 1)
	_, _, err := New(context.Background(), Config{Hooks: []Hook{hook}}, Dependencies{})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error=%v", err)
	}
}
