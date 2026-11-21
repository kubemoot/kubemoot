package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/crewmemory"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/kubemoot/kubemoot/discussion-gateway/internal/crewscope"
)

// The stores a conversation is erased from, as docs/operating/data-ownership.md lists them.
const (
	coordinatorStream = "KUBEMOOT_COORDINATOR"
	scheduledBucket   = "kubemoot_scheduled"
)

// eraser removes one conversation from every store that holds it.
type eraser struct {
	js     jetstream.JetStream
	scope  crewscope.Scope
	id     string
	report erasure
}

func newEraser(js jetstream.JetStream, scope crewscope.Scope, id string) *eraser {
	e := &eraser{js: js, scope: scope, id: id}
	e.report.ConversationID = id
	e.report.Kept = kept
	return e
}

// erase removes the conversation, whose threads in the discussion stream are known, from each
// store, and reports what it removed. A store that cannot be cleared is reported in Failed and
// the rest are still cleared; the delete is repeatable. The history and the owner record go
// last: they name the threads and the owner, so a delete that stopped early can be repeated.
func (e *eraser) erase(ctx context.Context, streamThreads []string) erasure {
	history, historyThreads, err := e.readHistory(ctx)
	e.fail("history", err)
	threads := validThreads(append(append([]string{}, streamThreads...), historyThreads...))
	e.report.Threads = threads

	e.discussion(ctx, threads)
	e.coordinatorLog(ctx, threads)
	e.workspace(ctx, threads)
	e.scheduled(ctx, threads)
	e.memory(ctx, threads)
	// The history and the owner record name what is left to clear, so they stay until every
	// other store is clear: a delete that failed can be repeated by the owner.
	if len(e.report.Failed) > 0 {
		return e.report
	}
	if history {
		e.purge(ctx, bus.ConversationHistoryKey(e.scope.Namespace, e.scope.Crew, e.id), "history", &e.report.Deleted.History)
	}
	if len(e.report.Failed) == 0 {
		ownerKey := bus.ConversationOwnerKey(e.scope.Namespace, e.scope.Crew, e.id)
		e.purgeIfPresent(ctx, ownerKey, "owner", &e.report.Deleted.Owner)
	}
	return e.report
}

// fail records a store that could not be cleared.
func (e *eraser) fail(store string, err error) {
	if err != nil {
		handlerLog.Error(err, "Could not clear a store of a deleted conversation", "crew", e.scope.Crew, "store", store)
		e.report.Failed = append(e.report.Failed, store)
	}
}

// bucket opens a KV bucket, and is nil when the bucket does not exist.
func (e *eraser) bucket(ctx context.Context, name string) (jetstream.KeyValue, error) {
	kv, err := e.js.KeyValue(ctx, name)
	if errors.Is(err, jetstream.ErrBucketNotFound) {
		return nil, nil
	}
	return kv, err
}

// readHistory reports whether the conversation has a history and the threads of its turns.
func (e *eraser) readHistory(ctx context.Context) (held bool, threads []string, err error) {
	kv, err := e.bucket(ctx, bus.ConversationsBucket)
	if kv == nil || err != nil {
		return false, nil, err
	}
	entry, err := kv.Get(ctx, bus.ConversationHistoryKey(e.scope.Namespace, e.scope.Crew, e.id))
	if errors.Is(err, jetstream.ErrKeyNotFound) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	var history bus.ConversationHistory
	if json.Unmarshal(entry.Value(), &history) == nil {
		for _, t := range history.Turns {
			threads = append(threads, t.ThreadID)
		}
	}
	return true, threads, nil
}

// purge removes every revision of a key in the conversations bucket, counting one.
func (e *eraser) purge(ctx context.Context, key, store string, count *int64) {
	kv, err := e.bucket(ctx, bus.ConversationsBucket)
	if kv == nil || err != nil {
		e.fail(store, err)
		return
	}
	if err := kv.Purge(ctx, key); err != nil {
		e.fail(store, err)
		return
	}
	*count++
}

// purgeIfPresent is purge for a key that may not exist.
func (e *eraser) purgeIfPresent(ctx context.Context, key, store string, count *int64) {
	kv, err := e.bucket(ctx, bus.ConversationsBucket)
	if kv == nil || err != nil {
		e.fail(store, err)
		return
	}
	if _, err := kv.Get(ctx, key); errors.Is(err, jetstream.ErrKeyNotFound) {
		return
	} else if err != nil {
		e.fail(store, err)
		return
	}
	e.purge(ctx, key, store, count)
}

