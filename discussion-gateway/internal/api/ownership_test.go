package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-logr/logr/funcr"
	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/identity"
	"github.com/nats-io/nats.go/jetstream"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"

	"github.com/kubemoot/kubemoot/discussion-gateway/internal/crewscope"
	natsclient "github.com/kubemoot/kubemoot/discussion-gateway/internal/nats"
)

const (
	convA        = "conv-alice"
	reasonHeader = "Kubemoot-Access-Reason"
	listPath     = "/api/v1/discussions/pilot"
)

// ownedSetup is a gateway that verifies callers, over a NATS with the conversations bucket
// and the workspace store.
type ownedSetup struct {
	srv *httptest.Server
	js  jetstream.JetStream
	kv  jetstream.KeyValue
}

func newOwnedSetup(t *testing.T, allowAnonymous bool) ownedSetup {
	t.Helper()
	nc, js := jetStreamServer(t)
	return newOwnedSetupOn(t, nc, js, allowAnonymous)
}

func newOwnedSetupOn(t *testing.T, nc *natsclient.Client, js jetstream.JetStream, allowAnonymous bool) ownedSetup {
	t.Helper()
	ctx := context.Background()
	createConversationsBucket(t, js)
	kv, err := js.KeyValue(ctx, bus.ConversationsBucket)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := js.CreateObjectStore(ctx, jetstream.ObjectStoreConfig{Bucket: workspaceBucket}); err != nil {
		t.Fatal(err)
	}
	return ownedSetup{srv: enforcingServer(t, nc, allowAnonymous), js: js, kv: kv}
}

func (o ownedSetup) ask(t *testing.T, token, conversation string) int {
	t.Helper()
	body := `{"message":"hi","conversationId":"` + conversation + `"}`
	return do(t, http.MethodPost, o.srv.URL+postPath, token, body).StatusCode
}

// finishThread publishes a closed thread of the conversation.
func (o ownedSetup) finishThread(t *testing.T, thread, conversation string) {
	t.Helper()
	publishDiscuss(t, o.js, turnStart(thread, conversation, "q of "+conversation))
	publishDiscuss(t, o.js, msgOf(thread, thread+"-s", msgSynthesis, "answer"))
	publishDiscuss(t, o.js, msgOf(thread, thread+"-c", msgThreadClose, ""))
}

func (o ownedSetup) streamStatus(t *testing.T, token, conversation string, header ...string) int {
	t.Helper()
	return do(t, http.MethodGet, o.srv.URL+listPath+"/"+conversation+"/stream", token, "", header...).StatusCode
}

func (o ownedSetup) list(t *testing.T, token, query string, header ...string) (int, bus.ConversationList) {
	t.Helper()
	resp := do(t, http.MethodGet, o.srv.URL+listPath+query, token, "", header...)
	var got bus.ConversationList
	if resp.StatusCode == http.StatusOK {
		_ = json.NewDecoder(resp.Body).Decode(&got)
	}
	return resp.StatusCode, got
}

func ids(l bus.ConversationList) string {
	out := make([]string, 0, len(l.Conversations))
	for _, c := range l.Conversations {
		out = append(out, c.ConversationID)
	}
	return strings.Join(out, ",")
}

func TestOnlyTheOwnerMayContinueAConversation(t *testing.T) {
	o := newOwnedSetup(t, false)
	if got := o.ask(t, tokenAlice, convA); got != http.StatusOK {
		t.Fatalf("alice starts it: %d", got)
	}
	if got := o.ask(t, tokenAlice, convA); got != http.StatusOK {
		t.Fatalf("alice continues it: %d", got)
	}
	if got := o.ask(t, tokenBob, convA); got != http.StatusNotFound {
		t.Fatalf("bob continues alice's: %d, want 404", got)
	}
	if got := o.ask(t, tokenAdmin, convA); got != http.StatusNotFound {
		t.Fatalf("an operator reads other callers' conversations and does not add turns: %d", got)
	}
}

