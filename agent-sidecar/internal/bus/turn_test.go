package bus_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	sdk "github.com/a2aproject/a2a-go/v2/a2a"

	kbus "github.com/kubemoot/kubemoot/operator/pkg/bus"

	"github.com/kubemoot/kubemoot/agent-sidecar/internal/a2a"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/placement"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/turn"
)

const (
	evaluating = kbus.DiscussionMessageMessageTypeEvaluating
	agree      = kbus.DiscussionMessageMessageTypeAgree
	failure    = kbus.DiscussionMessageMessageTypeFailure
	standAside = kbus.DiscussionMessageMessageTypeStandAside
	waiting    = kbus.DiscussionMessageMessageTypeWaiting
)

func TestASelectedAgentAnswersAndTheSidecarFillsTheFacts(t *testing.T) {
	p := newPod(t, options{}, func(_ context.Context, _ *sdk.Message, m *turn.Meter) (a2a.Result, error) {
		m.ModelCall("gpu1", modelQwen, "warm", 120, 30, 900*time.Millisecond)
		m.ToolCall(toolFetch, false)
		return answer("Two nodes have a GPU.")(nil, nil, nil)
	})
	p.advisory(t, "t1", agent, "other")
	if m := p.next(t); m.MessageType != evaluating {
		t.Fatalf("the first signal is evaluating, got %s", m.MessageType)
	}
	m := p.next(t)
	if m.MessageType != agree || m.Content != "Two nodes have a GPU." || m.ThreadID != "t1" || m.Channel != general {
		t.Fatalf("%+v", m)
	}
	checkMeasured(t, m.Metadata)
}

// checkMeasured asserts the facts the sidecar fills in on the terminal signal.
func checkMeasured(t *testing.T, md map[string]any) {
	t.Helper()
	for k, want := range map[string]any{"signal": "agree", "role": "tooler", "harness": "test-harness",
		"harnessImage": "registry/harness@sha256:abc", "modelName": modelQwen, "server": "gpu1",
		"inputTokens": float64(120), "outputTokens": float64(30), "totalTokens": float64(150)} {
		if md[k] != want {
			t.Errorf("metadata %s = %v, want %v", k, md[k], want)
		}
	}
	want := []string{toolFetch}
	if !slices.Equal(anyStrings(md["toolsUsed"]), want) || !slices.Equal(anyStrings(md["toolsCalled"]), want) ||
		!slices.Equal(anyStrings(md["modelsUsed"]), []string{"qwen3:14b@gpu1"}) {
		t.Errorf("toolsUsed lists what could be called, toolsCalled what was: %v", md)
	}
	if _, ok := md["turnMs"]; !ok {
		t.Error("the sidecar times the turn")
	}
}

func anyStrings(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, e := range list {
		s, _ := e.(string)
		out = append(out, s)
	}
	return out
}

func TestTheHarnessGetsTheQuestionTheThreadAndTheInstructionsContext(t *testing.T) {
	p := newPod(t, options{}, answer("ok"))
	p.send(t, "thread_start", "human", "t2", map[string]any{"userQuery": askNodes})
	p.advisory(t, "t2")
	p.nextOf(t, agree)
	msg := p.h.firstMessage()
	if msg.ContextID != "t2" || msg.Parts[0].Text() != askNodes {
		t.Fatalf("the thread is the A2A context and the person's question the text: %+v", msg)
	}
	checkProfile(t, dataOf(t, msg))
}

func checkProfile(t *testing.T, in a2a.TurnInput) {
	t.Helper()
	if in.Phase != "EVALUATING" || in.Agent != agent || in.Crew != crewPilot || in.TurnKey == "" || in.DeadlineSeconds != 30 {
		t.Errorf("%+v", in)
	}
	if len(in.Thread) != 2 || in.Thread[0].Type != "thread_start" || in.Thread[1].Type != typeAdvisory {
		t.Errorf("the harness sees the thread so far: %+v", in.Thread)
	}
}

func TestAnAgentTheCoordinatorDidNotConveneStaysQuiet(t *testing.T) {
	p := newPod(t, options{}, answer("never"))
	p.advisory(t, "t3", "someone-else")
	p.quiet(t, 400*time.Millisecond)
	if p.h.calls.Load() != 0 {
		t.Fatal("the harness must not be called")
	}
}

func TestAnAdvisoryWithNoCircleIsOpenToEveryAgent(t *testing.T) {
	p := newPod(t, options{}, answer("hi"))
	p.advisory(t, "t4")
	p.nextOf(t, agree)
}

