package turn

import (
	"testing"
	"time"
)

func TestMeterAccumulates(t *testing.T) {
	m := NewMeter()
	m.ModelCall("gpu-1", "a", "warm", 10, 5, 120*time.Millisecond)
	m.ModelCall("gpu-2", "b", "cold", 20, 6, 80*time.Millisecond)
	m.ToolCall("fetch", false)
	m.ToolCall("fetch", true)
	s := m.Snapshot()
	if s["inputTokens"] != int64(30) || s["outputTokens"] != int64(11) || s["inferenceMs"] != int64(200) || s["totalTokens"] != int64(41) {
		t.Fatalf("%v", s)
	}
	if s["failedTool"] != "fetch" || s["failureCount"] != 1 {
		t.Fatalf("%v", s)
	}
	if used := s["modelsUsed"].([]string); len(used) != 2 || s["server"] != "gpu-2" {
		t.Fatalf("%v", s)
	}
}

func TestEmptyMeterHasNoFailureFields(t *testing.T) {
	s := NewMeter().Snapshot()
	if _, ok := s["failedTool"]; ok {
		t.Fatal("no failure was recorded")
	}
}

func TestRegistryAttributesByKeyOrToTheSingleOpenTurn(t *testing.T) {
	r := NewRegistry()
	a := r.Open("k1")
	if r.Lookup("k1") != a || r.Lookup("unknown") != a {
		t.Fatal("with one open turn an unknown key means that turn")
	}
	b := r.Open("k2")
	if r.Lookup("k2") != b || r.Lookup("unknown") != nil {
		t.Fatal("with two open turns an unknown key is unattributed")
	}
	r.Close("k1")
	r.Close("k2")
	if r.Lookup("k1") != nil {
		t.Fatal("closed turn still found")
	}
}