func TestTheRecordNamesTheOwnerByIdOnly(t *testing.T) {
	o := newOwnedSetup(t, false)
	o.ask(t, tokenAlice, convA)
	entry, err := o.kv.Get(context.Background(), bus.ConversationOwnerKey("ns", "pilot", convA))
	if err != nil {
		t.Fatal(err)
	}
	var rec bus.ConversationOwner
	if err := json.Unmarshal(entry.Value(), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Owner.ID != (identity.Hasher{}).ID(identity.IssuerKubernetes, userAlice) || strings.Contains(string(entry.Value()), userAlice) {
		t.Fatalf("record %s", entry.Value())
	}
}

func TestAnIdWhoseHistoryOutlivedItsRecordCannotBeClaimed(t *testing.T) {
	o := newOwnedSetup(t, false)
	if _, err := o.kv.Put(context.Background(), bus.ConversationHistoryKey("ns", "pilot", "orphan"), []byte(`{"turns":[]}`)); err != nil {
		t.Fatal(err)
	}
	if got := o.ask(t, tokenBob, "orphan"); got != http.StatusNotFound {
		t.Fatalf("status %d: a caller must not attach to a history no one owns", got)
	}
}

func TestOnlyTheOwnerMayStreamAndAnOperatorNeedsAReason(t *testing.T) {
	o := newOwnedSetup(t, false)
	o.ask(t, tokenAlice, convA)
	o.finishThread(t, "T1", convA)

	if got := o.streamStatus(t, tokenAlice, convA); got != http.StatusOK {
		t.Fatalf("owner: %d", got)
	}
	if got := o.streamStatus(t, tokenBob, convA); got != http.StatusNotFound {
		t.Fatalf("another caller: %d, want 404", got)
	}
	if got := o.streamStatus(t, tokenAdmin, convA); got != http.StatusBadRequest {
		t.Fatalf("operator without a reason: %d", got)
	}
	if got := o.streamStatus(t, tokenAdmin, convA, reasonHeader, "support ticket 42"); got != http.StatusOK {
		t.Fatalf("operator with a reason: %d", got)
	}
	if got := o.streamStatus(t, tokenAdmin, convA, reasonHeader, strings.Repeat("x", 201)); got != http.StatusBadRequest {
		t.Fatalf("a reason past the bound: %d", got)
	}
	if got := o.streamStatus(t, tokenNoRole, convA, reasonHeader, "curious"); got != http.StatusForbidden {
		t.Fatalf("a caller who may not use the crew: %d", got)
	}
}

func TestOperatorAccessIsAudited(t *testing.T) {
	var lines []string
	prev := auditLog
	auditLog = funcr.New(func(prefix, args string) { lines = append(lines, prefix+args) }, funcr.Options{})
	t.Cleanup(func() { auditLog = prev })

	o := newOwnedSetup(t, false)
	o.ask(t, tokenAlice, convA)
	o.finishThread(t, "T1", convA)
	o.streamStatus(t, tokenAdmin, convA, reasonHeader, "support ticket 42")
	o.streamStatus(t, tokenAlice, convA)
	if len(lines) != 1 {
		t.Fatalf("one operator access, none for the owner: %v", lines)
	}
	for _, want := range []string{identity.AuditEvent, `"operator"="admin"`, `"operation"="stream"`, convA, "support ticket 42",
		(identity.Hasher{}).ID(identity.IssuerKubernetes, userAlice)} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("audit record lacks %q: %s", want, lines[0])
		}
	}
}

// listSetup is a crew with alice's, bob's and an anonymous conversation, and one with no record.
func listSetup(t *testing.T) ownedSetup {
	t.Helper()
	o := newOwnedSetup(t, true)
	o.ask(t, tokenAlice, convA)
	o.ask(t, tokenBob, "conv-bob")
	o.ask(t, "", "conv-anon")
	o.finishThread(t, "T1", convA)
	o.finishThread(t, "T2", "conv-bob")
	o.finishThread(t, "T3", "conv-anon")
	// A conversation with no owner record belongs to no one.
	o.finishThread(t, "T4", "conv-unowned")
	return o
}

func TestListShowsOwnConversationsOnly(t *testing.T) {
	o := listSetup(t)
	for token, want := range map[string]string{tokenAlice: convA, tokenBob: "conv-bob", "": "conv-anon"} {
		code, got := o.list(t, token, "")
		if code != http.StatusOK || ids(got) != want {
			t.Errorf("token %q: %d %q, want %q", token, code, ids(got), want)
		}
	}
	if code, got := o.list(t, tokenAdmin, ""); code != http.StatusOK || len(got.Conversations) != 0 {
		t.Errorf("an operator lists their own without all=true: %d %v", code, got)
	}
}

func TestAnOperatorCanListAllWithAReason(t *testing.T) {
	o := listSetup(t)
	if code, _ := o.list(t, tokenAdmin, "?all=true"); code != http.StatusBadRequest {
		t.Errorf("all=true without a reason: %d", code)
	}
	code, all := o.list(t, tokenAdmin, "?all=true", reasonHeader, "audit")
	if code != http.StatusOK || len(all.Conversations) != 3 {
		t.Fatalf("operator list: %d %v", code, ids(all))
	}
	for _, c := range all.Conversations {
		if c.ConversationID == convA && c.Owner != (identity.Hasher{}).ID(identity.IssuerKubernetes, userAlice) {
			t.Errorf("the list names the owner by id: %+v", c)
		}
	}
	if code, _ := o.list(t, tokenAlice, "?all=true", reasonHeader, "curious"); code != http.StatusNotFound {
		t.Errorf("all=true without the right: %d", code)
	}
}

