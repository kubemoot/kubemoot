package bus

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
)

// delivery is one bus message and what can be said back about it. A JetStream delivery has to be
// acknowledged or its consumer delivers it again after AckWait; a core NATS delivery has no ack.
type delivery struct {
	subject string
	data    []byte
	hdr     nats.Header
	msg     *nats.Msg // the JetStream message; nil on core NATS
}

// inProgress tells JetStream the message is still being worked, which restarts its AckWait.
func (d delivery) inProgress() bool {
	return d.msg != nil && d.msg.InProgress() == nil
}

// ack settles the message.
func (d delivery) ack() {
	if d.msg != nil {
		_ = d.msg.Ack()
	}
}

// term settles a message that can never be processed, so it is not delivered again.
func (d delivery) term() {
	if d.msg != nil {
		_ = d.msg.Term()
	}
}

// nak asks for the message again after a delay, for a turn whose signal could not be published.
func (d delivery) nak(delay time.Duration) {
	if d.msg != nil {
		_ = d.msg.NakWithDelay(delay)
	}
}

// channelOf is the channel token of kubemoot.discuss.<ns>.<crew>.<channel>.<thread>, or empty.
func channelOf(subject string) string {
	parts := strings.SplitN(subject, ".", 6)
	if len(parts) < 6 {
		return ""
	}
	return parts[4]
}

// Subscribe starts delivering the crew's discussion messages to handle. With a consumer in the
// contract it binds the operator's durable consumer by name, with explicit acknowledgement; the
// sidecar never creates a consumer of its own. Without one it subscribes on core NATS.
func (r *Runner) Subscribe(ctx context.Context) (*nats.Subscription, error) {
	r.setup()
	r.base = ctx
	return r.bind()
}

// bind makes one subscription; the context the turns live in is already set.
func (r *Runner) bind() (*nats.Subscription, error) {
	wildcard := r.ID.DiscussWildcard()
	if r.Bus.Consumer == "" {
		return r.Conn.Subscribe(wildcard, func(m *nats.Msg) {
			r.handle(delivery{subject: m.Subject, data: m.Data, hdr: m.Header})
		})
	}
	if r.JS == nil {
		return nil, fmt.Errorf("consumer %q needs JetStream", r.Bus.Consumer)
	}
	return r.JS.Subscribe(wildcard, func(m *nats.Msg) {
		r.handle(delivery{subject: m.Subject, data: m.Data, hdr: m.Header, msg: m})
	}, nats.Bind(r.Bus.Stream, r.Bus.Consumer), nats.ManualAck())
}