func TestTheSameMessageDeliveredTwiceRunsOneTurn(t *testing.T) {
	p := newPod(t, options{}, answer("once"))
	m := p.advisory(t, "t5", agent)
	p.nextOf(t, agree)
	data, _ := jsonOf(m)
	if err := p.nc.Publish(id.DiscussSubject(general, "t5"), data); err != nil {
		t.Fatal(err)
	}
	p.quiet(t, 400*time.Millisecond)
	if n := p.h.calls.Load(); n != 1 {
		t.Fatalf("%d turns ran", n)
	}
}

func TestAHarnessFailureIsAFailureSignalNeverSilence(t *testing.T) {
	cases := map[string]struct {
		do   func(context.Context, *sdk.Message, *turn.Meter) (a2a.Result, error)
		kind string
	}{
		"a transport error": {func(context.Context, *sdk.Message, *turn.Meter) (a2a.Result, error) {
			return a2a.Result{}, errors.New("connection refused")
		}, "harness_error"},
		"an empty reply": {answer("   "), a2a.FailureEmpty},
		"a failed task": {func(context.Context, *sdk.Message, *turn.Meter) (a2a.Result, error) {
			return a2a.Result{State: sdk.TaskStateFailed}, nil
		}, "harness_failed"},
		"a task wanting input": {func(context.Context, *sdk.Message, *turn.Meter) (a2a.Result, error) {
			return a2a.Result{State: sdk.TaskStateInputRequired}, nil
		}, "input_required"},
	}
	for name, c := range cases {
		p := newPod(t, options{}, c.do)
		p.advisory(t, "tf", agent)
		m := p.nextOf(t, failure)
		if m.Metadata["failureType"] != c.kind || m.Content == "" {
			t.Errorf("%s: %+v", name, m)
		}
	}
}

func TestARejectedTaskIsAStandAside(t *testing.T) {
	p := newPod(t, options{}, func(context.Context, *sdk.Message, *turn.Meter) (a2a.Result, error) {
		return a2a.Result{State: sdk.TaskStateRejected}, nil
	})
	p.advisory(t, "tr", agent)
	m := p.nextOf(t, standAside)
	if m.Metadata["reason"] != "rejected" {
		t.Errorf("%+v", m.Metadata)
	}
}

func TestACallNoServerCouldTakeBecomesACapacityStandAside(t *testing.T) {
	p := newPod(t, options{}, func(_ context.Context, _ *sdk.Message, m *turn.Meter) (a2a.Result, error) {
		m.NoFit(placement.KindModelTooLarge)
		return a2a.Result{}, errors.New("the model door answered 503")
	})
	p.advisory(t, "tn", agent)
	m := p.nextOf(t, standAside)
	if m.Metadata["reason"] != placement.KindModelTooLarge {
		t.Fatalf("%+v", m.Metadata)
	}
}

func TestADeclaredToolTheGatewayLacksFailsTheTurnBeforeTheHarnessStarts(t *testing.T) {
	p := newPod(t, options{}, answer("never"))
	p.tools.missing, p.tools.detail = []string{toolFetch, "git"}, "gateway http://gw does not list them"
	p.advisory(t, "tt", agent)
	m := p.nextOf(t, failure)
	if m.Metadata["failureType"] != "tools_unavailable" || !contains(m.Content, "fetch, git") {
		t.Fatalf("%+v", m)
	}
	if p.h.calls.Load() != 0 {
		t.Fatal("a model without its tools answers from nothing; the harness must not start")
	}
}

func TestAWaitingTurnSaysSoOnTheBus(t *testing.T) {
	p := newPod(t, options{}, func(_ context.Context, _ *sdk.Message, m *turn.Meter) (a2a.Result, error) {
		m.Waiting(placement.KindGPUBusy)
		return answer("later")(nil, nil, nil)
	})
	p.advisory(t, "tw", agent)
	for {
		m := p.next(t)
		if m.MessageType == waiting {
			if m.Metadata["reason"] != placement.KindGPUBusy {
				t.Fatalf("%+v", m.Metadata)
			}
			return
		}
	}
}

func TestAHeartbeatGoesOutWhileTheTurnRuns(t *testing.T) {
	release := make(chan struct{})
	p := newPod(t, options{}, func(ctx context.Context, _ *sdk.Message, _ *turn.Meter) (a2a.Result, error) {
		<-release
		return answer("slow")(nil, nil, nil)
	})
	p.advisory(t, "th", agent)
	p.nextOf(t, kbus.DiscussionMessageMessageTypeHeartbeat)
	close(release)
	p.nextOf(t, agree)
}

