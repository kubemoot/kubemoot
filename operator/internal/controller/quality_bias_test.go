package controller

import (
	"math"
	"testing"
)

// TestParseQualityBias pins the input tolerance of CSP QualityBias values.
// String-on-CRD lets authors write "0.7" naturally; this parser is the
// boundary that rejects malformed entries silently rather than failing
// scheduling on a typo.
func TestParseQualityBias(t *testing.T) {
	cases := []struct {
		in    string
		want  float64
		valid bool
	}{
		{"0.7", 0.7, true},
		{"0", 0.0, true},
		{"1", 1.0, true},
		{"  0.5  ", 0.5, true}, // whitespace-tolerant
		{"", 0, false},
		{testABC, 0, false},
		{"-0.1", 0, false},        // below range
		{testVersion11, 0, false}, // above range
		{"NaN", 0, false},
		{"+Inf", 0, false},
	}
	for _, c := range cases {
		got, ok := parseQualityBias(c.in)
		if ok != c.valid {
			t.Errorf("parseQualityBias(%q) valid=%v, want %v", c.in, ok, c.valid)
			continue
		}
		if ok && math.Abs(got-c.want) > 1e-9 {
			t.Errorf("parseQualityBias(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestEffectiveQualityBiasMaxAcrossCapabilities pins the conservative
// "max wins" combiner from the kanban card decision: if any of an agent's
// declared capabilities asks for quality, treat the agent as quality-needing.
// The homelab coordinator's `[tool-calling, reasoning]` should resolve to
// the reasoning bias (0.7), not be diluted by the tool-calling bias (0.3).
func TestEffectiveQualityBiasMaxAcrossCapabilities(t *testing.T) {
	biasMap := map[string]string{
		testToolCalling: "0.3",
		testReasoning:   "0.7",
		"observability": testBias04,
		testDefault:     testBias04,
	}

	// Single-capability tooler (nvidia-gpu-now) → 0.3 (speed-leaning).
	if b, ok := effectiveQualityBias([]string{testToolCalling}, biasMap); !ok || math.Abs(b-0.3) > 1e-9 {
		t.Errorf("single tool-calling: got (%v,%v), want (0.3,true)", b, ok)
	}
	// Coordinator declares both → max(0.3, 0.7) = 0.7 (quality-leaning).
	if b, ok := effectiveQualityBias([]string{testToolCalling, testReasoning}, biasMap); !ok || math.Abs(b-0.7) > 1e-9 {
		t.Errorf("coordinator caps: got (%v,%v), want (0.7,true)", b, ok)
	}
	// obs-metrics: tool-calling + observability → max(0.3, 0.4) = 0.4.
	if b, ok := effectiveQualityBias([]string{testToolCalling, "observability"}, biasMap); !ok || math.Abs(b-0.4) > 1e-9 {
		t.Errorf("obs-metrics caps: got (%v,%v), want (0.4,true)", b, ok)
	}
}

// TestEffectiveQualityBiasDefaultFallback pins the fallback path: when none
// of the agent's declared capabilities have a parseable bias entry, the
// map's "default" key applies. Keeps unmapped capabilities from silently
// disabling quality-bias scoring entirely.
func TestEffectiveQualityBiasDefaultFallback(t *testing.T) {
	biasMap := map[string]string{
		testReasoning: "0.9",
		testDefault:   testBias04,
	}
	// Agent declares capabilities none of which are in the map → use default.
	if b, ok := effectiveQualityBias([]string{"unmapped-cap"}, biasMap); !ok || math.Abs(b-0.4) > 1e-9 {
		t.Errorf("unmapped cap fall back: got (%v,%v), want (0.4,true)", b, ok)
	}
	// Empty capabilities → use default.
	if b, ok := effectiveQualityBias(nil, biasMap); !ok || math.Abs(b-0.4) > 1e-9 {
		t.Errorf("nil caps fall back: got (%v,%v), want (0.4,true)", b, ok)
	}
	// One mapped + several unmapped → still use the mapped one, not default.
	if b, ok := effectiveQualityBias([]string{"unmapped", testReasoning, testOther}, biasMap); !ok || math.Abs(b-0.9) > 1e-9 {
		t.Errorf("one mapped wins over default: got (%v,%v), want (0.9,true)", b, ok)
	}
}

// TestEffectiveQualityBiasDisabledWhenMissing pins the "no signal" path. An
// empty biasMap, or a map with only unparseable values and no default,
// returns (0, false) so the caller skips bias scoring entirely instead of
// silently treating the agent as 0-bias (which would heavily favor
// latencyClass:low — a hidden behavior change).
func TestEffectiveQualityBiasDisabledWhenMissing(t *testing.T) {
	// Nil map.
	if b, ok := effectiveQualityBias([]string{testReasoning}, nil); ok || b != 0 {
		t.Errorf("nil map: got (%v,%v), want (0,false)", b, ok)
	}
	// Empty map.
	if b, ok := effectiveQualityBias([]string{testReasoning}, map[string]string{}); ok || b != 0 {
		t.Errorf("empty map: got (%v,%v), want (0,false)", b, ok)
	}
	// All unparseable, no default.
	bad := map[string]string{testReasoning: "garbage", testToolCalling: ""}
	if b, ok := effectiveQualityBias([]string{testReasoning, testToolCalling}, bad); ok || b != 0 {
		t.Errorf("all unparseable: got (%v,%v), want (0,false)", b, ok)
	}
	// Unparseable default with unmapped caps.
	badDefault := map[string]string{testDefault: "xyz"}
	if b, ok := effectiveQualityBias([]string{"unmapped"}, badDefault); ok || b != 0 {
		t.Errorf("unparseable default: got (%v,%v), want (0,false)", b, ok)
	}
}

// TestQualityBiasScoreLatencyClassRanking pins the latencyClass → score
// mapping. With bias=0.7 (quality-leaning), latencyClass:high wins; with
// bias=0.3 (speed-leaning), latencyClass:low wins. medium follows a tent curve
// (peaks at bias 0.5). Models without a latencyClass label contribute nothing.
func TestQualityBiasScoreLatencyClassRanking(t *testing.T) {
	highLbl := map[string]string{testLatencyClass: testHigh}
	medLbl := map[string]string{testLatencyClass: "medium"}
	lowLbl := map[string]string{testLatencyClass: testLow}
	noLbl := map[string]string{"family": "qwen3"} // no latencyClass

	// Quality-biased agent (e.g. coordinator @ 0.7) — high > medium > low.
	highQ, _ := qualityBiasScore(0.7, highLbl)
	medQ, _ := qualityBiasScore(0.7, medLbl)
	lowQ, _ := qualityBiasScore(0.7, lowLbl)
	if highQ <= medQ || medQ <= lowQ {
		t.Errorf("bias=0.7 ranking: high=%d med=%d low=%d, want high>med>low", highQ, medQ, lowQ)
	}
	// medium tent at bias 0.7: 100 - |0.7-0.5|*200 = 60.
	if highQ != 70 || medQ != 60 || lowQ != 30 {
		t.Errorf("bias=0.7 exact scores: got high=%d med=%d low=%d, want 70/60/30", highQ, medQ, lowQ)
	}

	// Speed-biased agent (e.g. nvidia-gpu-now @ 0.3) — low > medium > high.
	highS, _ := qualityBiasScore(0.3, highLbl)
	medS, _ := qualityBiasScore(0.3, medLbl)
	lowS, _ := qualityBiasScore(0.3, lowLbl)
	if lowS <= medS || medS <= highS {
		t.Errorf("bias=0.3 ranking: high=%d med=%d low=%d, want low>med>high", highS, medS, lowS)
	}

	// No latencyClass label → empty reason → caller appends nothing.
	gotScore, gotReason := qualityBiasScore(0.7, noLbl)
	if gotScore != 0 || gotReason != "" {
		t.Errorf("no latencyClass: got (%d,%q), want (0,\"\")", gotScore, gotReason)
	}
}

// TestQualityBiasScoreBoundsAndSymmetry pins the math at the endpoints.
// bias=0.0 → low gets 100, high gets 0 (pure speed). bias=1.0 → high gets
// 100, low gets 0 (pure quality). bias=0.5 → high=low=50 but medium peaks at 100.
// Reason tokens carry the score so dashboard attribution stays informative.
func TestQualityBiasScoreBoundsAndSymmetry(t *testing.T) {
	highLbl := map[string]string{testLatencyClass: testHigh}
	lowLbl := map[string]string{testLatencyClass: testLow}
	medLbl := map[string]string{testLatencyClass: "medium"}

	// bias=0.0 — full speed.
	if s, r := qualityBiasScore(0.0, highLbl); s != 0 || r != "bias-high+0" {
		t.Errorf("bias=0 high: got (%d,%q), want (0,bias-high+0)", s, r)
	}
	if s, _ := qualityBiasScore(0.0, lowLbl); s != 100 {
		t.Errorf("bias=0 low: got %d, want 100", s)
	}

	// bias=1.0 — full quality.
	if s, _ := qualityBiasScore(1.0, highLbl); s != 100 {
		t.Errorf("bias=1 high: got %d, want 100", s)
	}
	if s, r := qualityBiasScore(1.0, lowLbl); s != 0 || r != "bias-low+0" {
		t.Errorf("bias=1 low: got (%d,%q), want (0,bias-low+0)", s, r)
	}

	// bias=0.5 — high and low tie at 50; medium PEAKS at 100 (the tent apex), so
	// the middle tier is the strict winner exactly where neither speed nor quality
	// is preferred. This is the saddle-point fix: previously all three tied at 50.
	hi, _ := qualityBiasScore(0.5, highLbl)
	md, _ := qualityBiasScore(0.5, medLbl)
	lo, _ := qualityBiasScore(0.5, lowLbl)
	if hi != 50 || md != 100 || lo != 50 {
		t.Errorf("bias=0.5: got high=%d med=%d low=%d, want 50/100/50", hi, md, lo)
	}

	// medium decays to 0 at the extremes so a pure-speed/pure-quality agent never
	// lands on the middle tier.
	if s, _ := qualityBiasScore(0.0, medLbl); s != 0 {
		t.Errorf("bias=0 medium: got %d, want 0", s)
	}
	if s, _ := qualityBiasScore(1.0, medLbl); s != 0 {
		t.Errorf("bias=1 medium: got %d, want 0", s)
	}

	// medium is reachable (strict max) in the mid band, e.g. bias=0.4: 80 vs 40/60.
	if s, _ := qualityBiasScore(0.4, medLbl); s != 80 {
		t.Errorf("bias=0.4 medium: got %d, want 80 (reachable mid-tier)", s)
	}
}