func TestListLimitCountsOnlyWhatTheCallerMaySee(t *testing.T) {
	o := newOwnedSetup(t, false)
	o.ask(t, tokenAlice, convA)
	o.finishThread(t, "T1", convA)
	for i, id := range []string{"b1", "b2", "b3"} {
		o.ask(t, tokenBob, id)
		o.finishThread(t, "TB"+string(rune('a'+i)), id)
	}
	if code, got := o.list(t, tokenAlice, "?limit=1"); code != http.StatusOK || ids(got) != convA {
		t.Fatalf("%d %q: newer conversations of others must not use up alice's limit", code, ids(got))
	}
}

func TestAnonymousConversationsAreOpenToAnonymousCallersOnly(t *testing.T) {
	o := newOwnedSetup(t, true)
	o.ask(t, "", "conv-anon")
	o.ask(t, tokenAlice, convA)
	o.finishThread(t, "T1", "conv-anon")
	o.finishThread(t, "T2", convA)
	if got := o.ask(t, "", "conv-anon"); got != http.StatusOK {
		t.Errorf("an anonymous caller continues an anonymous conversation: %d", got)
	}
	if got := o.streamStatus(t, "", "conv-anon"); got != http.StatusOK {
		t.Errorf("and streams it: %d", got)
	}
	if got := o.streamStatus(t, "", convA); got != http.StatusNotFound {
		t.Errorf("an anonymous caller streams alice's: %d", got)
	}
	if got := o.streamStatus(t, tokenAlice, "conv-anon"); got != http.StatusNotFound {
		t.Errorf("alice streams an anonymous conversation: %d", got)
	}
}

func TestWorkspaceKeysFollowTheirThreadsConversation(t *testing.T) {
	o := newOwnedSetup(t, false)
	o.ask(t, tokenAlice, convA)
	o.finishThread(t, "T1", convA)
	store, err := o.js.ObjectStore(context.Background(), workspaceBucket)
	if err != nil {
		t.Fatal(err)
	}
	const key = "ns/pilot/T1/agent/result-1"
	if _, err := store.PutBytes(context.Background(), key, []byte("secret result")); err != nil {
		t.Fatal(err)
	}
	read := func(token, k string, header ...string) int {
		return do(t, http.MethodGet, o.srv.URL+listPath+"/workspace?key="+k, token, "", header...).StatusCode
	}
	if got := read(tokenAlice, key); got != http.StatusOK {
		t.Fatalf("owner: %d", got)
	}
	if got := read(tokenBob, key); got != http.StatusNotFound {
		t.Fatalf("another caller: %d", got)
	}
	if got := read(tokenAdmin, key); got != http.StatusBadRequest {
		t.Fatalf("operator without a reason: %d", got)
	}
	if got := read(tokenAdmin, key, reasonHeader, "ticket"); got != http.StatusOK {
		t.Fatalf("operator with a reason: %d", got)
	}
	// A thread the stream no longer holds names no conversation: not served.
	if got := read(tokenAlice, "ns/pilot/GONE/agent/result-1"); got != http.StatusNotFound {
		t.Fatalf("unknown thread: %d", got)
	}
}

func TestAGatewayThatVerifiesNoOneWritesNoOwnersAndListsEverything(t *testing.T) {
	nc, js := jetStreamServer(t)
	kv, err := js.CreateKeyValue(context.Background(), jetstream.KeyValueConfig{Bucket: bus.ConversationsBucket})
	if err != nil {
		t.Fatal(err)
	}
	srv := gatewayServer(t, nc)
	body := `{"message":"hi","conversationId":"` + convA + `"}`
	for _, token := range []string{"", tokenAlice, tokenBob} {
		if resp := do(t, http.MethodPost, srv.URL+postPath, token, body); resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d", resp.StatusCode)
		}
	}
	publishDiscuss(t, js, turnStart("T1", convA, "q"))
	if keys, _ := kv.Keys(context.Background()); len(keys) != 0 {
		t.Fatalf("an unconfigured crew keeps no owner records: %v", keys)
	}
	resp := do(t, http.MethodGet, srv.URL+listPath, tokenBob, "")
	raw, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(raw), "owner") || !strings.Contains(string(raw), convA) {
		t.Fatalf("list %s", raw)
	}
}

