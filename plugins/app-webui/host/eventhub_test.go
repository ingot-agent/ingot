package hostcomponent

import (
	"encoding/json"
	"errors"
	"testing"

	appbackend "github.com/ingot-agent/app-webui"
)

func TestEventHubSerializationFailureDoesNotAdvanceCursor(t *testing.T) {
	hub := newEventHub(4, 2)
	if err := hub.Publish(appbackend.Event{Type: "invalid", Data: json.RawMessage(`invalid`)}); err == nil {
		t.Fatal("accepted invalid JSON event")
	}
	if hub.Cursor() != 0 {
		t.Fatal("failed serialization consumed a cursor")
	}
}

func TestEventHubSnapshotsOwnTheirBytes(t *testing.T) {
	hub := newEventHub(4, 2)
	first, err := hub.Subscribe(0)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := hub.Subscribe(0)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	mustPublish(t, hub, "event")
	record := <-first.Events()
	record.Data[0] = '!'
	if record := <-second.Events(); !json.Valid(record.Data) {
		t.Fatal("one subscriber mutated another subscriber's event")
	}
	replay, err := hub.Subscribe(0)
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()
	records := replay.Replay()
	if !json.Valid(records[0].Data) {
		t.Fatal("subscriber mutated the retained replay")
	}
	records[0].Data[0] = '!'
	if !json.Valid(replay.Replay()[0].Data) {
		t.Fatal("Replay returned shared event bytes")
	}
}

func TestEventHubReplayToLive(t *testing.T) {
	hub := newEventHub(4, 2)
	mustPublish(t, hub, "one")
	mustPublish(t, hub, "two")

	subscription, err := hub.Subscribe(1)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer subscription.Close()
	replay := subscription.Replay()
	if len(replay) != 1 || replay[0].ID != 2 {
		t.Fatalf("replay = %#v, want event 2", replay)
	}

	mustPublish(t, hub, "three")
	record, ok := <-subscription.Events()
	if !ok || record.ID != 3 {
		t.Fatalf("live record = %#v, %v; want event 3", record, ok)
	}
}

func TestEventHubRejectsExpiredCursor(t *testing.T) {
	hub := newEventHub(2, 1)
	mustPublish(t, hub, "one")
	mustPublish(t, hub, "two")
	mustPublish(t, hub, "three")

	_, err := hub.Subscribe(0)
	if !errors.Is(err, appbackend.ErrCursorExpired) {
		t.Fatalf("Subscribe error = %v, want ErrCursorExpired", err)
	}
}

func TestEventHubDropsWholeSlowSubscriber(t *testing.T) {
	hub := newEventHub(4, 1)
	subscription, err := hub.Subscribe(0)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer subscription.Close()

	mustPublish(t, hub, "one")
	mustPublish(t, hub, "two")
	if record, ok := <-subscription.Events(); !ok || record.ID != 1 {
		t.Fatalf("buffered record = %#v, %v; want event 1", record, ok)
	}
	if _, ok := <-subscription.Events(); ok {
		t.Fatal("slow subscriber remained open")
	}
}

func mustPublish(t *testing.T, hub *eventHub, eventType string) {
	t.Helper()
	if err := hub.Publish(appbackend.Event{Type: eventType}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
}
