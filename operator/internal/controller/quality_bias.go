// Package controller — quality_bias.go isolates the per-capability quality
// bias scoring helpers from the main agent reconciler so the math is trivially
// unit-testable as pure functions.
//
// The card [[Capability-Weighted Quality Bias in Scheduler]] (epic-adl-poc)
// motivates this: simple toolers were paying the qwen3:32b tax under a
// uniform "all-quality" prefer rule, starving each other under Ollama's
// num_parallel=1. The agent declares an abstract capability
// (`tool-calling`, `reasoning`, ...); CSP authors what each capability is
// worth (`qualityBias: { reasoning: 0.7, tool-calling: 0.3, ... }`); the
// scheduler resolves to a concrete Model size. Loose coupling preserved —
// no model name leaks into the Agent CR.

package controller

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// latencyClassLabel is the Model label whose value (low/medium/high) drives
// the auto-derived quality bias score. Authoritative on the Model CR; the
// scheduler reads it only as a string.
const latencyClassLabel = "latencyClass"

// parseQualityBias parses a CSP QualityBias map value into a float in
// [0.0, 1.0]. Returns (value, true) on a clean parse; (0, false) for the
// empty string, unparseable values, NaN/Inf, or values outside the closed
// unit interval. Tolerant of surrounding whitespace.
//
// Strings (rather than float64) on the CRD avoid the well-known float
// precision foot-gun for kubebuilder-generated OpenAPI schemas while keeping
// the YAML human-natural ("0.7" rather than 0.7).
func parseQualityBias(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1 {
		return 0, false
	}
	return v, true
}

// effectiveQualityBias computes the per-agent bias from the CSP's QualityBias
// map and the agent's declared capabilities. Returns (bias, true) when at
// least one bias is determined; (0, false) when none can be — caller should
// then skip quality bias scoring entirely.
//
// Rule: take the max of biases for the agent's declared capabilities. If
// none of the agent's capabilities have a mapped (and parseable) bias, fall
// back to the map's "default" entry. "If any of my declared needs say
// quality matters, treat me as quality-needing" — conservative for quality.
func effectiveQualityBias(agentCaps []string, biasMap map[string]string) (float64, bool) {
	if len(biasMap) == 0 {
		return 0, false
	}
	var best float64
	var any bool
	for _, c := range agentCaps {
		if v, ok := parseQualityBias(biasMap[c]); ok {
			if !any || v > best {
				best = v
				any = true
			}
		}
	}
	if any {
		return best, true
	}
	if v, ok := parseQualityBias(biasMap["default"]); ok {
		return v, true
	}
	return 0, false
}

// qualityBiasScore returns the score contribution and reason token for a
// single Model under the supplied effective bias. Reads the Model's
// latencyClass label and produces a weight in [0, 100]:
//   - high   → round(bias * 100)            // quality-leaning models win as bias → 1
//   - medium → round(100 - |bias-0.5|*200)  // TENT curve, peaks at bias 0.5
//   - low    → round((1 - bias) * 100)      // speed-leaning models win as bias → 0
//
// Medium uses a TENT curve, not a flat 50, so the middle tier is actually
// reachable. A flat 50 is a saddle point: high=bias*100 and low=(1-bias)*100 tie
// or beat 50 everywhere except bias=0.5 (where all three tie), so medium was never
// the unique max at ANY bias and the 14B tier could never be selected. The tent
// makes medium the strict winner for bias in (0.333, 0.667) and decays to 0 at the
// extremes (a pure-speed or pure-quality agent should never land on medium).
// See [[Declare Medium-Tier 14B and Let Kubemoot Place It]].
//
// Models without a recognized latencyClass label (or with an unexpected value)
// receive no contribution and an empty reason — the caller appends nothing.
func qualityBiasScore(bias float64, modelLabels map[string]string) (int64, string) {
	switch modelLabels[latencyClassLabel] {
	case "high":
		s := int64(math.Round(bias * 100))
		return s, fmt.Sprintf("bias-high+%d", s)
	case "medium":
		s := int64(math.Round(100 - math.Abs(bias-0.5)*200))
		if s < 0 {
			s = 0
		}
		return s, fmt.Sprintf("bias-medium+%d", s)
	case "low":
		s := int64(math.Round((1 - bias) * 100))
		return s, fmt.Sprintf("bias-low+%d", s)
	default:
		return 0, ""
	}
}
