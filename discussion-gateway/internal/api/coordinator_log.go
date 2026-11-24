package api

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/kubemoot/kubemoot/discussion-gateway/internal/crewscope"
)

// The coordinator's log of a thread is the authority for what the coordinator did with it: it
// records each phase the thread entered (so a pause the coordinator ignored is not in it) and the
// request the thread answers. The gateway reads it for the state of a turn. A crew whose coordinator
// writes no log (coordinator.eventLog: false), or whose log has expired, has no entry here, and
// the state falls back to what the discussion stream shows.

// threadLog is what the coordinator's log says of one thread.
type threadLog struct {
	// known is true when the log holds an event of the thread.
	known bool
	// requestSeq is the stream sequence of the queued request the thread answers; 0 when the thread
	// began another way or the log does not say.
	requestSeq uint64
	// paused is true while the last phase the thread entered is PAUSED.
	paused bool
}

// apply folds one event of the thread, in log order.
func (l *threadLog) apply(ev bus.CoordinatorEvent) {
	switch ev.Type {
	case bus.CoordinatorEventTypeThreadOpened:
		l.known = true
		if ev.Data.RequestSeq > 0 && l.requestSeq == 0 {
			l.requestSeq = uint64(ev.Data.RequestSeq)
		}
	case bus.CoordinatorEventTypePhaseEntered:
		l.known = true
		l.paused = ev.Data.Phase == bus.CoordinatorEventFieldsPhasePAUSED
	case bus.CoordinatorEventTypeThreadClosed:
		l.known = true
		l.paused = false
	}
}

// readThreadLogs reads the coordinator's log of each thread. A thread the log holds nothing of is
// absent from the result.
func readThreadLogs(
	ctx context.Context, js jetstream.JetStream, scope crewscope.Scope, threads []string,
) (map[string]threadLog, error) {
	out := map[string]threadLog{}
	for _, thread := range threads {
		var l threadLog
		err := readStreamSubject(ctx, js, coordinatorStream, scope.CoordinatorSubject(thread), func(msg jetstream.Msg) {
			var ev bus.CoordinatorEvent
			if json.Unmarshal(msg.Data(), &ev) == nil {
				l.apply(ev)
			}
		})
		if errors.Is(err, jetstream.ErrStreamNotFound) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if l.known {
			out[thread] = l
		}
	}
	return out, nil
}

// applyLogs sets the pause of each open thread from the coordinator's log, where the log has the
// thread: the log records the phase the coordinator entered, the pause message only what was asked.
func (r *threadReducer) applyLogs(logs map[string]threadLog) {
	for id, t := range r.byID {
		if l, ok := logs[id]; ok && t.open() {
			t.paused = l.paused
		}
	}
}
