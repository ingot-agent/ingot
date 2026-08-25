package contextcompact

import (
	"context"
	"sync"
)

type gateEntry struct {
	token chan struct{}
	refs  int
}

type gateManager struct {
	mu      sync.Mutex
	entries map[string]*gateEntry
}

func newGateManager() *gateManager {
	return &gateManager{entries: make(map[string]*gateEntry)}
}

func (m *gateManager) acquire(ctx context.Context, key string) (func(), error) {
	if ctx == nil {
		return nil, context.Canceled
	}
	m.mu.Lock()
	entry := m.entries[key]
	if entry == nil {
		entry = &gateEntry{token: make(chan struct{}, 1)}
		entry.token <- struct{}{}
		m.entries[key] = entry
	}
	entry.refs++
	m.mu.Unlock()

	select {
	case <-entry.token:
		if err := ctx.Err(); err != nil {
			m.releaseRef(key, entry, true)
			return nil, err
		}
		var once sync.Once
		return func() { once.Do(func() { m.releaseRef(key, entry, true) }) }, nil
	case <-ctx.Done():
		m.releaseRef(key, entry, false)
		return nil, ctx.Err()
	}
}

func (m *gateManager) releaseRef(key string, entry *gateEntry, held bool) {
	if held {
		entry.token <- struct{}{}
	}
	m.mu.Lock()
	entry.refs--
	if entry.refs == 0 && m.entries[key] == entry {
		delete(m.entries, key)
	}
	m.mu.Unlock()
}
