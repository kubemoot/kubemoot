package bus

import (
	"fmt"
	"os"
	"testing"
	"time"

	kbus "github.com/kubemoot/kubemoot/operator/pkg/bus"
)

const (
	roleTooler     = "tooler"
	chanKubernetes = "kubernetes"
)

func msg(id, typ, from, thread string, circle ...string) kbus.DiscussionMessage {
	m := kbus.DiscussionMessage{MessageID: id, ThreadID: thread, AgentName: from, MessageType: kbus.DiscussionMessageMessageType(typ)}
	if len(circle) > 0 {
		in := make([]any, len(circle))
		for i, c := range circle {
			in[i] = c
		}
		m.Metadata = map[string]any{"innerCircle": in}
	}
	return m
}

func TestJudge(t *testing.T) {
	cases := []struct {
		name string
		role string
		seq  []kbus.DiscussionMessage
		want []Verdict // the verdict of each message in seq
	}{
		{"a tooler runs on the advisory", roleTooler, []kbus.DiscussionMessage{msg("1", "advisory_ready", "coordinator", "t")}, []Verdict{Run}},
		{"a tooler runs on follow_up and reply", roleTooler, []kbus.DiscussionMessage{
			msg("1", "follow_up", "coordinator", "t"), msg("2", "reply", "human", "t")}, []Verdict{Run, Run}},
		{"a tooler ignores thread_start and review_ready", roleTooler, []kbus.DiscussionMessage{
			msg("1", "thread_start", "human", "t"), msg("2", "review_ready", "coordinator", "t")}, []Verdict{Ignore, Ignore}},
		{"an analyst runs on review_ready only", RoleAnalyst, []kbus.DiscussionMessage{
			msg("1", "advisory_ready", "coordinator", "t"), msg("2", "review_ready", "coordinator", "t")}, []Verdict{Ignore, Run}},
		{"its own message is ignored", roleTooler, []kbus.DiscussionMessage{msg("1", "advisory_ready", "me", "t")}, []Verdict{Ignore}},
		{"a repeated id is a duplicate", roleTooler, []kbus.DiscussionMessage{
			msg("1", "advisory_ready", "coordinator", "t"), msg("1", "advisory_ready", "coordinator", "t")}, []Verdict{Run, Duplicate}},
		{"a circle without us is not selected", roleTooler, []kbus.DiscussionMessage{
			msg("1", "advisory_ready", "coordinator", "t", "a", "b")}, []Verdict{NotSelected}},
		{"a circle with us runs", roleTooler, []kbus.DiscussionMessage{
			msg("1", "advisory_ready", "coordinator", "t", "a", "me")}, []Verdict{Run}},
		{"an empty circle is open", roleTooler, []kbus.DiscussionMessage{
			{MessageID: "1", ThreadID: "t", AgentName: "coordinator", MessageType: "advisory_ready", Metadata: map[string]any{"innerCircle": []any{}}}}, []Verdict{Run}},
		{"a closed thread ignores a late advisory", roleTooler, []kbus.DiscussionMessage{
			msg("1", "synthesis", "coordinator", "t"), msg("2", "advisory_ready", "coordinator", "t")}, []Verdict{Stop, Ignore}},
		{"a person's reply reopens a closed thread", roleTooler, []kbus.DiscussionMessage{
			msg("1", "thread_close", "coordinator", "t"), msg("2", "reply", "human", "t")}, []Verdict{Stop, Run}},
		{"stop_requested stops", roleTooler, []kbus.DiscussionMessage{msg("1", "stop_requested", "human", "t")}, []Verdict{Stop}},
		{"three turns a thread, the fourth is limited", roleTooler, []kbus.DiscussionMessage{
			msg("1", "reply", "human", "t"), msg("2", "reply", "human", "t"), msg("3", "reply", "human", "t"),
			msg("4", "reply", "human", "t")}, []Verdict{Run, Run, Run, Limited}},
	}
	for _, c := range cases {
		a := &Admission{Agent: "me", Role: c.role}
		for i, m := range c.seq {
			if got := a.Judge(m); got != c.want[i] {
				t.Errorf("%s: message %d (%s) got verdict %d, want %d", c.name, i+1, m.MessageType, got, c.want[i])
			}
		}
	}
}

