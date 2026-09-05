package hostcomponent

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	appbackend "github.com/ingot-agent/app-webui"
	"github.com/ingot-agent/sdk/interaction"
)

func TestSubmissionRejectsNullWithoutConsumingPending(t *testing.T) {
	for _, kind := range []interaction.FieldKind{interaction.FieldString, interaction.FieldInteger, interaction.FieldNumber, interaction.FieldBoolean, interaction.FieldMultiChoice} {
		t.Run(fmtKind(kind), func(t *testing.T) {
			host := newInteractionHost(newEventHub(8, 2))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := host.Request(ctx, interaction.Request{Name: "input", Fields: []interaction.Field{
					{Name: "value", Kind: kind, Required: true, Options: optionsForKind(kind)},
				}})
				done <- err
			}()
			pending := waitForPending(t, host)
			err := host.Respond(pending.ID, appbackend.InteractionSubmission{Values: map[string]json.RawMessage{"value": json.RawMessage(`null`)}})
			if !errors.Is(err, appbackend.ErrInvalidInteractionResponse) || len(host.Pending()) != 1 {
				t.Errorf("null response = %v, pending = %d", err, len(host.Pending()))
			}
			cancel()
			<-done
		})
	}
}

func fmtKind(kind interaction.FieldKind) string {
	name, _ := fieldKindName(kind)
	return name
}

func optionsForKind(kind interaction.FieldKind) []interaction.Option {
	if kind == interaction.FieldMultiChoice {
		return []interaction.Option{{Value: "main"}}
	}
	return nil
}

func TestRequestRejectsInvalidDefaultsAndOptions(t *testing.T) {
	wrongType := interaction.IntegerValue(1)
	wrongChoice := interaction.StringValue("missing")
	notFinite := interaction.NumberValue(math.NaN())
	for _, field := range []interaction.Field{
		{Name: "value", Kind: interaction.FieldString, Default: &wrongType},
		{Name: "value", Kind: interaction.FieldChoice, Options: []interaction.Option{{Value: "main"}}, Default: &wrongChoice},
		{Name: "value", Kind: interaction.FieldNumber, Default: &notFinite, Sensitive: true},
		{Name: "value", Kind: interaction.FieldChoice},
		{Name: "value", Kind: interaction.FieldChoice, Options: []interaction.Option{{Value: "main"}, {Value: "main"}}},
	} {
		if err := validateRequest(interaction.Request{Name: "invalid", Fields: []interaction.Field{field}}); err == nil {
			t.Errorf("accepted invalid field: %#v", field)
		}
	}
}

func TestInvalidStateDoesNotChangeSnapshotOrCursor(t *testing.T) {
	hub := newEventHub(8, 2)
	host := newInteractionHost(hub)
	for _, state := range []interaction.State{
		{Name: "invalid", Level: interaction.Level(255)},
		{Name: "invalid", Values: []interaction.Entry{{Name: "value", Value: interaction.Value{}}}},
		{Name: "invalid", Values: []interaction.Entry{{Name: "value", Value: interaction.NumberValue(math.Inf(1))}}},
		{Name: "invalid", Values: []interaction.Entry{{Name: "value", Value: interaction.StringValue("a")}, {Name: "value", Value: interaction.StringValue("b")}}},
	} {
		if err := host.Set(context.Background(), state); err == nil {
			t.Errorf("accepted invalid state: %#v", state)
		}
		if len(host.States()) != 0 || hub.Cursor() != 0 {
			t.Fatal("invalid state changed the snapshot or cursor")
		}
	}
}

type gatedSink struct {
	appbackend.EventSink
	target  string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *gatedSink) Publish(event appbackend.Event) error {
	if event.Type == s.target {
		s.once.Do(func() {
			close(s.entered)
			<-s.release
		})
	}
	return s.EventSink.Publish(event)
}

