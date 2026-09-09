package tooledit

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ingot-agent/sdk/interaction"
	"github.com/ingot-agent/sdk/operation"
	"github.com/ingot-agent/sdk/workspace"
)

// TestSetupOperationPersistsThroughInteraction proves the ADR 0003 §3 flow:
// the Operation asks the Host for values, then writes them into the Plugin's
// own state scope. A restart re-reads them.
func TestSetupOperationPersistsThroughInteraction(t *testing.T) {
	scope := testStateScope{dir: filepath.Join(t.TempDir(), "state")}
	deps := Dependencies{Workspace: staticResolver{binding: workspace.Binding{Root: t.TempDir()}}, State: scope}
	exports, _, err := New(context.Background(), deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(exports.Operations) != 1 {
		t.Fatalf("operations = %#v", exports.Operations)
	}
	definition := exports.Operations[0].Definition()
	if definition.Name != setupOperationName || definition.Group != setupOperationGroup {
		t.Fatalf("definition = %#v", definition)
	}
	channel := &recordingChannel{answers: map[string]interaction.Value{
		"max_file_bytes": interaction.IntegerValue(2048),
		"max_scan_bytes": interaction.IntegerValue(4096),
	}}
	result, err := exports.Operations[0].Invoke(context.Background(), operation.Request{Interaction: channel})
	if err != nil {
		t.Fatal(err)
	}
	var output struct {
		MaxFileBytes    int  `json:"max_file_bytes"`
		MaxScanBytes    int  `json:"max_scan_bytes"`
		RestartRequired bool `json:"restart_required"`
	}
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	if output.MaxFileBytes != 2048 || output.MaxScanBytes != 4096 || !output.RestartRequired {
		t.Fatalf("output = %#v", output)
	}
	// The request carried the effective defaults so a UI can prefill the form.
	if len(channel.request.Fields) != 2 || channel.request.Fields[0].Default == nil || channel.request.Fields[0].Default.Integer != defaultMaxFileBytes {
		t.Fatalf("request = %#v", channel.request)
	}
	// A fresh construction reads the persisted values back.
	reloaded, _, err := New(context.Background(), deps)
	if err != nil {
		t.Fatal(err)
	}
	edit, ok := reloaded.Tools[0].(*editTool)
	if !ok {
		t.Fatalf("unexpected tool %T", reloaded.Tools[0])
	}
	if edit.config.maxFileBytes != 2048 || edit.config.maxScanBytes != 4096 {
		t.Fatalf("reloaded config = %#v", edit.config)
	}
}

// TestSetupOperationRejectsNonPositiveValues proves the Plugin validates its
// own input instead of trusting the Host.
func TestSetupOperationRejectsNonPositiveValues(t *testing.T) {
	scope := testStateScope{dir: filepath.Join(t.TempDir(), "state")}
	exports, _, err := New(context.Background(), Dependencies{Workspace: staticResolver{binding: workspace.Binding{Root: t.TempDir()}}, State: scope})
	if err != nil {
		t.Fatal(err)
	}
	channel := &recordingChannel{answers: map[string]interaction.Value{
		"max_file_bytes": interaction.IntegerValue(0),
		"max_scan_bytes": interaction.IntegerValue(0),
	}}
	if _, err := exports.Operations[0].Invoke(context.Background(), operation.Request{Interaction: channel}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("non-positive limits = %v", err)
	}
}

// recordingChannel records the outgoing request and answers from a fixed map,
// falling back to each field's default.
type recordingChannel struct {
	request interaction.Request
	answers map[string]interaction.Value
}

func (c *recordingChannel) Request(_ context.Context, request interaction.Request) (interaction.Response, error) {
	c.request = request
	response := interaction.Response{}
	for _, field := range request.Fields {
		value, ok := c.answers[field.Name]
		if !ok {
			if field.Default == nil {
				continue
			}
			value = *field.Default
		}
		response.Values = append(response.Values, interaction.Answer{Name: field.Name, Value: value})
	}
	return response, nil
}

func (*recordingChannel) Emit(context.Context, interaction.Event) error { return nil }
func (*recordingChannel) Set(context.Context, interaction.State) error  { return nil }
func (*recordingChannel) Clear(context.Context, string) error           { return nil }
