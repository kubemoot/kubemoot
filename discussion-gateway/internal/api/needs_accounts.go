package api

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/kubemoot/kubemoot/discussion-gateway/internal/crewscope"
)

// When a tool call of a running turn is refused because the asking person has not connected an
// account (or must connect it again), the gateway notes the account name on the conversation, and
// the conversation's state shows it on the turn that was running then. The note is only account
// names and times, never a token or a person; a client that can reach the person offers the way
// to connect, so the crew's own answer text never has to carry a link.

// maxNoteTries bounds the compare-and-swap loop of one note.
const maxNoteTries = 3

// maxNotedAccounts bounds the names one conversation's note keeps. A crew declares a few
// accounts, so a note that grows past this holds names nobody declared; the oldest go first.
const maxNotedAccounts = 16

// AccountNoter records on a conversation that a tool call was refused for an account.
type AccountNoter interface {
	NeedsAccount(ctx context.Context, conversationID, account string) error
}

// NeedsAccount implements AccountNoter over the conversations bucket.
func (o handlerOwners) NeedsAccount(ctx context.Context, conversationID, account string) error {
	js, err := o.h.natsClient.JetStream()
	if err != nil {
		return err
	}
	kv, err := js.KeyValue(ctx, bus.ConversationsBucket)
	if err != nil {
		return err
	}
	key := bus.ConversationNeedsKey(o.scope.Namespace, o.scope.Crew, conversationID)
	for range maxNoteTries {
		retry, err := noteAccount(ctx, kv, key, account, time.Now())
		if !retry {
			return err
		}
	}
	return errors.New("the note of a refused account kept losing a race")
}

// noteAccount adds the account to the record under key. retry is true when another writer won.
func noteAccount(
	ctx context.Context, kv jetstream.KeyValue, key, account string, now time.Time,
) (retry bool, err error) {
	var cur bus.NeedsAccounts
	entry, err := kv.Get(ctx, key)
	missing := errors.Is(err, jetstream.ErrKeyNotFound)
	if err != nil && !missing {
		return false, err
	}
	if !missing {
		_ = json.Unmarshal(entry.Value(), &cur)
	}
	if cur.Refused == nil {
		cur.Refused = map[string]string{}
	}
	cur.Refused[account] = now.UTC().Format(time.RFC3339Nano)
	dropOldest(cur.Refused, maxNotedAccounts)
	raw, err := json.Marshal(cur)
	if err != nil {
		return false, err
	}
	if missing {
		_, err = kv.Create(ctx, key, raw)
	} else {
		_, err = kv.Update(ctx, key, raw, entry.Revision())
	}
	return lostRace(err), err
}

// dropOldest removes the entries with the oldest times until at most limit remain. A time that
// cannot be read counts as the oldest.
func dropOldest(refused map[string]string, limit int) {
	for len(refused) > limit {
		oldest, oldestAt := "", time.Time{}
		for name, at := range refused {
			t, _ := time.Parse(time.RFC3339Nano, at)
			if oldest == "" || t.Before(oldestAt) {
				oldest, oldestAt = name, t
			}
		}
		delete(refused, oldest)
	}
}

// lostRace reports whether a create or update failed because another writer got there first.
func lostRace(err error) bool {
	var api *jetstream.APIError
	return errors.Is(err, jetstream.ErrKeyExists) ||
		(errors.As(err, &api) && api.ErrorCode == jetstream.JSErrCodeStreamWrongLastSequence)
}

// withNeededAccounts sets needsAccounts on the conversation's latest turn: the accounts a tool
// call was refused for at or after the turn began. A note is best effort; the state is returned
// without it when it cannot be read.
func (h *Handler) withNeededAccounts(
	ctx context.Context, js jetstream.JetStream, scope crewscope.Scope, id string, state *bus.ConversationState,
) {
	latest := latestStartedTurn(state.Turns)
	if latest < 0 {
		return
	}
	kv, err := js.KeyValue(ctx, bus.ConversationsBucket)
	if err != nil {
		return
	}
	entry, err := kv.Get(ctx, bus.ConversationNeedsKey(scope.Namespace, scope.Crew, id))
	if err != nil {
		if !errors.Is(err, jetstream.ErrKeyNotFound) {
			handlerLog.Info("Could not read the accounts a conversation needs", "crew", scope.Crew)
		}
		return
	}
	state.Turns[latest].NeedsAccounts = accountsSince(entry.Value(), state.Turns[latest].StartedAt)
}

// latestStartedTurn is the index of the last turn that has a thread, or -1. A queued question
// has none and cannot have been refused anything yet.
func latestStartedTurn(turns []bus.TurnStatus) int {
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].ThreadID != "" {
			return i
		}
	}
	return -1
}

// accountsSince lists, sorted, the accounts the record says were refused at or after the turn's
// start. The turn's start has whole-second precision (the stream's message time), so a refusal
// is compared at whole seconds too: one in the same second as the start belongs to the turn. A
// record that cannot be read names none.
func accountsSince(value []byte, startedAt string) []string {
	var rec bus.NeedsAccounts
	started, err := time.Parse(time.RFC3339, startedAt)
	if json.Unmarshal(value, &rec) != nil || err != nil {
		return nil
	}
	var out []string
	for account, at := range rec.Refused {
		if t, err := time.Parse(time.RFC3339Nano, at); err == nil && !t.Truncate(time.Second).Before(started) {
			out = append(out, account)
		}
	}
	sort.Strings(out)
	return out
}