func TestSameOwner(t *testing.T) {
	a := bus.Principal{Kind: bus.PrincipalKindKubernetes, ID: "1"}
	if !sameOwner(a, a) || sameOwner(a, bus.Principal{Kind: bus.PrincipalKindKubernetes, ID: "2"}) ||
		sameOwner(a, bus.Principal{Kind: bus.PrincipalKindAnonymous}) {
		t.Fatal("owner comparison")
	}
	anon := bus.Principal{Kind: bus.PrincipalKindAnonymous}
	if !sameOwner(anon, anon) {
		t.Fatal("anonymous callers share the anonymous principal")
	}
}

// With the conversations bucket unreachable, nothing is allowed: ask, stream and list all
// answer 503, and none answers with the cause.
func TestWhenOwnersCannotBeCheckedEveryOperationRefuses(t *testing.T) {
	nc, _ := jetStreamServer(t)
	srv := enforcingServer(t, nc, false) // no conversations bucket exists
	cases := []struct{ method, path, body string }{
		{http.MethodPost, postPath, `{"message":"hi","conversationId":"c1"}`},
		{http.MethodGet, listPath + "/c1/stream", ""},
		{http.MethodGet, listPath, ""},
	}
	for _, c := range cases {
		resp := do(t, c.method, srv.URL+c.path, tokenAlice, c.body)
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusServiceUnavailable || strings.Contains(string(raw), "bucket") {
			t.Errorf("%s %s: %d %s", c.method, c.path, resp.StatusCode, raw)
		}
	}
}

// Of many callers racing to claim one new id, exactly one wins and the rest are refused.
func TestExactlyOneCallerClaimsANewId(t *testing.T) {
	o := newOwnedSetup(t, false)
	store := ownerStore{kv: o.kv}
	scope := crewscope.Scope{Namespace: "ns", Crew: pilotCrew}
	const callers = 12
	results := make(chan bool, callers)
	for i := range callers {
		p := bus.Principal{Kind: bus.PrincipalKindKubernetes, ID: string(rune('a' + i))}
		go func() {
			ok, err := store.claim(context.Background(), scope, "raced", p)
			if err != nil {
				t.Error(err)
			}
			results <- ok
		}()
	}
	wins := 0
	for range callers {
		if <-results {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("%d callers claimed one id", wins)
	}
}

// An operator whose access cannot be checked is refused with 503, not admitted.
func TestAnOperatorCheckThatFailsRefuses(t *testing.T) {
	nc, js := jetStreamServer(t)
	createConversationsBucket(t, js)
	c := cluster()
	c.PrependReactor("create", "subjectaccessreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		sar := a.(k8stesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview)
		if sar.Spec.ResourceAttributes.Subresource == identity.ConversationsSubresource {
			return true, nil, errors.New("api server down")
		}
		return false, nil, nil
	})
	rv := identity.Reviewer{Client: c}
	mux := http.NewServeMux()
	NewHandler(nc, "ns").WithAccess(identity.NewAuthenticator(identity.Hasher{}, false, identity.TokenReviewVerifier{Reviewer: rv}), rv).RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	do(t, http.MethodPost, srv.URL+postPath, tokenAlice, `{"message":"hi","conversationId":"c1"}`)
	resp := do(t, http.MethodGet, srv.URL+listPath+"/c1/stream", tokenAdmin, "", reasonHeader, "ticket")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

// A record that cannot be decoded owns nothing: the owner is refused, an operator may read.
func TestAnUnreadableOwnerRecordMakesTheConversationOperatorOnly(t *testing.T) {
	o := newOwnedSetup(t, false)
	if _, err := o.kv.Put(context.Background(), bus.ConversationOwnerKey("ns", "pilot", "broken"), []byte("{not json")); err != nil {
		t.Fatal(err)
	}
	o.finishThread(t, "T1", "broken")
	if got := o.streamStatus(t, tokenAlice, "broken"); got != http.StatusNotFound {
		t.Fatalf("a caller: %d", got)
	}
	if got := o.streamStatus(t, tokenAdmin, "broken", reasonHeader, "repair"); got != http.StatusOK {
		t.Fatalf("an operator: %d", got)
	}
}

func TestTheAccessReasonIsBoundedInCharactersNotBytes(t *testing.T) {
	o := newOwnedSetup(t, false)
	o.ask(t, tokenAlice, convA)
	o.finishThread(t, "T1", convA)
	if got := o.streamStatus(t, tokenAdmin, convA, reasonHeader, strings.Repeat("é", identity.MaxAccessReason)); got != http.StatusOK {
		t.Fatalf("200 two-byte characters are 200 characters: %d", got)
	}
}
