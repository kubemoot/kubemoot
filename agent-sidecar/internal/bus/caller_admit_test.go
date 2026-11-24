package bus

import (
	"strings"
	"testing"

	kbus "github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/kubemoot/kubemoot/operator/pkg/callerline"
)

const (
	handle = "ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12ab12"
	label  = "alice@example.org"
)

func asker(kind, id, name string) map[string]any {
	who := map[string]any{"kind": kind}
	if id != "" {
		who["id"] = id
	}
	if name != "" {
		who["displayName"] = name
	}
	return map[string]any{"userQuery": "which nodes are ready", "principal": who}
}

func start(thread string, md map[string]any) kbus.DiscussionMessage {
	return kbus.DiscussionMessage{MessageID: "s-" + thread, ThreadID: thread, AgentName: "human",
		MessageType: kbus.DiscussionMessageMessageTypeThreadStart, Metadata: md}
}

func TestACallerComesFromTheThreadStartAndTheFirstOneWins(t *testing.T) {
	a := &Admission{Agent: "me", Role: roleTooler}
	a.Judge(start("t", asker("kubernetes", handle, label)))
	a.Judge(start("t2", asker("subscriber", "owner-two", "")))
	forged := start("t", asker("kubernetes", "forged", "mallory"))
	forged.MessageID = "again"
	a.Judge(forged)
	later := msg("m", "advisory_ready", "c", "t")
	later.Metadata = asker("kubernetes", "forged2", "mallory")
	a.Judge(later)
	if got := a.CallerOf("t"); got != (Caller{Kind: "kubernetes", Handle: handle, Label: label}) {
		t.Errorf("the thread's caller is the first thread_start's: %+v", got)
	}
	if got := a.CallerOf("t2"); got.Handle != "owner-two" || got.Label != "" {
		t.Errorf("a subscriber has a handle and no label: %+v", got)
	}
	if got := a.CallerOf("nothing"); got != (Caller{}) {
		t.Errorf("a thread that was never started has no caller: %+v", got)
	}
}

func TestAThreadStartWithoutAUsablePrincipalNamesNoOne(t *testing.T) {
	a := &Admission{Agent: "me", Role: roleTooler}
	a.Judge(start("none", map[string]any{"userQuery": "q"}))
	a.Judge(start("text", map[string]any{"principal": "alice"}))
	if a.CallerOf("none") != (Caller{}) || a.CallerOf("text") != (Caller{}) {
		t.Error("no principal object, no caller")
	}
}

func TestTheCallersAreBounded(t *testing.T) {
	a := &Admission{Agent: "me", Role: roleTooler}
	for i := 0; i < maxTrackedThread+10; i++ {
		m := start(string(rune('a'+i%26))+strings.Repeat("x", i), asker("kubernetes", handle, ""))
		m.MessageID = m.ThreadID
		a.Judge(m)
	}
	if len(a.callers) > maxTrackedThread {
		t.Fatalf("%d callers held", len(a.callers))
	}
}

func TestAPrincipalWithNoKindIsStillRecordedButNamedToNoOne(t *testing.T) {
	a := &Admission{Agent: "me", Role: roleTooler}
	m := start("nk", map[string]any{"principal": map[string]any{"id": handle}})
	a.Judge(m)
	if c := a.CallerOf("nk"); c.Kind != "" || callerline.Sentence(c.Kind, c.Handle, c.Label) != "" {
		t.Errorf("%+v", c)
	}
}
