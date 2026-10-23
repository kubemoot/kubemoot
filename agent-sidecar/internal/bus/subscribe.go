package bus

import (
	"context"
	"errors"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
)

// Timing of the wait for the consumer: the first retry comes quickly and the wait doubles to a cap.
// The warning is repeated on a slow cadence for as long as the sidecar is not subscribed.
const (
	retryFirst       = 250 * time.Millisecond
	retryMax         = 5 * time.Second
	defaultWarnEvery = time.Minute
	watchEvery       = 2 * time.Second
)

// nextWait is the wait after d.
func nextWait(d time.Duration) time.Duration { return min(d*2, retryMax) }

// waitable errors say the consumer is not available yet, which another pod ending or the operator
// creating it will fix. Anything else (no JetStream, a missing stream, a permission the pod lacks)
// is a misconfiguration that waiting does not cure. No-responders is waited for as well, since the
// JetStream API is briefly unanswered during a server restart; the wait is logged every minute.
var waitable = []error{nats.ErrConsumerNotFound, nats.ErrTimeout,
	nats.ErrNoResponders, nats.ErrDisconnected, nats.ErrConnectionReconnecting}

// alreadyBound is the text nats.go gives, without a sentinel, when the consumer has a subscription.
// The tests that bind a second subscriber against a real server pin this text: if a nats.go
// upgrade changes it, they fail instead of the sidecar crash-looping during a rollout.
const alreadyBound = "consumer is already bound to a subscription"

func isWaitable(err error) bool {
	if strings.Contains(err.Error(), alreadyBound) {
		return true
	}
	for _, w := range waitable {
		if errors.Is(err, w) {
			return true
		}
	}
	return false
}

func (r *Runner) warnEvery() time.Duration {
	if r.WarnEvery > 0 {
		return r.WarnEvery
	}
	return defaultWarnEvery
}

// Hold keeps the runner subscribed to the crew's discussion until ctx ends, and tells state when
// it is and is not. The consumer has one subscription at a time, so during a rolling update the pod
// being replaced keeps it and the new pod waits, ready, until it is free. A subscription that is
// lost (the consumer deleted and made again, the subscription closed) is bound again. It returns
// an error only for a misconfiguration that waiting cannot cure.
func (r *Runner) Hold(ctx context.Context, state func(bool)) error {
	r.setup()
	r.base = ctx
	for {
		sub, err := r.awaitBind(ctx)
		if sub == nil {
			return err
		}
		state(true)
		r.watch(ctx, sub)
		_ = sub.Unsubscribe()
		state(false)
		if ctx.Err() != nil {
			return nil
		}
		r.Log.Warn("the subscription to the crew's discussion was lost; binding again", "consumer", r.Bus.Consumer)
	}
}

// awaitBind subscribes, waiting with a growing delay while the consumer is not available. It
// returns nil, nil when ctx ends.
func (r *Runner) awaitBind(ctx context.Context) (*nats.Subscription, error) {
	wait, last, loggedAt := retryFirst, "", time.Time{}
	for {
		sub, err := r.bind()
		if err == nil {
			if last != "" {
				r.Log.Info("subscribed to the crew's discussion", "consumer", r.Bus.Consumer)
			}
			return sub, nil
		}
		if !isWaitable(err) {
			return nil, err
		}
		if err.Error() != last || time.Since(loggedAt) >= r.warnEvery() {
			last, loggedAt = err.Error(), time.Now()
			r.Log.Warn("not subscribed to the crew's discussion yet; waiting", "consumer", r.Bus.Consumer, "error", err)
		}
		select {
		case <-ctx.Done():
			return nil, nil
		case <-time.After(wait):
		}
		wait = nextWait(wait)
	}
}

// watch returns when ctx ends or the subscription is lost: closed, or the consumer it was bound to
// is gone or was made again with another delivery subject. It looks on a jittered beat, so pods
// that started together do not ask the server together.
func (r *Runner) watch(ctx context.Context, sub *nats.Subscription) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(watchEvery + rand.N(watchEvery/4)):
			if !sub.IsValid() || r.consumerMoved(ctx, sub) {
				return
			}
		}
	}
}

// consumerMoved is true when the operator's consumer no longer delivers to sub. The question ends
// with ctx, so a shutdown during a NATS outage is not held up, and a failure to ask is not a loss.
func (r *Runner) consumerMoved(ctx context.Context, sub *nats.Subscription) bool {
	if r.Bus.Consumer == "" || r.JS == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, watchEvery)
	defer cancel()
	ci, err := r.JS.ConsumerInfo(r.Bus.Stream, r.Bus.Consumer, nats.Context(ctx))
	return movedOn(ci, err, sub.Subject)
}

// movedOn decides from the answer about the consumer whether a subscription on subject lost it.
// A consumer that is gone is a loss; so is one made again, which has a new delivery subject (the
// server refuses to change the delivery subject of a consumer that is bound, so a different subject
// always means a different consumer). Any other error, a timeout or no responders, says nothing
// about the subscription.
func movedOn(ci *nats.ConsumerInfo, err error, subject string) bool {
	if err != nil {
		return errors.Is(err, nats.ErrConsumerNotFound)
	}
	return ci.Config.DeliverSubject != subject
}
