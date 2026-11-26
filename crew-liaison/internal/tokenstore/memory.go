package tokenstore

import (
	"context"
	"sort"
	"sync"
	"time"
)

// Memory is a Backend in the process's own memory: nothing to install and nothing shared. A
// restart forgets every value and two replicas do not see each other's, so it fits tests
// and one replica without NATS.
type Memory struct {
	mu     sync.Mutex
	now    func() time.Time
	seq    uint64
	values map[string]memoryValue
}

type memoryValue struct {
	value    []byte
	revision uint64
	expires  time.Time
}

// NewMemory returns an empty backend. now is the clock; nil is time.Now.
func NewMemory(now func() time.Time) *Memory {
	if now == nil {
		now = time.Now
	}
	return &Memory{now: now, values: map[string]memoryValue{}}
}

func memKey(group, name string) string { return group + "/" + name }

// live returns the unexpired value, dropping an expired one. The caller holds the lock.
func (m *Memory) live(key string) (memoryValue, bool) {
	v, ok := m.values[key]
	if ok && !m.now().Before(v.expires) {
		delete(m.values, key)
		return memoryValue{}, false
	}
	return v, ok
}

// Get implements Backend.
func (m *Memory) Get(_ context.Context, group, name string) (Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.live(memKey(group, name))
	if !ok {
		return Entry{}, ErrNotFound
	}
	return Entry{Value: append([]byte(nil), v.value...), Revision: v.revision}, nil
}

// Put implements Backend.
func (m *Memory) Put(
	_ context.Context, group, name string, value []byte, ttl time.Duration, ifRevision uint64,
) (uint64, error) {
	if ttl <= 0 {
		return 0, ErrBadTTL
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := memKey(group, name)
	cur, ok := m.live(key)
	if !revisionMatches(ifRevision, cur.revision, ok) {
		return 0, ErrConflict
	}
	m.seq++
	m.values[key] = memoryValue{value: append([]byte(nil), value...), revision: m.seq, expires: m.now().Add(ttl)}
	return m.seq, nil
}

// revisionMatches applies the Put rules: AnyRevision always matches, MustNotExist needs no
// value, otherwise the revision must be the current one.
func revisionMatches(want, current uint64, exists bool) bool {
	switch want {
	case AnyRevision:
		return true
	case MustNotExist:
		return !exists
	default:
		return exists && want == current
	}
}

// Delete implements Backend.
func (m *Memory) Delete(_ context.Context, group, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.values, memKey(group, name))
	return nil
}

// Names implements Backend.
func (m *Memory) Names(_ context.Context, group string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for key := range m.values {
		g, n := splitMemKey(key)
		if g != group {
			continue
		}
		if _, ok := m.live(key); ok {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out, nil
}

func splitMemKey(key string) (group, name string) {
	for i := 0; i < len(key); i++ {
		if key[i] == '/' {
			return key[:i], key[i+1:]
		}
	}
	return key, ""
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
