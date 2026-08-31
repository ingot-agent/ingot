package modelruntime_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/ingot-agent/ingot-abi"
	modelruntime "github.com/ingot-agent/model-runtime"
	"github.com/ingot-agent/sdk/content"
	"github.com/ingot-agent/sdk/model"
)

func TestReasoningStreamValidation(t *testing.T) {
	rStart := model.StreamEvent{Kind: model.StreamPartStart, PartKind: content.KindText, Semantic: model.StreamSemanticReasoning}
	rDelta := model.StreamEvent{Kind: model.StreamPartDelta, TextDelta: "thinking", Semantic: model.StreamSemanticReasoning}
	rEnd := model.StreamEvent{Kind: model.StreamPartEnd, Semantic: model.StreamSemanticReasoning}
	cStart := model.StreamEvent{Kind: model.StreamPartStart, PartKind: content.KindText}
	cDelta := model.StreamEvent{Kind: model.StreamPartDelta, TextDelta: "answer"}
	cEnd := model.StreamEvent{Kind: model.StreamPartEnd}
	for _, tc := range []struct {
		name    string
		events  []model.StreamEvent
		final   content.Content
		invalid bool
	}{
		{"reasoning only", []model.StreamEvent{rStart, rDelta, rEnd}, nil, false},
		{"legacy content", []model.StreamEvent{cStart, cDelta, cEnd}, content.FromText("answer"), false},
		{"reasoning then content", []model.StreamEvent{rStart, rDelta, rEnd, cStart, cDelta, cEnd}, content.FromText("answer"), false},
		{"interleaved", []model.StreamEvent{rStart, cStart, rDelta, cDelta, rDelta, cEnd, rEnd}, content.FromText("answer"), false},
		{"multiple reasoning parts", []model.StreamEvent{rStart, rDelta, rEnd,
			{Kind: model.StreamPartStart, PartIndex: 1, PartKind: content.KindText, Semantic: model.StreamSemanticReasoning},
			{Kind: model.StreamPartDelta, PartIndex: 1, Semantic: model.StreamSemanticReasoning, TextDelta: "more"},
			{Kind: model.StreamPartEnd, PartIndex: 1, Semantic: model.StreamSemanticReasoning}}, nil, false},
		{"reasoning must not enter final", []model.StreamEvent{rStart, rDelta, rEnd}, content.FromText("thinking"), true},
		{"content still validated", []model.StreamEvent{rStart, rDelta, rEnd, cStart, cDelta, cEnd}, content.FromText("wrong"), true},
		{"unclosed reasoning", []model.StreamEvent{rStart, rDelta}, nil, true},
		{"reasoning delta before start", []model.StreamEvent{rDelta}, nil, true},
		{"semantic changed", []model.StreamEvent{rStart, cDelta, rEnd}, nil, true},
		{"unknown semantic", []model.StreamEvent{{Kind: model.StreamPartStart, PartKind: content.KindText, Semantic: 255}}, nil, true},
		{"reasoning index gap", []model.StreamEvent{{Kind: model.StreamPartStart, PartIndex: 1, PartKind: content.KindText, Semantic: model.StreamSemanticReasoning}}, nil, true},
		{"reasoning is text", []model.StreamEvent{{Kind: model.StreamPartStart, PartKind: content.KindAudio, Semantic: model.StreamSemanticReasoning}}, nil, true},
		{"reasoning rejects bytes", []model.StreamEvent{rStart, {Kind: model.StreamPartDelta, Semantic: model.StreamSemanticReasoning, DataDelta: []byte("x")}}, nil, true},
		{"reasoning rejects invalid utf8", []model.StreamEvent{rStart, {Kind: model.StreamPartDelta, Semantic: model.StreamSemanticReasoning, TextDelta: string([]byte{0xff})}}, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &eventStreamingProvider{events: tc.events, response: model.Response{Message: model.Message{Role: model.RoleAssistant, Content: tc.final}}}
			exports, _, err := modelruntime.New(context.Background(), modelruntime.Config{DefaultModel: "m"}, modelruntime.Dependencies{Providers: []ingotabi.Named[model.Provider]{{Name: "p", Value: provider}}})
			if err != nil {
				t.Fatal(err)
			}
			var events []model.StreamEvent
			_, err = exports.Streaming.Stream(context.Background(), model.Request{}, func(event model.StreamEvent) error { events = append(events, event); return nil })
			if tc.invalid {
				if !errors.Is(err, modelruntime.ErrInvalidResponse) {
					t.Fatalf("error=%v", err)
				}
			} else if err != nil || !reflect.DeepEqual(events, tc.events) {
				t.Fatalf("events=%v error=%v", events, err)
			}
		})
	}
}
