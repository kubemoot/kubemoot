package turn

import (
	"fmt"
	"slices"
	"testing"
	"time"
)

func TestMeterAccumulates(t *testing.T) {
	m := NewMeter(Info{ThreadID: "t"})
	m.ModelCall("gpu-1", "a", "warm", 10, 5, 120*time.Millisecond)
	m.ModelCall("gpu-2", "b", "cold", 20, 6, 80*time.Millisecond)
	m.ToolCall("fetch", false)
	m.ToolCall("fetch", true)
	s := m.Snapshot()
	if s["inputTokens"] != int64(30) || s["outputTokens"] != int64(11) || s["inferenceMs"] != int64(200) ||
		s["totalTokens"] != int64(41) {
		t.Fatalf("%v", s)
	}
	if s["failedTool"] != "fetch" || s["failureCount"] != 1 {
		t.Fatalf("%v", s)
	}
	checkLastCall(t, s)
}

func checkLastCall(t *testing.T, s map[string]any) {
	t.Helper()
	if used := s["modelsUsed"].([]string); !slices.Equal(used, []string{"a@gpu-1", "b@gpu-2"}) || s["server"] != "gpu-2" {
		t.Fatalf("%v", s)
	}
	if s["modelName"] != "b" || s["pickReason"] != "cold" {
		t.Fatalf("the last call names the model and the reason: %v", s)
	}
}

func TestEmptyMeterHasNoFailureOrModelFields(t *testing.T) {
	s := NewMeter(Info{}).Snapshot()
	for _, k := range []string{"failedTool", "modelName", "modelsUsed", "server"} {
		if _, ok := s[k]; ok {
			t.Fatalf("%s present on an empty meter", k)
		}
	}
}

func TestWaitingReportsOnce(t *testing.T) {
	var got []string
	m := NewMeter(Info{OnWaiting: func(r string) { got = append(got, r) }})
	m.Waiting("gpu-busy")
	m.Waiting("gpu-busy")
	m.Degraded("placement-unavailable")
	if !slices.Equal(got, []string{"gpu-busy"}) {
		t.Fatalf("the owner hears of waiting once: %v", got)
	}
}

func TestDegradedCallsAreMarkedInThePickReason(t *testing.T) {
	m := NewMeter(Info{})
	m.Degraded("placement-unavailable")
	m.ModelCall("bound", "a", "bound", 1, 1, time.Millisecond)
	if r := m.Snapshot()["pickReason"]; r != "degraded: bound" {
		t.Fatalf("pickReason %v", r)
	}
}

func TestNoFitKindDefaultsToBusy(t *testing.T) {
	m := NewMeter(Info{})
	if m.NoFitKind() != "" {
		t.Fatal("no call failed yet")
	}
	m.NoFit("")
	if m.NoFitKind() != "gpu-busy" {
		t.Fatal(m.NoFitKind())
	}
	m.NoFit("model-too-large")
	if m.NoFitKind() != "model-too-large" {
		t.Fatal(m.NoFitKind())
	}
}

func TestRegistryAttributesByKeyOrToTheSingleOpenTurn(t *testing.T) {
	r := NewRegistry()
	a := r.Open("k1", Info{})
	if r.Lookup("k1") != a || r.Lookup("unknown") != a {
		t.Fatal("with one open turn an unknown key means that turn")
	}
	b := r.Open("k2", Info{})
	if r.Lookup("k2") != b || r.Lookup("unknown") != nil || r.Active() != 2 {
		t.Fatal("with two open turns an unknown key is unattributed")
	}
	r.Close("k1")
	r.Close("k2")
	if r.Lookup("k1") != nil || r.Active() != 0 {
		t.Fatal("closed turn still found")
	}
}

func TestAMeterAnswersARememberedCallOnlyWithinItsTurn(t *testing.T) {
	m := NewMeter(Info{})
	if _, ok := m.Answered("k"); ok {
		t.Fatal("a new turn holds no results")
	}
	m.Remember("k", "result")
	if got, ok := m.Answered("k"); !ok || got != "result" {
		t.Fatalf("got %q %v", got, ok)
	}
	if _, ok := NewMeter(Info{}).Answered("k"); ok {
		t.Fatal("another turn's meter must not see it")
	}
}

func TestRememberKeepsAtMostMaxAnsweredAndDropsTheOldest(t *testing.T) {
	m := &Meter{}
	for i := 0; i <= maxAnswered; i++ {
		m.Remember(fmt.Sprint("k", i), "v")
	}
	if _, ok := m.Answered("k0"); ok {
		t.Fatal("the oldest result should have been dropped")
	}
	if _, ok := m.Answered(fmt.Sprint("k", maxAnswered)); !ok || len(m.answered) != maxAnswered {
		t.Fatalf("newest missing or map holds %d", len(m.answered))
	}
}
