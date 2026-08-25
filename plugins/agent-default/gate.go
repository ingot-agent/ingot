package agentdefault

import (
	"context"
	"sync"
)

type gateWaiter struct {
	ready   chan struct{}
	granted bool
}

type sessionGate struct {
	active  bool
	waiters []*gateWaiter
}

type gateManager struct {
	mu      sync.Mutex
	entries map[string]*sessionGate
}

func newGateManager() *gateManager {
	return &gateManager{entries: make(map[string]*sessionGate)}
}

func (m *gateManager) acquire(ctx context.Context, key string) (func(), error) {
	waiter := &gateWaiter{ready: make(chan struct{})}
	m.mu.Lock()
	gate := m.entries[key]
	if gate == nil {
		gate = &sessionGate{}
		m.entries[key] = gate
	}
	if !gate.active {
		gate.active = true
		waiter.granted = true
		close(waiter.ready)
	} else {
		gate.waiters = append(gate.waiters, waiter)
	}
	m.mu.Unlock()

	select {
	case <-waiter.ready:
		release := m.releaseFunc(key, gate)
		if err := ctx.Err(); err != nil {
			release()
			return nil, err
		}
		return release, nil
	case <-ctx.Done():
		m.mu.Lock()
		if !waiter.granted {
			for i, candidate := range gate.waiters {
				if candidate == waiter {
					gate.waiters = append(gate.waiters[:i], gate.waiters[i+1:]...)
					break
				}
			}
			if !gate.active && len(gate.waiters) == 0 && m.entries[key] == gate {
				delete(m.entries, key)
			}
			m.mu.Unlock()
			return nil, ctx.Err()
		}
		m.mu.Unlock()
		release := m.releaseFunc(key, gate)
		release()
		return nil, ctx.Err()
	}
}

func (m *gateManager) releaseFunc(key string, gate *sessionGate) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			if len(gate.waiters) == 0 {
				gate.active = false
				if m.entries[key] == gate {
					delete(m.entries, key)
				}
				m.mu.Unlock()
				return
			}
			next := gate.waiters[0]
			gate.waiters = gate.waiters[1:]
			next.granted = true
			close(next.ready)
			m.mu.Unlock()
		})
	}
}
