package bus

import (
	"context"
	"log/slog"
	"time"

	kbus "github.com/kubemoot/kubemoot/operator/pkg/bus"
)

// Lifecycle announces the pod to the crew and keeps its liveness entry fresh. It says waking as
// soon as the sidecar is up, ready each time readiness becomes true (NATS connected, the harness
// answering), and writes the heartbeat entry on an interval. Readiness is read, never timed: the
// poll only observes it.
type Lifecycle struct {
	Pub *Publisher
	// Ready reports readiness; NATS and Harness are the two facts it is made of, for the heartbeat.
	Ready, NATS, Harness func() bool
	// LastInference reports when the last model call finished; the zero time means none yet.
	LastInference func() time.Time
	// Poll is how often readiness is observed; Beat is the heartbeat interval.
	Poll, Beat time.Duration
	Log        *slog.Logger
}

// Run blocks until ctx ends.
func (l *Lifecycle) Run(ctx context.Context) {
	if l.Log == nil {
		l.Log = slog.Default()
	}
	l.announce(ctx, kbus.DiscussionMessageMessageTypeWaking)
	if l.Poll <= 0 {
		l.Poll = 500 * time.Millisecond
	}
	if l.Beat <= 0 {
		l.Beat = 30 * time.Second
	}
	poll := time.NewTicker(l.Poll)
	beat := time.NewTicker(l.Beat)
	defer poll.Stop()
	defer beat.Stop()
	wasReady := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
			now := l.Ready()
			if now && !wasReady {
				l.announce(ctx, kbus.DiscussionMessageMessageTypeReady)
			}
			wasReady = now
		case <-beat.C:
			l.heartbeat()
		}
	}
}

func (l *Lifecycle) announce(ctx context.Context, signal kbus.DiscussionMessageMessageType) {
	if err := l.Pub.Lifecycle(ctx, signal); err != nil {
		l.Log.Warn("lifecycle signal not published", "signal", signal, "error", err)
	}
}

func (l *Lifecycle) heartbeat() {
	h := Heartbeat{NATS: l.NATS(), HarnessReady: l.Harness(), Interval: l.Beat}
	if l.LastInference != nil {
		h.LastInference = l.LastInference()
	}
	if err := l.Pub.WriteHeartbeat(h); err != nil {
		l.Log.Warn("heartbeat not written", "error", err)
	}
}
