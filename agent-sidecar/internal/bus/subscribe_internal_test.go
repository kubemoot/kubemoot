package bus

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

func TestTheWaitDoublesFromTheFirstToTheCap(t *testing.T) {
	want := []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 5 * time.Second, 5 * time.Second}
	d := retryFirst
	for i, w := range want {
		if d = nextWait(d); d != w {
			t.Fatalf("step %d: %v, want %v", i, d, w)
		}
	}
}

func TestOnlyAnUnavailableConsumerIsWaitedFor(t *testing.T) {
	waits := []error{errors.New("nats: consumer is already bound to a subscription"), nats.ErrConsumerNotFound,
		nats.ErrTimeout, nats.ErrNoResponders, nats.ErrDisconnected, nats.ErrConnectionReconnecting,
		fmt.Errorf("wrapped: %w", nats.ErrTimeout)}
	for _, err := range waits {
		if !isWaitable(err) {
			t.Errorf("%v should be waited for", err)
		}
	}
	fatal := []error{nats.ErrStreamNotFound, nats.ErrPermissionViolation, nats.ErrConnectionClosed,
		errors.New(`consumer "x" needs JetStream`), errors.New("nats: invalid consumer name")}
	for _, err := range fatal {
		if isWaitable(err) {
			t.Errorf("%v should end the wait", err)
		}
	}
}

// Only the consumer being gone, or made again with another delivery subject, is a loss; a failure
// to ask is not.
func TestOnlyAMovedConsumerIsALostSubscription(t *testing.T) {
	same := &nats.ConsumerInfo{Config: nats.ConsumerConfig{DeliverSubject: "_INBOX.a"}}
	again := &nats.ConsumerInfo{Config: nats.ConsumerConfig{DeliverSubject: "_INBOX.b"}}
	cases := []struct {
		name string
		ci   *nats.ConsumerInfo
		err  error
		lost bool
	}{
		{"unchanged", same, nil, false},
		{"made again", again, nil, true},
		{"gone", nil, nats.ErrConsumerNotFound, true},
		{"timeout", nil, nats.ErrTimeout, false},
		{"no responders", nil, nats.ErrNoResponders, false},
		{"deadline", nil, context.DeadlineExceeded, false},
		{"canceled", nil, context.Canceled, false},
	}
	for _, c := range cases {
		if got := movedOn(c.ci, c.err, "_INBOX.a"); got != c.lost {
			t.Errorf("%s: lost=%t, want %t", c.name, got, c.lost)
		}
	}
}
