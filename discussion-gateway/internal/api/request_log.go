package api

import (
	"sync"
	"time"
)

// clockSkew allows for the gateway's clock and the NATS server's disagreeing slightly
// when a thread's start is compared with the time its request was queued.
const clockSkew = 2 * time.Second

// requestLog remembers when this gateway last queued a request for each conversation,
// so a stream opened for that turn follows the thread the request started rather than
// an earlier turn's thread that is still inside the stream's look-back window.
type requestLog struct {
	mu     sync.Mutex
	queued map[string]time.Time
	ttl    time.Duration
	now    func() time.Time
}

func newRequestLog(ttl time.Duration) *requestLog {
	return &requestLog{queued: map[string]time.Time{}, ttl: ttl, now: time.Now}
}

// record notes that a request for the conversation was queued now, returns that time,
// and forgets conversations older than the log's time to live.
func (l *requestLog) record(conversationID string) time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for id, t := range l.queued {
		if now.Sub(t) > l.ttl {
			delete(l.queued, id)
		}
	}
	l.queued[conversationID] = now
	return now
}

// notBefore is the earliest time a thread for the conversation's latest request can
// have started, or the zero time when this gateway did not queue it.
func (l *requestLog) notBefore(conversationID string) time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	t, ok := l.queued[conversationID]
	if !ok {
		return time.Time{}
	}
	return t.Add(-clockSkew)
}