func TestTheMinuteLimitCapsTheAgentAcrossThreads(t *testing.T) {
	now := time.Now()
	a := &Admission{Agent: "me", Role: roleTooler, Now: func() time.Time { return now }}
	for i := 0; i < maxPerMinute; i++ {
		if v := a.Judge(msg(fmt.Sprint("m", i), "advisory_ready", "c", fmt.Sprint("thread", i))); v != Run {
			t.Fatalf("turn %d: %d", i, v)
		}
	}
	if v := a.Judge(msg("over", "advisory_ready", "c", "another")); v != Limited {
		t.Fatalf("the eleventh turn in a minute: %d", v)
	}
	now = now.Add(rateWindow + time.Second)
	if v := a.Judge(msg("later", "advisory_ready", "c", "another")); v != Run {
		t.Fatalf("a new minute: %d", v)
	}
}

func TestTheSeenSetIsBounded(t *testing.T) {
	a := &Admission{Agent: "me", Role: roleTooler}
	a.Judge(msg("first", "thread_start", "h", "t"))
	for i := 0; i < maxSeen; i++ {
		a.Judge(msg(fmt.Sprint("x", i), "thread_start", "h", "t"))
	}
	if len(a.seen) > maxSeen {
		t.Fatalf("%d ids remembered", len(a.seen))
	}
	if a.Judge(msg("first", "advisory_ready", "c", "t")) == Duplicate {
		t.Fatal("the oldest id should have been forgotten")
	}
}

func TestTheThreadIsRecordedInOrderAndCapped(t *testing.T) {
	a := &Admission{Agent: "me", Role: roleTooler}
	a.Judge(kbus.DiscussionMessage{MessageID: "1", ThreadID: "t", AgentName: "human", MessageType: "thread_start", Content: "q"})
	a.Judge(kbus.DiscussionMessage{MessageID: "2", ThreadID: "t", AgentName: "other", MessageType: "agree", Content: "a"})
	got := a.Thread("t")
	if len(got) != 2 || got[0].Content != "q" || got[1].Agent != "other" {
		t.Fatalf("%+v", got)
	}
	for i := 0; i < maxThreadEntries+20; i++ {
		a.Judge(kbus.DiscussionMessage{MessageID: fmt.Sprint("m", i), ThreadID: "t", AgentName: "x", MessageType: "agree"})
	}
	if n := len(a.Thread("t")); n != maxThreadEntries {
		t.Fatalf("%d entries", n)
	}
	got[0].Content = "changed"
	if a.Thread("t")[0].Content == "changed" {
		t.Fatal("Thread must return a copy")
	}
}

func TestChannelOf(t *testing.T) {
	for in, want := range map[string]string{
		"kubemoot.discuss.ns.crew.general.t1":     "general",
		"kubemoot.discuss.ns.crew.general.t.with": "general",
		"kubemoot.discuss.ns":                     "",
		"":                                        "",
	} {
		if got := channelOf(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

func TestInstructions(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := dir + "/" + name
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	system := write("system.txt", "ALWAYS be brief")
	write("kubernetes.txt", "WHEN pods fail THEN read events")
	i := LoadInstructions(system, dir)
	if got := i.For(nil); got != "ALWAYS be brief" {
		t.Errorf("no skills: %q", got)
	}
	got := i.For([]string{chanKubernetes, "../etc/passwd", "missing", ""})
	want := "ALWAYS be brief\n\nSkills context for this discussion:\n\n--- Skill: kubernetes ---\nWHEN pods fail THEN read events\n"
	if got != want {
		t.Errorf("got %q", got)
	}
	if got := LoadInstructions("/no/such/file", dir).For([]string{chanKubernetes}); got == "" || got[:6] != "Skills" {
		t.Errorf("no system prompt still gets skills: %q", got)
	}
	if got := LoadInstructions("", "").For([]string{chanKubernetes}); got != "" {
		t.Errorf("nothing configured, nothing said: %q", got)
	}
}
