package tokenstore

import (
	"context"
	"sync"
	"time"
)

// Memory is a Store in the process's own memory: nothing to install, nothing shared. A
// restart forgets every value, and two liaison replicas do not see each other's, so it fits
// one replica and tests.
type Memory struct {
	mu     sync.Mutex
	now    func() time.Time
	values map[string]memoryValue
}

type memoryValue struct {
	value   []byte
	expires time.Time
}

// NewMemory returns an empty in-memory store. now is the clock; nil is time.Now.
func NewMemory(now func() time.Time) *Memory {
	if now == nil {
		now = time.Now
	}
	return &Memory{now: now, values: map[string]memoryValue{}}
}

// Put implements Store.
func (m *Memory) Put(_ context.Context, key string, value []byte, ttl time.Duration) error {
	if err := check(key, ttl); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.values[key] = memoryValue{value: append([]byte(nil), value...), expires: m.now().Add(ttl)}
	return nil
}

// Create implements Store.
func (m *Memory) Create(_ context.Context, key string, value []byte, ttl time.Duration) error {
	if err := check(key, ttl); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.live(key); ok {
		return ErrExists
	}
	m.values[key] = memoryValue{value: append([]byte(nil), value...), expires: m.now().Add(ttl)}
	return nil
}

// Get implements Store.
func (m *Memory) Get(_ context.Context, key string) ([]byte, error) {
	if err := CheckKey(key); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.live(key)
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), v.value...), nil
}

// Delete implements Store.
func (m *Memory) Delete(_ context.Context, key string) error {
	if err := CheckKey(key); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.values, key)
	return nil
}

// live returns the value under key when it has not expired, dropping it when it has. The
// caller holds the lock.
func (m *Memory) live(key string) (memoryValue, bool) {
	v, ok := m.values[key]
	if ok && !m.now().Before(v.expires) {
		delete(m.values, key)
		return memoryValue{}, false
	}
	return v, ok
}

// Sweep drops every expired value, so values nobody reads again do not stay in memory.
func (m *Memory) Sweep() {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	for key, v := range m.values {
		if !now.Before(v.expires) {
			delete(m.values, key)
		}
	}
}