// purgeSubjects removes from a stream every message on the subjects, and counts them. A stream
// that does not exist holds none.
func (e *eraser) purgeSubjects(ctx context.Context, stream string, subjects []string) (int64, error) {
	s, err := e.js.Stream(ctx, stream)
	if errors.Is(err, jetstream.ErrStreamNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var removed int64
	for _, subject := range subjects {
		info, err := s.Info(ctx, jetstream.WithSubjectFilter(subject))
		if err != nil {
			return removed, err
		}
		var n int64
		for _, count := range info.State.Subjects {
			n += int64(count)
		}
		if n == 0 {
			continue
		}
		if err := s.Purge(ctx, jetstream.WithPurgeSubject(subject)); err != nil {
			return removed, err
		}
		removed += n
	}
	return removed, nil
}

// discussion removes the threads' messages from the discussion stream.
func (e *eraser) discussion(ctx context.Context, threads []string) {
	subjects := make([]string, 0, len(threads))
	for _, t := range threads {
		subjects = append(subjects, e.scope.DiscussSubject("*", t))
	}
	n, err := e.purgeSubjects(ctx, streamName, subjects)
	e.report.Deleted.DiscussionMessages += n
	e.fail("discussion stream", err)
}

// coordinatorLog removes the threads' coordinator decision logs.
func (e *eraser) coordinatorLog(ctx context.Context, threads []string) {
	subjects := make([]string, 0, len(threads))
	for _, t := range threads {
		subjects = append(subjects, e.scope.CoordinatorSubject(t))
	}
	n, err := e.purgeSubjects(ctx, coordinatorStream, subjects)
	e.report.Deleted.CoordinatorEvents += n
	e.fail("coordinator log", err)
}

// workspace removes the threads' objects, <namespace>/<crew>/<thread>/<agent>/<object>, from
// both workspace buckets.
func (e *eraser) workspace(ctx context.Context, threads []string) {
	prefixes := make([]string, 0, len(threads))
	for _, t := range threads {
		prefixes = append(prefixes, e.scope.Namespace+"/"+e.scope.Crew+"/"+t+"/")
	}
	for _, bucket := range readBuckets {
		n, err := e.clearObjects(ctx, bucket, prefixes)
		e.report.Deleted.WorkspaceObjects += n
		e.fail("workspace "+bucket, err)
	}
}

// clearObjects deletes the objects of a bucket whose names start with a prefix.
func (e *eraser) clearObjects(ctx context.Context, bucket string, prefixes []string) (int64, error) {
	store, err := e.js.ObjectStore(ctx, bucket)
	if errors.Is(err, jetstream.ErrBucketNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	infos, err := store.List(ctx)
	if err != nil && !errors.Is(err, jetstream.ErrNoObjectsFound) {
		return 0, err
	}
	var removed int64
	for _, info := range infos {
		if info.Deleted || !hasAnyPrefix(info.Name, prefixes) {
			continue
		}
		if err := store.Delete(ctx, info.Name); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

func hasAnyPrefix(name string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// scheduled removes the reminders and follow-ups scheduled from the threads, which would
// otherwise fire into a conversation that no longer exists.
func (e *eraser) scheduled(ctx context.Context, threads []string) {
	n, err := e.purgeMatching(ctx, scheduledBucket, ">", func(value []byte) bool {
		var rec bus.ScheduledRecord
		return json.Unmarshal(value, &rec) == nil && rec.Namespace == e.scope.Namespace &&
			rec.Crew == e.scope.Crew && contains(threads, rec.SourceThreadID)
	})
	e.report.Deleted.Scheduled += n
	e.fail("scheduled", err)
}

// memory removes the crew memory facts learned in the threads.
func (e *eraser) memory(ctx context.Context, threads []string) {
	n, err := e.purgeMatching(ctx, crewmemory.Bucket, e.scope.Namespace+"."+e.scope.Crew+".>", func(value []byte) bool {
		var fact bus.CrewMemoryFact
		return json.Unmarshal(value, &fact) == nil && contains(threads, fact.LearnedInThread)
	})
	e.report.Deleted.MemoryFacts += n
	e.fail("crew memory", err)
}

func contains(values []string, v string) bool {
	if v == "" {
		return false
	}
	for _, have := range values {
		if have == v {
			return true
		}
	}
	return false
}

// purgeMatching purges the keys of a bucket matching filter whose value matches, and counts them.
func (e *eraser) purgeMatching(
	ctx context.Context, bucket, filter string, matches func(value []byte) bool,
) (int64, error) {
	kv, err := e.bucket(ctx, bucket)
	if kv == nil || err != nil {
		return 0, err
	}
	lister, err := kv.ListKeysFiltered(ctx, filter)
	if err != nil {
		return 0, err
	}
	var removed int64
	for key := range lister.Keys() {
		entry, err := kv.Get(ctx, key)
		if errors.Is(err, jetstream.ErrKeyNotFound) {
			continue
		}
		if err != nil {
			return removed, err
		}
		if !matches(entry.Value()) {
			continue
		}
		if err := kv.Purge(ctx, key); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}
