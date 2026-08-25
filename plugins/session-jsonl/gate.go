package sessionjsonl

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
	case <-ctx.Done():
		m.releaseReference(key, entry)
		return nil, ctx.Err()
	case <-entry.token:
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			entry.token <- struct{}{}
			m.releaseReference(key, entry)
		})
	}, nil
}

func (m *gateManager) releaseReference(key string, entry *gateEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry.refs--
	if entry.refs == 0 && m.entries[key] == entry {
		delete(m.entries, key)
	}
}
