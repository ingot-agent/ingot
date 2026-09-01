package openaicompat_test

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	openaicompat "github.com/ingot-agent/model-openai-compatible"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
)

func TestReasoningFieldsRemainTransientAndOrdered(t *testing.T) {
	for _, tc := range []struct {
		name      string
		deltas    []string
		texts     []string
		semantics []model.StreamSemantic
		output    string
	}{
		{"reasoning_content", []string{`"reasoning_content":"think"`, `"reasoning_content":" more"`, `"content":"answer"`}, []string{"think", " more", "answer"}, []model.StreamSemantic{model.StreamSemanticReasoning, model.StreamSemanticReasoning, model.StreamSemanticContent}, "answer"},
		{"reasoning alias", []string{`"reasoning":"think"`, `"content":"answer"`}, []string{"think", "answer"}, []model.StreamSemantic{model.StreamSemanticReasoning, model.StreamSemanticContent}, "answer"},
		{"same chunk prefers primary", []string{`"reasoning_content":"think","reasoning":"duplicate","content":"answer"`}, []string{"think", "answer"}, []model.StreamSemantic{model.StreamSemanticReasoning, model.StreamSemanticContent}, "answer"},
		{"reasoning only", []string{`"reasoning_content":"think"`}, []string{"think"}, []model.StreamSemantic{model.StreamSemanticReasoning}, ""},
		{"content only", []string{`"content":"answer"`}, []string{"answer"}, []model.StreamSemantic{model.StreamSemanticContent}, "answer"},
		{"null and empty", []string{`"reasoning_content":null,"reasoning":"","content":null`, `"reasoning_content":"","content":"answer"`}, []string{"answer"}, []model.StreamSemantic{model.StreamSemanticContent}, "answer"},
		{"interleaved", []string{`"content":"an"`, `"reasoning_content":"think"`, `"content":"swer"`}, []string{"an", "think", "swer"}, []model.StreamSemantic{model.StreamSemanticContent, model.StreamSemanticReasoning, model.StreamSemanticContent}, "answer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var sse strings.Builder
			for _, delta := range tc.deltas {
				sse.WriteString(`data: {"model":"m","choices":[{"index":0,"delta":{"role":"assistant",` + delta + `}}]}` + "\n\n")
			}
			sse.WriteString(`data: {"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n")
			provider := newProvider(t, openaicompat.ProviderConfig{Name: "p", BaseURL: "https://example.test"}, clientFunc(func(context.Context, *http.Request) (*http.Response, error) {
				return response(http.StatusOK, sse.String()), nil
			})).(model.StreamingProvider)
			var texts []string
			var semantics []model.StreamSemantic
			active := map[model.StreamSemantic]bool{}
			result, err := provider.Stream(context.Background(), model.Request{Model: "m"}, func(event model.StreamEvent) error {
				if event.PartIndex != 0 {
					t.Fatalf("unexpected index: %v", event)
				}
				switch event.Kind {
				case model.StreamPartStart:
					if active[event.Semantic] || event.PartKind != content.KindText {
						t.Fatalf("bad start: %v", event)
					}
					active[event.Semantic] = true
				case model.StreamPartDelta:
					if !active[event.Semantic] {
						t.Fatalf("delta before start: %v", event)
					}
					texts = append(texts, event.TextDelta)
					semantics = append(semantics, event.Semantic)
				case model.StreamPartEnd:
					if !active[event.Semantic] {
						t.Fatalf("end without start: %v", event)
					}
					active[event.Semantic] = false
				}
				return nil
			})
			if err != nil || !reflect.DeepEqual(texts, tc.texts) || !reflect.DeepEqual(semantics, tc.semantics) {
				t.Fatalf("texts=%v semantics=%v err=%v", texts, semantics, err)
			}
			text, _ := content.TextOnly(result.Message.Content)
			if text != tc.output || active[model.StreamSemanticContent] || active[model.StreamSemanticReasoning] {
				t.Fatalf("result=%v active=%v", result, active)
			}
		})
	}
}

func TestReasoningHandlerFailureAndCancellationStopSameChunk(t *testing.T) {
	for _, cancelInHandler := range []bool{false, true} {
		body := &trackingBody{Reader: strings.NewReader(`data: {"model":"m","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"think","content":"must not arrive"},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n")}
		provider := newProvider(t, openaicompat.ProviderConfig{Name: "p", BaseURL: "https://example.test"}, clientFunc(func(context.Context, *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body}, nil
		})).(model.StreamingProvider)
		ctx, cancel := context.WithCancel(context.Background())
		consumerErr := errors.New("consumer stopped")
		count := 0
		_, err := provider.Stream(ctx, model.Request{Model: "m"}, func(event model.StreamEvent) error {
			count++
			if event.Kind == model.StreamPartDelta {
				if cancelInHandler {
					cancel()
					return nil
				}
				return consumerErr
			}
			return nil
		})
		cancel()
		want := consumerErr
		if cancelInHandler {
			want = context.Canceled
		}
		if err != want || count != 2 || !body.isClosed() {
			t.Fatalf("cancel=%v err=%v calls=%d closed=%v", cancelInHandler, err, count, body.isClosed())
		}
	}
}
