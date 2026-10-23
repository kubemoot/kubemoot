package bus

import (
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