func TestStateEventsFollowMutationOrder(t *testing.T) {
	hub := newEventHub(8, 2)
	sink := &gatedSink{EventSink: hub, target: "interaction.state.set", entered: make(chan struct{}), release: make(chan struct{})}
	host := newInteractionHost(sink)
	setDone, clearDone := make(chan error, 1), make(chan error, 1)
	go func() { setDone <- host.Set(context.Background(), interaction.State{Name: "status"}) }()
	<-sink.entered
	go func() { clearDone <- host.Clear(context.Background(), "status") }()
	select {
	case err := <-clearDone:
		clearDone <- err
	case <-time.After(20 * time.Millisecond):
	}
	close(sink.release)
	if err := <-setDone; err != nil {
		t.Fatal(err)
	}
	if err := <-clearDone; err != nil {
		t.Fatal(err)
	}
	sub, err := hub.Subscribe(0)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	var types []string
	for _, record := range sub.Replay() {
		var event appbackend.Event
		if err := json.Unmarshal(record.Data, &event); err != nil {
			t.Fatal(err)
		}
		types = append(types, event.Type)
	}
	if !reflect.DeepEqual(types, []string{"interaction.state.set", "interaction.state.clear"}) || len(host.States()) != 0 {
		t.Fatalf("events = %v, states = %v", types, host.States())
	}
}

func TestRequestEventPrecedesConcurrentResponse(t *testing.T) {
	hub := newEventHub(8, 2)
	sink := &gatedSink{EventSink: hub, target: "interaction.requested", entered: make(chan struct{}), release: make(chan struct{})}
	host := newInteractionHost(sink)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requestDone, responseDone := make(chan error, 1), make(chan error, 1)
	go func() {
		_, err := host.Request(ctx, interaction.Request{Name: "prompt"})
		requestDone <- err
	}()
	<-sink.entered
	go func() {
		pending := host.Pending()
		if len(pending) != 1 {
			responseDone <- errors.New("pending request missing")
			return
		}
		responseDone <- host.Respond(pending[0].ID, appbackend.InteractionSubmission{})
	}()
	select {
	case err := <-responseDone:
		responseDone <- err
	case <-time.After(20 * time.Millisecond):
	}
	close(sink.release)
	if err := <-responseDone; err != nil {
		t.Fatal(err)
	}
	if err := <-requestDone; err != nil {
		t.Fatal(err)
	}
	sub, err := hub.Subscribe(0)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	records := sub.Replay()
	if len(records) != 2 || !strings.Contains(string(records[0].Data), "interaction.requested") || !strings.Contains(string(records[1].Data), "interaction.resolved") {
		t.Fatalf("request settlement events = %v", records)
	}
}

func TestConcurrentResponseAndCancellationSettleExactlyOnce(t *testing.T) {
	for i := 0; i < 32; i++ {
		hub := newEventHub(8, 2)
		host := newInteractionHost(hub)
		ctx, cancel := context.WithCancel(context.Background())
		requestDone := make(chan error, 1)
		go func() {
			_, err := host.Request(ctx, interaction.Request{Name: "prompt"})
			requestDone <- err
		}()
		pending := waitForPending(t, host)
		start := make(chan struct{})
		responses := make(chan error, 2)
		for j := 0; j < 2; j++ {
			go func() {
				<-start
				responses <- host.Respond(pending.ID, appbackend.InteractionSubmission{})
			}()
		}
		cancelDone := make(chan struct{})
		go func() { <-start; cancel(); close(cancelDone) }()
		close(start)
		winners := 0
		for j := 0; j < 2; j++ {
			if err := <-responses; err == nil {
				winners++
			} else if !errors.Is(err, appbackend.ErrInteractionNotFound) {
				t.Fatal(err)
			}
		}
		<-cancelDone
		requestErr := <-requestDone
		if winners > 1 || (winners == 1 && requestErr != nil) || (winners == 0 && !errors.Is(requestErr, context.Canceled)) {
			t.Fatalf("settlement winners = %d, request error = %v", winners, requestErr)
		}
		if len(host.Pending()) != 0 || hub.Cursor() != 2 {
			t.Fatalf("settlement did not produce exactly one terminal event: pending=%d cursor=%d", len(host.Pending()), hub.Cursor())
		}
	}
}

