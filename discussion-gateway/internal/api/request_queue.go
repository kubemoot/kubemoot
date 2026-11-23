package api

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/kubemoot/kubemoot/discussion-gateway/internal/crewscope"
)

// requestStream is the work queue that holds a crew's questions until its coordinator takes them.
const requestStream = "KUBEMOOT_REQUEST"

// pendingQuestion is a question in the crew's request queue.
type pendingQuestion struct {
	seq uint64
	req bus.DiscussionRequest
	// taken is true once the crew's coordinator pulled the question and has not acknowledged it:
	// the crew is working on it, or about to begin.
	taken bool
	// ahead is how many questions of the crew come before this one in the queue.
	ahead int
}

// requestedAt is when the gateway queued the question; the zero time when it names none.
func (q pendingQuestion) requestedAt() time.Time {
	at, _ := time.Parse(time.RFC3339, q.req.Timestamp)
	return at
}

// requestConsumer is the durable consumer of the crew's coordinator on the request stream, as
// agent-runtime's CrewScope.requestConsumer names it.
func requestConsumer(scope crewscope.Scope) string {
	return "request-" + scope.Namespace + "-" + scope.Crew
}

// readRequestQueue reads the crew's questions the request stream holds, oldest first. A question
// is taken when it is at or before the last one the coordinator's consumer delivered: it stays in
// the stream until the coordinator acknowledges it, after the answer. A missing stream or
// consumer means nothing is queued or nothing taken.
func readRequestQueue(ctx context.Context, js jetstream.JetStream, scope crewscope.Scope) ([]pendingQuestion, error) {
	stream, err := js.Stream(ctx, requestStream)
	if errors.Is(err, jetstream.ErrStreamNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	subject := scope.RequestSubject()
	info, err := stream.Info(ctx, jetstream.WithSubjectFilter(subject))
	if err != nil {
		return nil, err
	}
	want := int(info.State.Subjects[subject])
	if want == 0 {
		return nil, nil
	}
	delivered, err := deliveredThrough(ctx, stream, requestConsumer(scope))
	if err != nil {
		return nil, err
	}
	return readQueuedMessages(ctx, stream, subject, want, delivered)
}

// deliveredThrough is the stream sequence of the last message the consumer delivered; 0 when it
// does not exist, so nothing is taken.
func deliveredThrough(ctx context.Context, stream jetstream.Stream, name string) (uint64, error) {
	consumer, err := stream.Consumer(ctx, name)
	if errors.Is(err, jetstream.ErrConsumerNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return consumer.CachedInfo().Delivered.Stream, nil
}

// readQueuedMessages reads up to want messages on subject, oldest first, marking those the
// consumer delivered.
func readQueuedMessages(
	ctx context.Context, stream jetstream.Stream, subject string, want int, delivered uint64,
) ([]pendingQuestion, error) {
	var out []pendingQuestion
	for next := uint64(1); len(out) < want; {
		raw, err := stream.GetMsg(ctx, next, jetstream.WithGetMsgSubject(subject))
		if errors.Is(err, jetstream.ErrMsgNotFound) {
			break
		}
		if err != nil {
			return nil, err
		}
		next = raw.Sequence + 1
		var req bus.DiscussionRequest
		if json.Unmarshal(raw.Data, &req) != nil {
			continue
		}
		out = append(out, pendingQuestion{seq: raw.Sequence, req: req, taken: raw.Sequence <= delivered, ahead: len(out)})
	}
	return out, nil
}

// unstarted are the conversation's questions whose discussion has not begun. A question no
// coordinator has taken has no thread. The one question a coordinator has taken (it takes one at a
// time) has begun once a thread of the conversation started at or after the time it was queued;
// until then, and again between a thread's close and the acknowledgement of its question, only the
// thread's start time tells the two apart.
func unstarted(queue []pendingQuestion, conversation string, threads []conversationThread) []pendingQuestion {
	var out []pendingQuestion
	for _, q := range queue {
		if q.req.ConversationID == conversation && (!q.taken || !startedSince(threads, q.requestedAt())) {
			out = append(out, q)
		}
	}
	return out
}

// startedSince reports whether any thread started at or after at.
func startedSince(threads []conversationThread, at time.Time) bool {
	for _, t := range threads {
		if !t.startedAt.Before(at) {
			return true
		}
	}
	return false
}
