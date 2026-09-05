package hostcomponent

import (
	"encoding/json"
	"sync"

	appbackend "github.com/ingot-agent/app-webui"
)

type eventHub struct {
	mu               sync.Mutex
	cursor           uint64
	capacity         int
	subscriberBuffer int
	replay           []appbackend.EventRecord
	nextSubscriber   uint64
	subscribers      map[uint64]chan appbackend.EventRecord
}

func newEventHub(capacity, subscriberBuffer int) *eventHub {
	return &eventHub{
		capacity:         capacity,
		subscriberBuffer: subscriberBuffer,
		replay:           make([]appbackend.EventRecord, 0, capacity),
		subscribers:      make(map[uint64]chan appbackend.EventRecord),
	}
}

func (h *eventHub) Publish(event appbackend.Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	h.mu.Lock()
	h.cursor++
	record := appbackend.EventRecord{ID: h.cursor, Data: append([]byte(nil), data...)}
	if len(h.replay) == h.capacity {
		copy(h.replay, h.replay[1:])
		h.replay[len(h.replay)-1] = record
	} else {
		h.replay = append(h.replay, record)
	}
	for id, subscriber := range h.subscribers {
		select {
		case subscriber <- cloneRecord(record):
		default:
			delete(h.subscribers, id)
			close(subscriber)
		}
	}
	h.mu.Unlock()
	return nil
}

func (h *eventHub) Cursor() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cursor
}

func (h *eventHub) Subscribe(after uint64) (appbackend.Subscription, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if after > h.cursor {
		return nil, appbackend.ErrCursorAhead
	}
	if len(h.replay) > 0 {
		oldest := h.replay[0].ID
		if oldest > 1 && after < oldest-1 {
			return nil, appbackend.ErrCursorExpired
		}
	}
	replay := make([]appbackend.EventRecord, 0, len(h.replay))
	for _, record := range h.replay {
		if record.ID > after {
			replay = append(replay, cloneRecord(record))
		}
	}
	h.nextSubscriber++
	id := h.nextSubscriber
	live := make(chan appbackend.EventRecord, h.subscriberBuffer)
	h.subscribers[id] = live
	return &subscription{hub: h, id: id, replay: replay, live: live}, nil
}

func (h *eventHub) unsubscribe(id uint64) {
	h.mu.Lock()
	if subscriber, ok := h.subscribers[id]; ok {
		delete(h.subscribers, id)
		close(subscriber)
	}
	h.mu.Unlock()
}

type subscription struct {
	once   sync.Once
	hub    *eventHub
	id     uint64
	replay []appbackend.EventRecord
	live   chan appbackend.EventRecord
}

func (s *subscription) Replay() []appbackend.EventRecord {
	result := make([]appbackend.EventRecord, len(s.replay))
	for i, record := range s.replay {
		result[i] = cloneRecord(record)
	}
	return result
}

func (s *subscription) Events() <-chan appbackend.EventRecord { return s.live }

func (s *subscription) Close() { s.once.Do(func() { s.hub.unsubscribe(s.id) }) }

func cloneRecord(record appbackend.EventRecord) appbackend.EventRecord {
	return appbackend.EventRecord{ID: record.ID, Data: append([]byte(nil), record.Data...)}
}

var _ appbackend.EventHub = (*eventHub)(nil)