func TestInteractionRequestValidatesWithoutConsumingPending(t *testing.T) {
	hub := newEventHub(16, 4)
	host := newInteractionHost(hub)
	secret := interaction.StringValue("server-default")
	responseCh := make(chan interaction.Response, 1)
	errorCh := make(chan error, 1)
	go func() {
		response, err := host.Request(context.Background(), interaction.Request{
			Name: "confirm",
			Fields: []interaction.Field{
				{Name: "branch", Kind: interaction.FieldChoice, Required: true, Options: []interaction.Option{{Value: "main"}, {Value: "dev"}}},
				{Name: "token", Kind: interaction.FieldString, Sensitive: true, Default: &secret},
			},
		})
		responseCh <- response
		errorCh <- err
	}()

	pending := waitForPending(t, host)
	if pending.Fields[1].Default != nil || !pending.Fields[1].HasDefault {
		t.Fatalf("sensitive default leaked or disappeared: %#v", pending.Fields[1])
	}
	if err := host.Respond(pending.ID, appbackend.InteractionSubmission{Values: map[string]json.RawMessage{
		"branch": json.RawMessage(`"other"`),
	}}); !errors.Is(err, appbackend.ErrInvalidInteractionResponse) {
		t.Fatalf("invalid Respond error = %v", err)
	}
	if len(host.Pending()) != 1 {
		t.Fatal("invalid response consumed pending interaction")
	}
	if err := host.Respond(pending.ID, appbackend.InteractionSubmission{Values: map[string]json.RawMessage{
		"branch": json.RawMessage(`"dev"`),
	}}); err != nil {
		t.Fatalf("Respond: %v", err)
	}
	response := <-responseCh
	if err := <-errorCh; err != nil {
		t.Fatalf("Request: %v", err)
	}
	if len(response.Values) != 2 || response.Values[0].Name != "branch" || response.Values[1].Value.String != "server-default" {
		t.Fatalf("response = %#v", response)
	}
	sub, err := hub.Subscribe(0)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	for _, record := range sub.Replay() {
		if strings.Contains(string(record.Data), "server-default") || strings.Contains(string(record.Data), `"values"`) {
			t.Fatalf("interaction event leaked response values: %s", record.Data)
		}
	}
}

func TestInteractionRequestCancellationWinsSettlementOnce(t *testing.T) {
	host := newInteractionHost(newEventHub(8, 2))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := host.Request(ctx, interaction.Request{Name: "wait"})
		done <- err
	}()
	pending := waitForPending(t, host)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Request error = %v, want context.Canceled", err)
	}
	if err := host.Respond(pending.ID, appbackend.InteractionSubmission{}); !errors.Is(err, appbackend.ErrInteractionNotFound) {
		t.Fatalf("late Respond error = %v, want ErrInteractionNotFound", err)
	}
}

func TestScopedInteractionSnapshotsOwnCorrelation(t *testing.T) {
	hub := newEventHub(16, 4)
	host := newInteractionHost(hub)
	scope := appbackend.Scope{Operation: &appbackend.OperationScope{InvocationID: "operation-one"}}
	channel := host.Scoped(scope)
	scope.Operation.InvocationID = "mutated"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := channel.Request(ctx, interaction.Request{Name: "confirm"}); done <- err }()
	pending := waitForPending(t, host)
	if pending.Scope.Operation.InvocationID != "operation-one" {
		t.Fatalf("scope = %#v", pending.Scope)
	}
	pending.Scope.Operation.InvocationID = "caller mutation"
	if host.Pending()[0].Scope.Operation.InvocationID != "operation-one" {
		t.Fatal("pending scope shared with caller")
	}
	if err := channel.Set(ctx, interaction.State{Name: "status"}); err != nil {
		t.Fatal(err)
	}
	states := host.States()
	states[0].Scope.Operation.InvocationID = "caller mutation"
	if host.States()[0].Scope.Operation.InvocationID != "operation-one" {
		t.Fatal("state scope shared with caller")
	}
	if err := channel.Emit(ctx, interaction.Event{Name: "notice"}); err != nil {
		t.Fatal(err)
	}
	if err := channel.Clear(ctx, "status"); err != nil {
		t.Fatal(err)
	}
	if err := host.Respond(pending.ID, appbackend.InteractionSubmission{}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	sub, err := hub.Subscribe(0)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	for _, record := range sub.Replay() {
		var event appbackend.Event
		if err := json.Unmarshal(record.Data, &event); err != nil {
			t.Fatal(err)
		}
		if event.Scope == nil || event.Scope.Operation.InvocationID != "operation-one" {
			t.Fatalf("event lost operation scope: %s", record.Data)
		}
	}
}

func waitForPending(t *testing.T, host *interactionHost) appbackend.PendingInteraction {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if pending := host.Pending(); len(pending) > 0 {
			return pending[0]
		}
		select {
		case <-deadline.C:
			t.Fatal("timed out waiting for pending interaction")
		case <-ticker.C:
		}
	}
}