func TestARunningTurnPastItsDeadlineFailsAndIsCanceled(t *testing.T) {
	p := newPod(t, options{deadline: time.Second}, func(ctx context.Context, _ *sdk.Message, _ *turn.Meter) (a2a.Result, error) {
		<-ctx.Done()
		return a2a.Result{}, ctx.Err()
	})
	p.h.task = "task-7"
	p.advisory(t, "td", agent)
	m := p.nextOf(t, failure)
	if m.Metadata["failureType"] != "loop_time_exceeded" {
		t.Fatalf("%+v", m.Metadata)
	}
	select {
	case id := <-p.h.canceled:
		if id != "task-7" {
			t.Errorf("canceled %s", id)
		}
	case <-time.After(waitFor):
		t.Fatal("the harness was not told to cancel the task")
	}
}

func TestAStopEndsARunningTurnWithoutASignal(t *testing.T) {
	started := make(chan struct{})
	p := newPod(t, options{}, func(ctx context.Context, _ *sdk.Message, _ *turn.Meter) (a2a.Result, error) {
		close(started)
		<-ctx.Done()
		return a2a.Result{}, ctx.Err()
	})
	p.h.task = "task-9"
	p.advisory(t, "ts", agent)
	p.nextOf(t, evaluating)
	<-started
	p.send(t, "stop_requested", "human", "ts", nil)
	select {
	case id := <-p.h.canceled:
		if id != "task-9" {
			t.Errorf("canceled %s", id)
		}
	case <-time.After(waitFor):
		t.Fatal("the harness was not told to cancel the task")
	}
	for {
		select {
		case m := <-p.got:
			if m.MessageType != kbus.DiscussionMessageMessageTypeHeartbeat {
				t.Fatalf("a stopped turn publishes no terminal signal, got %s", m.MessageType)
			}
		case <-time.After(500 * time.Millisecond):
			return
		}
	}
}

func TestTheSelectionIsPlannedAtOnce(t *testing.T) {
	p := newPod(t, options{}, answer("ok"))
	p.advisory(t, "tp", agent)
	p.nextOf(t, agree)
	p.planner.mu.Lock()
	defer p.planner.mu.Unlock()
	if len(p.planner.plans) != 1 || p.planner.plans[0].ThreadID != "tp" || p.planner.plans[0].Agent != agent ||
		!slices.Equal(p.planner.plans[0].Models, []string{modelQwen}) {
		t.Fatalf("%+v", p.planner.plans)
	}
}

func TestTheRequestersDateAndZoneReachTheHarnessAndTheDoors(t *testing.T) {
	var info turn.Info
	p := newPod(t, options{}, func(_ context.Context, _ *sdk.Message, m *turn.Meter) (a2a.Result, error) {
		info = m.Info()
		return answer("ok")(nil, nil, nil)
	})
	p.send(t, typeAdvisory, "coordinator", "tz", map[string]any{"requestDate": "2026-10-20T21:30:00-04:00",
		"timeZone": "America/New_York"})
	p.nextOf(t, agree)
	if info.RequestDate != "2026-10-20T21:30:00-04:00" || info.TimeZone != "America/New_York" {
		t.Errorf("the doors read the requester from the meter: %+v", info)
	}
	in := dataOf(t, p.h.firstMessage())
	if in.RequestDate != info.RequestDate || in.TimeZone != info.TimeZone {
		t.Errorf("the harness is told too: %+v", in)
	}
}

func TestAnAnalystWaitsForTheReviewPhase(t *testing.T) {
	p := newPod(t, options{role: "analyst"}, answer("reviewed"))
	p.advisory(t, "ta", agent)
	p.quiet(t, 300*time.Millisecond)
	p.send(t, "review_ready", "coordinator", "ta", nil)
	m := p.nextOf(t, agree)
	if m.Metadata["role"] != "analyst" || dataOf(t, p.h.firstMessage()).Phase != "REVIEW" {
		t.Errorf("%+v", m.Metadata)
	}
}

func TestAMessageOnAChannelTheAgentDoesNotListenOnIsIgnored(t *testing.T) {
	p := newPod(t, options{}, answer("never"))
	md := map[string]any{"innerCircle": []any{agent}}
	msg := kbus.DiscussionMessage{MessageID: "x1", ThreadID: "tc", AgentName: "coordinator",
		MessageType: typeAdvisory, Channel: "kubernetes", Metadata: md}
	data, _ := jsonOf(msg)
	if err := p.nc.Publish(id.DiscussSubject("kubernetes", "tc"), data); err != nil {
		t.Fatal(err)
	}
	p.quiet(t, 400*time.Millisecond)
}
