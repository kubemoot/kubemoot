/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// fitness_deferred_judge.go is the post-suite judging engine for DEFER fitness
// assertions. It is deliberately keyword-, crew-, and endpoint-AGNOSTIC.
//
// A scenario assertion of the form `DEFER synthesis <KEYWORD> "<reference>"` is
// not evaluated inline by the runner. After the whole suite completes, this engine:
//  1. collects every DEFER assertion from the suite's transcripts,
//  2. resolves each KEYWORD to the Crew that DECLARES it via the label
//     kubemoot.ai/adl-keyword=<KEYWORD> (no crew or keyword is hardcoded),
//  3. dispatches {KEYWORD, QUESTION, ANSWER, REFERENCE} to that crew's
//     discussion endpoint, and
//  4. reads back the standard verdict contract {score 0.0-1.0, fabrication, reasoning}.
//
// Adding a new evaluation capability is a deployment, not a code change: write a
// crew with a judge prompt for the keyword and label it kubemoot.ai/adl-keyword=X.
// REFLECTS (handled by kubemoot-fitness-crew) is just the first plugin.
//
// It runs as a POST-SUITE pass at completion — never inline (which would compete
// with the crew under test for GPU) and never on report download.
package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/kubemoot/kubemoot/operator/pkg/sse"
	"sync"
	"time"

	"github.com/go-logr/logr"
	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	// keywordCrewLabel is how a crew declares which DEFER fitness keyword it
	// evaluates. ANY crew labelled kubemoot.ai/adl-keyword=<KEYWORD> is invoked
	// for that keyword — keyword and crew are decoupled; new capabilities plug in
	// by deploying a crew, with no operator change.
	keywordCrewLabel = "kubemoot.ai/adl-keyword"

	// judgeDispatchTimeout bounds ONE judge-scenario dispatch. The judge runs a
	// 32b crew discussion (scoring the provided comparison document), which is GPU-bound and can
	// take many minutes (~11min observed under GPU contention). This is a safety
	// ceiling, NOT flow control: a normal dispatch settles on the 'done' event far
	// sooner. Set well above observed judge latency so a real verdict is never cut
	// off (the prior fixed 300s http.Client.Timeout cut the SSE stream mid-judge).
	judgeDispatchTimeout = 20 * time.Minute

	// logMsgScenarioScored is the structured-log message emitted on every path
	// that records a scenario's quality (all-gated, no-verdict, or a real
	// verdict) — kept identical so a log query catches every scored scenario.
	logMsgScenarioScored = "deferred judge: scenario scored"
)

var (
	deferredThinkBlock = regexp.MustCompile(`(?s)<think>.*?</think>`)
	deferredJSONObject = regexp.MustCompile(`(?s)\{.*\}`)
	deferredInFlight   sync.Map // prefix -> struct{}, dedupes the background pass per run
)

// criticalFlawPhrases are reason-text markers that AFFIRMATIVELY name a critical
// flaw in an answer. When the judge's own stated reason contains one (not negated)
// while the numeric score is >= 0.9, the score and reason are inconsistent: a
// critical flaw cannot coexist with a near-perfect score. The operator clamps
// such scores to criticalFlawScoreCap as a defensive backstop; the primary
// enforcement is the judge prompt rule. Mirrors the advisory fabrication-gate
// calibration (see the project_fitness_fabrication_gate_binary note): advisory
// telemetry rather than a hard zero.
//
// Phrases are deliberately specific (e.g. "fabricated" not bare "fabricat",
// "unconfirmed claim" not bare "unconfirmed") to avoid firing on benign hedging,
// and matches preceded by a negator ("no fabricated...", "without unsupported
// claim") are ignored - see negatedBefore.
var criticalFlawPhrases = []string{
	"fabricated",        // affirmatively names a fabricated claim (not "no fabrication")
	"missing required",  // missing required element / required field missing
	"constraint violat", // constraint violation / violated constraint
	"unconfirmed claim", // narrowed from bare "unconfirmed" (a common hedge)
	"unconfirmed correlation",
	"unsupported claim", // narrowed from bare "unsupported" (can be mild)
	"critical flaw",     // judge explicitly labels the flaw as critical
}

// flawNegators precede a phrase to turn it from a flaw statement into its denial,
// e.g. "no fabricated facts", "without unsupported claims".
var flawNegators = []string{"no ", "not ", "n't ", "without ", "nothing ", "none ", "free of "}

// criticalFlawScoreCap is the maximum score (0-1 scale) allowed when the
// judge's reason names a critical flaw. 0.70 is the bottom of the 0.70-0.89
// rubric band (correct core, minor flaw), the highest a genuinely-flawed
// answer can legitimately reach.
const criticalFlawScoreCap = 0.70

// hasCriticalFlawReason returns true when the reason affirmatively names any
// critical-flaw marker (case-insensitive, negated occurrences ignored).
func hasCriticalFlawReason(reason string) bool {
	lower := strings.ToLower(reason)
	for _, phrase := range criticalFlawPhrases {
		if flawPhrasePresent(lower, phrase) {
			return true
		}
	}
	return false
}

// flawPhrasePresent reports whether phrase occurs in s in at least one
// non-negated position.
func flawPhrasePresent(s, phrase string) bool {
	from := 0
	for {
		i := strings.Index(s[from:], phrase)
		if i < 0 {
			return false
		}
		abs := from + i
		if !negatedBefore(s, abs) {
			return true
		}
		from = abs + len(phrase)
	}
}

// negatedBefore reports whether a negator appears in the short window of text
// immediately preceding idx.
func negatedBefore(s string, idx int) bool {
	start := idx - 12
	if start < 0 {
		start = 0
	}
	prefix := s[start:idx]
	for _, neg := range flawNegators {
		if strings.Contains(prefix, neg) {
			return true
		}
	}
	return false
}

func deferredSidecarKey(prefix string) string { return prefix + "deferred-scores-v2.json" }

// deferredScoreCache is the resumable judging checkpoint: per-scenario mean
// quality (0-100) plus a Complete flag. The worker persists it after EACH scenario
// so an operator restart (or budget exhaustion) resumes instead of re-judging from
// zero — judge-all over a full N=15 suite is hours of GPU-bound crew calls, so the
// pass MUST be restart-safe. runDeferredJudgePass re-triggers until Complete.
type deferredScoreCache struct {
	Scores   map[string]float64 `json:"scores"`
	Reasons  map[string]string  `json:"reasons,omitempty"` // per-scenario judge rationale (one terse sentence), surfaced in the dashboard
	Complete bool               `json:"complete"`
}

// loadDeferredCache reads the checkpoint (empty, not-complete when absent).
func loadDeferredCache(store objectStore, prefix string) deferredScoreCache {
	c := deferredScoreCache{Scores: map[string]float64{}, Reasons: map[string]string{}}
	if data, err := store.GetObject(FitnessArtifactsBucket, deferredSidecarKey(prefix)); err == nil && data != nil {
		_ = json.Unmarshal(data, &c)
		if c.Scores == nil {
			c.Scores = map[string]float64{}
		}
		if c.Reasons == nil {
			c.Reasons = map[string]string{}
		}
	}
	return c
}

// parseDeferredAssertion pulls the KEYWORD and REFERENCE out of a deferred
// assertion's raw text: `DEFER synthesis <KEYWORD> "<reference>"`. Keyword-agnostic
// — KEYWORD is whatever token follows the subject.
func parseDeferredAssertion(raw string) (keyword, reference string, ok bool) {
	t := strings.TrimSpace(raw)
	if !strings.HasPrefix(strings.ToUpper(t), "DEFER") {
		return "", "", false
	}
	q := strings.Index(t, `"`)
	if q < 0 {
		return "", "", false
	}
	rest := t[q+1:]
	end := strings.LastIndex(rest, `"`)
	if end <= 0 {
		return "", "", false
	}
	reference = rest[:end]
	// KEYWORD is the first token before the quote that isn't DEFER or the subject.
	for _, f := range strings.Fields(t[:q]) {
		if strings.EqualFold(f, "DEFER") || strings.EqualFold(f, "synthesis") {
			continue
		}
		keyword = strings.ToUpper(f)
		break
	}
	if keyword == "" {
		return "", "", false
	}
	return keyword, reference, true
}

// resolveKeywordEndpoint finds the crew that declares the keyword and returns its
// in-cluster discussion endpoint. No keyword/crew/endpoint is hardcoded.
func resolveKeywordEndpoint(ctx context.Context, c client.Client, keyword string) (string, error) {
	var crews kubemootv1alpha1.CrewList
	if err := c.List(ctx, &crews, client.MatchingLabels{keywordCrewLabel: keyword}); err != nil {
		return "", fmt.Errorf("list crews for keyword %q: %w", keyword, err)
	}
	if len(crews.Items) == 0 {
		return "", fmt.Errorf("no crew declares %s=%s", keywordCrewLabel, keyword)
	}
	crew := crews.Items[0]
	return fmt.Sprintf("http://%s-discussion.%s.svc:80/api/v1/discussions/%s",
		crew.Name, crew.Namespace, crew.Name), nil
}

// errJudgeNoVerdict signals that the judge crew returned a parseable verdict with
// no scores and total<=0 - i.e. it produced literally nothing to score. This is the
// judge crew's own discussion flaking (empty synthesis from its coordinator,
// or a stand-aside), NOT a real
// content zero: a legitimately consensus-gated answer comes back via the total>0
// path (gated runs are counted in total and contribute 0). Observed 2026-06-11: the
// same scenario scores 0 in one run and 95-100 in another, perfectly anti-correlated
// across runs - a random ~5-8%/run judge failure. The worker retries on this signal
// rather than recording a silent 0. See [[Deferred Judge Spurious Zeros and Stuck Pass]].
var errJudgeNoVerdict = errors.New("judge returned no verdict (total<=0, no scores)")

// answerScore is one per-answer entry of the batch verdict.
type answerScore struct {
	Index       int     `json:"index"`
	Score       float64 `json:"score"`
	Fabrication bool    `json:"fabrication"`
	Reason      string  `json:"reason"`
}

// batchVerdict is the contract the batched judge returns: one 0.0-1.0 score per
// scored answer, plus the scenario's total iteration count. Gated runs (consensus
// failed / empty synthesis) are excluded from scores but counted in total — so
// they contribute 0 to the scenario quality.
type batchVerdict struct {
	Scores []answerScore `json:"scores"`
	Total  float64       `json:"total"` // float: a judge sometimes emits "total": 0.0 / 2.0
}

// singleVerdict is the SINGLE-answer shape the judge crew emits for a one-answer
// (n=1) scenario: {"score","fabrication","reasoning"}. The fitness-coordinator
// synthesis frequently collapses the batch contract to this singular form (its own
// no-verdict fallback template demonstrates exactly this shape), so the operator
// MUST tolerate it; otherwise a real verdict (often 0.85-1.0) is discarded as a
// no-verdict and recorded as a spurious 0. Score is a POINTER so an absent field
// (a genuine empty verdict) is distinguishable from a present "score": 0. Accepts
// either "reasoning" (the singular key) or "reason" (the batch key).
// See [[Deferred Judge Spurious Zeros and Stuck Pass]].
type singleVerdict struct {
	Score       *float64 `json:"score"`
	Fabrication bool     `json:"fabrication"`
	Reasoning   string   `json:"reasoning"`
	Reason      string   `json:"reason"`
}

// coerceSingleVerdict folds a single-answer verdict into the batch shape (one
// answer, total 1) when the batch parse found no scores. A genuinely empty
// verdict (no "score" field, so sv.Score is nil) is left untouched so it still
// falls through to errJudgeNoVerdict. Prefers the singular "reasoning" key,
// falling back to the batch "reason" key.
func coerceSingleVerdict(obj string, bv *batchVerdict) {
	var sv singleVerdict
	if json.Unmarshal([]byte(obj), &sv) != nil || sv.Score == nil {
		return
	}
	reason := sv.Reasoning
	if reason == "" {
		reason = sv.Reason
	}
	bv.Scores = []answerScore{{Index: 0, Score: *sv.Score, Fabrication: sv.Fabrication, Reason: reason}}
	bv.Total = 1
}

// parseScenarioQuality reads the batched verdict out of a crew's synthesis (after
// stripping any <think> block) and returns the scenario quality 0-100:
// 100 × Σ(answer scores) / total, with gated runs contributing 0 within total.
func parseScenarioQuality(synthesis string) (float64, string, error) {
	clean := deferredThinkBlock.ReplaceAllString(synthesis, "")
	obj := deferredJSONObject.FindString(clean)
	if obj == "" {
		return 0, "", fmt.Errorf("no JSON verdict in crew response")
	}
	var bv batchVerdict
	if err := json.Unmarshal([]byte(obj), &bv); err != nil {
		return 0, "", fmt.Errorf("parse verdict JSON: %w", err)
	}
	if len(bv.Scores) == 0 {
		// The fitness-coordinator often emits the single-answer shape {"score",...}
		// instead of the batch {"scores":[...],"total"} for a one-answer scenario.
		// Fold it into the batch shape so a real verdict is not discarded as a
		// no-verdict 0. A genuinely empty verdict has no "score" field and falls
		// through to errJudgeNoVerdict below.
		coerceSingleVerdict(obj, &bv)
	}
	total := bv.Total
	if total <= 0 {
		// total<=0 with no scores means the judge returned NOTHING to score. This is
		// NOT a genuine gated-0 (a gated answer is counted in total and reaches the
		// score loop below contributing 0); it is the judge crew's own discussion
		// flaking. Signal errJudgeNoVerdict so the worker can retry in-pass; only
		// after bounded retries does the worker accept 0 (with an explanatory reason),
		// which also avoids the old "re-dispatch a genuinely-empty scenario forever".
		if len(bv.Scores) == 0 {
			return 0, "", errJudgeNoVerdict
		}
		total = float64(len(bv.Scores))
	}
	sum, reason := sumVerdictScores(bv)
	return math.Max(0, math.Min(100, 100*sum/total)), reason, nil
}

// sumVerdictScores normalizes each per-answer score, applies the critical-flaw
// consistency cap, and returns their sum plus the most diagnostic reason (the
// rationale of the lowest-scoring answer, falling back to any non-empty reason).
func sumVerdictScores(bv batchVerdict) (sum float64, reason string) {
	// Capture a representative reason: the rationale of the LOWEST-scoring answer,
	// which is the most diagnostic ("why did this scenario score where it did").
	// At n=1 it is simply the single answer's reason.
	lowestSeen := math.MaxFloat64
	for _, s := range bv.Scores {
		sc := normalizeAnswerScore(s.Score, s.Reason)
		sum += sc
		if sc <= lowestSeen && s.Reason != "" {
			lowestSeen = sc
			reason = s.Reason
		}
	}
	if reason == "" { // no reason on the lowest answer; fall back to any non-empty
		for _, s := range bv.Scores {
			if s.Reason != "" {
				reason = s.Reason
				break
			}
		}
	}
	return sum, reason
}

// normalizeAnswerScore clamps one judge score to 0-1 (tolerating a 0-100 emit)
// and applies the critical-flaw consistency cap.
func normalizeAnswerScore(score float64, reason string) float64 {
	// Trust the judge's analog score. The fabrication flag is advisory
	// telemetry, NOT a gate: the rubric already scores fabrication
	// proportionally (a wholly-invented answer lands in the 0.0-0.14 band; an
	// otherwise-correct answer with one unsupported peripheral claim takes a
	// one-band deduction). Hard-zeroing every fabrication-flagged answer
	// re-binarized the distribution to 0/100 and zeroed correct answers the
	// judge had only flagged for a minor or reference-confirmed detail. The
	// anti-fabrication weight now lives in the rubric, analog. See
	// [[Fitness Judge Calibration]] and [[project_fitness_consensus_gate_rule]].
	sc := score
	if sc > 1 { // tolerate a judge that emits 0-100 per answer
		sc /= 100
	}
	sc = math.Max(0, math.Min(1, sc))
	// Consistency cap: a judge that names a critical flaw in its reason but
	// still awards a near-perfect score is internally inconsistent. Cap the
	// per-answer score so the reason and numeric outcome agree. The primary
	// fix lives in the judge prompt; this is the operator-side safety net,
	// mirroring how the fabrication gate was made advisory rather than binary.
	if sc > criticalFlawScoreCap && hasCriticalFlawReason(reason) {
		sc = criticalFlawScoreCap
	}
	return sc
}

type discussionEvent struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

// judgeAnswer is one consensus-passing iteration handed to the judge: the crew's
// final synthesis plus the tool-derived evidence behind it (matches the judge
// crew's expected per-answer shape).
type judgeAnswer struct {
	Synthesis string   `json:"synthesis"`
	Evidence  []string `json:"evidence"`
}

// judgeComparisonDoc is the full scoring context the operator hands the judge crew
// in the dispatch message - the same {question, reference, answers, gated, total}
// shape collect_scenario used to assemble crew-side. Building it operator-side
// (the operator already reads these transcripts) removes the judge's tool
// round-trip. See [[Lean Judge Direct Context No Tool Round-Trip]].
type judgeComparisonDoc struct {
	Question  string        `json:"question"`
	Reference string        `json:"reference"`
	Answers   []judgeAnswer `json:"answers"`
	Gated     int           `json:"gated"` // consensus-failed or empty-synthesis runs (score 0)
	Total     int           `json:"total"` // iterations found for the scenario
}

// buildComparisonDoc assembles the scoring context for one scenario from its
// iteration transcripts (which the operator already has in the object store),
// reproducing what the collect_scenario tool returned crew-side. Returns the
// marshalled doc, the total iterations found, and the count of judgeable
// (non-gated) answers.
func buildComparisonDoc(store objectStore, prefix string, scriptIdx int, keyword string) (doc string, total, answers int, err error) {
	scenarioPrefix := fmt.Sprintf("%ss%d-i", strings.TrimRight(prefix, "/")+"/", scriptIdx)
	keys, lErr := store.ListObjects(FitnessArtifactsBucket, scenarioPrefix)
	if lErr != nil {
		return "", 0, 0, fmt.Errorf("list transcripts: %w", lErr)
	}
	sort.Strings(keys) // stable answer order
	cd := judgeComparisonDoc{Answers: []judgeAnswer{}}
	for _, key := range keys {
		td, ok := loadTranscript(store, key)
		if !ok {
			continue
		}
		addTranscriptToDoc(&cd, td, keyword)
	}
	out, mErr := json.Marshal(cd)
	if mErr != nil {
		return "", 0, 0, fmt.Errorf("marshal comparison doc: %w", mErr)
	}
	return string(out), cd.Total, len(cd.Answers), nil
}

// loadTranscript reads and unmarshals one iteration transcript, skipping
// non-JSON keys and any unreadable or unparseable object.
func loadTranscript(store objectStore, key string) (transcriptDoc, bool) {
	if !strings.HasSuffix(key, ".json") {
		return transcriptDoc{}, false
	}
	data, gErr := store.GetObject(FitnessArtifactsBucket, key)
	if gErr != nil || len(data) == 0 {
		return transcriptDoc{}, false
	}
	var td transcriptDoc
	if json.Unmarshal(data, &td) != nil {
		return transcriptDoc{}, false
	}
	return td, true
}

// addTranscriptToDoc folds one iteration transcript into the comparison doc:
// it fills question/reference once, counts the iteration, and either gates the
// run (consensus failed or empty synthesis) or appends it as a judgeable answer.
func addTranscriptToDoc(cd *judgeComparisonDoc, td transcriptDoc, keyword string) {
	cd.Total++
	if cd.Question == "" {
		cd.Question = td.Question
	}
	if cd.Reference == "" {
		cd.Reference = referenceForKeyword(td, keyword)
	}
	synth := synthesisFromEvents(td.Events)
	// Consensus gate: a run that failed the agree floor, or produced no
	// synthesis, earns 0 quality - counted in total, not handed to the judge.
	if !consensusOKForJudge(td) || strings.TrimSpace(synth) == "" {
		cd.Gated++
		return
	}
	cd.Answers = append(cd.Answers, judgeAnswer{Synthesis: synth, Evidence: evidenceFromEvents(td.Events)})
}

// referenceForKeyword pulls the quoted reference from the scenario's
// `DEFER synthesis <KEYWORD> "<reference>"` assertion.
func referenceForKeyword(td transcriptDoc, keyword string) string {
	for _, a := range td.Assertions {
		if kw, ref, ok := parseDeferredAssertion(a.Raw); ok && kw == keyword {
			return ref
		}
	}
	return ""
}

// consensusOKForJudge reads the ">=N {toolers|specialists} agree" floor assertion
// result; absent means the scenario does not expect agreement (e.g. out-of-scope)
// and is treated as passing. Tolerates both the current "tooler" term and the
// legacy "specialist" term in stored transcripts.
func consensusOKForJudge(td transcriptDoc) bool {
	for _, a := range td.Assertions {
		l := strings.ToLower(a.Raw)
		if strings.Contains(l, "agree") && (strings.Contains(l, "tooler") || strings.Contains(l, "specialist")) {
			return a.Passed
		}
	}
	return true
}

// evidenceFromEvents returns the specialists' finding-event text (agent + signal
// prefixed) - the tool-derived observations the synthesis should be grounded in.
// Mirrors the collect_scenario tool's evidence assembly.
func evidenceFromEvents(events []transcriptEvent) []string {
	out := []string{}
	for _, e := range events {
		if e.Type != "finding" {
			continue
		}
		text := strings.TrimSpace(e.Content)
		if text == "" {
			text = strings.TrimSpace(e.Summary)
		}
		if text == "" {
			continue
		}
		if a := strings.TrimSpace(e.Agent); a != "" {
			if sig := strings.TrimSpace(e.Signal); sig != "" {
				text = a + " (" + sig + "): " + text
			} else {
				text = a + ": " + text
			}
		}
		out = append(out, text)
	}
	return out
}

// dispatchScenario asks a judge crew to score ONE fitness scenario. The operator
// hands the judge the full comparison document (question, reference, answers with
// evidence, gated, total) IN the message - no tool round-trip - and the judge
// returns the scenario quality 0-100. See [[Lean Judge Direct Context No Tool Round-Trip]].
func dispatchScenario(ctx context.Context, hc *http.Client, endpoint, comparisonDoc string) (float64, string, error) {
	convID, err := postJudgeDiscussion(ctx, hc, endpoint, comparisonDoc)
	if err != nil {
		return 0, "", err
	}
	streamURL := strings.TrimRight(endpoint, "/") + "/" + convID + "/stream"
	synthesis, err := streamJudgeSynthesis(ctx, hc, streamURL)
	if err != nil {
		return 0, "", err
	}
	if synthesis == "" {
		// The stream ended (done, EOF, or ctx deadline) with no synthesis. This is a
		// transient failure (the judge discussion didn't conclude in time), NOT
		// errJudgeNoVerdict (a COMPLETED discussion whose verdict had total<=0). Keep
		// it a plain error on purpose: it backs off to the next reconcile pass rather
		// than entering the in-pass retry loop reserved for the empty-verdict sentinel.
		return 0, "", fmt.Errorf("no synthesis verdict received")
	}
	return parseScenarioQuality(synthesis)
}

// postJudgeDiscussion opens a judge discussion with the comparison document and
// returns the conversation id to stream.
func postJudgeDiscussion(ctx context.Context, hc *http.Client, endpoint, comparisonDoc string) (string, error) {
	msg := "Judge this fitness scenario. The comparison document follows as JSON: score every answer's " +
		"synthesis against the reference, verifying each claim against that answer's evidence. " +
		"Output only the verdict JSON.\n\n" + comparisonDoc
	body, _ := json.Marshal(map[string]string{"message": msg})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	var post struct {
		ConversationID string `json:"conversationId"`
	}
	dErr := json.NewDecoder(resp.Body).Decode(&post)
	_ = resp.Body.Close()
	if dErr != nil {
		return "", fmt.Errorf("decode discussion POST: %w", dErr)
	}
	if post.ConversationID == "" {
		return "", fmt.Errorf("discussion POST returned no conversationId")
	}
	return post.ConversationID, nil
}

// streamJudgeSynthesis reads the SSE stream and returns the last non-empty
// synthesis content, stopping on the 'done' event (empty string if none seen).
func streamJudgeSynthesis(ctx context.Context, hc *http.Client, streamURL string) (string, error) {
	sReq, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL, nil)
	if err != nil {
		return "", err
	}
	sReq.Header.Set("Accept", "text/event-stream")
	sResp, err := hc.Do(sReq)
	if err != nil {
		return "", err
	}
	defer func() { _ = sResp.Body.Close() }()

	var synthesis string
	err = sse.Data(sResp.Body, func(data string) bool {
		var ev discussionEvent
		if json.Unmarshal([]byte(data), &ev) != nil {
			return true
		}
		if ev.Type == "synthesis" && strings.TrimSpace(ev.Content) != "" {
			synthesis = ev.Content
		}
		return ev.Type != "done"
	})
	return synthesis, err
}

// runDeferredJudgePass scores every DEFER assertion in the suite via the crew that
// declares its keyword, caching per-scenario mean scores. Idempotent (skips when
// the sidecar exists), deduped per run, and runs the slow crew calls off-reconcile.
func (r *CrewFitnessSuiteReconciler) runDeferredJudgePass(suite *kubemootv1alpha1.CrewFitnessSuite) {
	if r.NATSPublisher == nil || suite.Status.RunID == "" {
		return
	}
	prefix := fmt.Sprintf("%s/%s/%s/", suite.Namespace, suite.Name, suite.Status.RunID)
	if loadDeferredCache(r.NATSPublisher, prefix).Complete {
		return // already fully judged — resume only an incomplete checkpoint
	}
	if _, running := deferredInFlight.LoadOrStore(prefix, struct{}{}); running {
		return
	}
	go r.deferredJudgeWorker(suite.DeepCopy(), prefix)
}

// scenarioOutcome is the per-scenario disposition the worker loop acts on: the
// scenario was handled (move on), it remains unfinished (count as pending), or
// the batch budget is exhausted (stop the pass).
type scenarioOutcome int

const (
	scenarioHandled         scenarioOutcome = iota // scored, skipped, or not-yet-runnable gap
	scenarioPending                                // could not finish; retry on a later pass
	scenarioBudgetExhausted                        // ctx deadline hit; stop and checkpoint
)

// judgePass holds the mutable state shared across one deferred-judge pass so the
// per-scenario work can be extracted from deferredJudgeWorker's loop.
type judgePass struct {
	r         *CrewFitnessSuiteReconciler
	store     objectStore // the run's artifact store; r.NATSPublisher in production, a fake in tests
	suite     *kubemootv1alpha1.CrewFitnessSuite
	prefix    string
	cache     deferredScoreCache
	endpoints map[string]string // keyword -> endpoint (resolved once; "" = cached miss)
	hc        *http.Client
	log       logr.Logger
}

func (r *CrewFitnessSuiteReconciler) deferredJudgeWorker(suite *kubemootv1alpha1.CrewFitnessSuite, prefix string) {
	defer deferredInFlight.Delete(prefix)
	// Per-attempt budget for a judge-all pass over a full suite (15 iters × ~27
	// DEFER scenarios = hundreds of GPU-bound crew calls). This is a batch budget,
	// not flow control — the checkpoint makes the pass restart-safe, so a budget
	// overrun simply resumes on the next reconcile rather than losing work.
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Hour)
	defer cancel()
	log := logf.FromContext(ctx)

	p := &judgePass{
		r:         r,
		store:     r.NATSPublisher, // *Publisher satisfies objectStore; tests inject a fake
		suite:     suite,
		prefix:    prefix,
		cache:     loadDeferredCache(r.NATSPublisher, prefix), // resume any prior checkpoint
		endpoints: map[string]string{},
		hc:        newJudgeHTTPClient(),
		log:       log,
	}

	// One discussion per scenario. The operator discovers the scenario's DEFER
	// keyword (from one transcript — the stored Raw is canonical for .adl and .md
	// alike), builds the comparison document from all iterations (buildComparisonDoc,
	// applying the consensus gate operator-side), and hands it to the judge in the
	// dispatch message. No collect_scenario tool round-trip.
	pending := 0
	for idx := range suite.Spec.Scripts {
		switch p.processScenario(ctx, idx) {
		case scenarioPending:
			pending++
		case scenarioBudgetExhausted:
			goto done // budget exhausted — checkpoint holds finished scenarios
		case scenarioHandled:
		}
	}
done:
	complete := pending == 0 && ctx.Err() == nil
	p.persist(complete)
	log.Info("deferred judge: pass finished", "prefix", prefix,
		"scenarios", len(p.cache.Scores), "pending", pending, "complete", complete)
}

// newJudgeHTTPClient builds the judge dispatch client.
//
// NO http.Client.Timeout: it caps the ENTIRE request including the streaming
// SSE body read. The judge is a GPU-bound 32b crew discussion (scoring the
// provided comparison document) that can run minutes (~11min observed under contention); a fixed
// total cap killed the stream before the judge synthesized -> "no synthesis
// verdict received" on every pass, retried forever (stuck JUDGING). Per the
// no-fixed-GPU-timeout rule, bound each dispatch with a generous per-attempt
// context deadline (judgeDispatchTimeout) instead, and let the SSE read settle on
// the 'done' event. Transport timeouts still fail a dead connection fast.
// See [[Deferred Judge Spurious Zeros and Stuck Pass]].
func newJudgeHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
		},
	}
}

// persist writes the checkpoint with the given completeness flag.
func (p *judgePass) persist(complete bool) {
	p.cache.Complete = complete
	blob, _ := json.Marshal(p.cache)
	if _, err := p.store.PutObject(FitnessArtifactsBucket, deferredSidecarKey(p.prefix), blob, 720*time.Hour); err != nil {
		p.log.Info("deferred judge: cache write failed", "err", err.Error())
	}
}

// processScenario judges (or skips) one suite script, recording its result into
// the checkpoint, and reports the loop disposition.
func (p *judgePass) processScenario(ctx context.Context, idx int) scenarioOutcome {
	scenario := p.suite.Spec.Scripts[idx].TestRef
	if _, done := p.cache.Scores[scenario]; done {
		return scenarioHandled // resumed: already judged in a prior pass
	}
	keyword, ok := p.scenarioKeyword(idx)
	if !ok {
		return scenarioHandled // gap / never ran, or no DEFER assertion to judge
	}
	ep, resolvable := p.resolveEndpoint(ctx, keyword)
	if !resolvable {
		return scenarioPending // keyword unresolved; retry on a later pass
	}
	if ctx.Err() != nil {
		return scenarioBudgetExhausted // budget exhausted — checkpoint holds finished scenarios
	}
	// Build the full comparison document operator-side (no collect_scenario
	// tool round-trip) and hand it to the judge in the dispatch message.
	comparisonDoc, total, answers, bErr := buildComparisonDoc(p.store, p.prefix, idx, keyword)
	if bErr != nil {
		p.log.Info("deferred judge: build comparison doc failed; retry next pass", "scenario", scenario, "err", bErr.Error())
		return scenarioPending
	}
	if total == 0 {
		return scenarioHandled // no transcripts for this scenario yet (gap / not run)
	}
	if answers == 0 {
		// Every iteration gated (consensus failed / empty synthesis): quality is
		// 0 by definition; no judge call needed. Recorded with a reason so it is
		// distinguishable from a flake.
		p.recordScore(scenario, 0, "all iterations gated (consensus failed or empty synthesis)")
		p.log.Info(logMsgScenarioScored, "scenario", scenario, "quality", 0.0, "reason", "all-gated")
		return scenarioHandled
	}
	return p.judgeAndRecord(ctx, scenario, keyword, ep, comparisonDoc)
}

// scenarioKeyword probes the first iteration transcript and returns the DEFER
// keyword it declares. ok is false when there is no transcript yet or no DEFER
// assertion to judge.
func (p *judgePass) scenarioKeyword(idx int) (string, bool) {
	probeKey := fmt.Sprintf("%ss%d-i1.json", p.prefix, idx)
	data, gErr := p.store.GetObject(FitnessArtifactsBucket, probeKey)
	if gErr != nil || data == nil {
		return "", false // no transcript for this scenario (gap / never ran)
	}
	ir, ok := iterationFromTranscript(p.suite, probeKey, data)
	if !ok {
		return "", false
	}
	for _, a := range ir.Assertions {
		if kw, _, isDef := parseDeferredAssertion(a.Raw); isDef {
			return kw, true
		}
	}
	return "", false // no DEFER assertion — nothing to judge for this scenario
}

// resolveEndpoint returns the judge endpoint for a keyword, resolving once and
// caching the result (including a miss as ""). resolvable is false when the
// keyword cannot be resolved (no crew declares it yet).
func (p *judgePass) resolveEndpoint(ctx context.Context, keyword string) (string, bool) {
	ep, have := p.endpoints[keyword]
	if !have {
		resolved, rErr := resolveKeywordEndpoint(ctx, p.r.Client, keyword)
		if rErr != nil {
			p.log.Info("deferred judge: keyword unresolved — leaving for a later pass", "keyword", keyword, "err", rErr.Error())
			p.endpoints[keyword] = "" // cache the miss
		} else {
			ep = resolved
			p.endpoints[keyword] = ep
		}
	}
	return ep, ep != ""
}

// judgeAndRecord dispatches the comparison document to the judge (with bounded
// no-verdict retries), records the outcome, and reports the loop disposition.
func (p *judgePass) judgeAndRecord(ctx context.Context, scenario, keyword, ep, comparisonDoc string) scenarioOutcome {
	quality, reason, jErr := p.dispatchWithRetries(ctx, scenario, ep, comparisonDoc)
	switch {
	case errors.Is(jErr, errJudgeNoVerdict):
		// Exhausted retries - treat as genuinely empty/gated, but record WHY so
		// it is not a silent zero and the pass converges.
		p.recordScore(scenario, 0, "judge returned no verdict after retries (treated as gated/empty)")
		p.log.Info(logMsgScenarioScored, "scenario", scenario, "quality", 0.0, "reason", "no-verdict-after-retries")
		return scenarioHandled
	case jErr != nil:
		p.log.Info("deferred judge: dispatch failed", "scenario", scenario, "keyword", keyword, "err", jErr.Error())
		return scenarioPending
	}
	p.recordScore(scenario, quality, reason)
	p.log.Info(logMsgScenarioScored, "scenario", scenario, "quality", quality, "reason", reason)
	return scenarioHandled
}

// dispatchWithRetries calls the judge, retrying only on errJudgeNoVerdict.
//
// Bounded in-pass retry on errJudgeNoVerdict: the judge crew flaked and
// returned nothing to score. Re-dispatching re-sends the same comparisonDoc
// to the judge, which usually succeeds (proven by manual re-judges that
// recovered scenarios from 0 to 95-100). Only after all retries still return
// no verdict do we accept 0 - a genuinely-empty scenario then converges to 0
// WITH a reason, so the run still completes and the 0 is distinguishable from
// a silent flake. Other errors (network, no synthesis, decode) are left
// unjudged to retry on a later pass, as before.
func (p *judgePass) dispatchWithRetries(ctx context.Context, scenario, ep, comparisonDoc string) (float64, string, error) {
	const noVerdictRetries = 3
	var quality float64
	var reason string
	var jErr error
	for attempt := 0; attempt <= noVerdictRetries; attempt++ {
		// Per-dispatch deadline: a generous safety ceiling for one GPU-bound judge
		// discussion, NOT flow control (the SSE read settles on 'done' well before
		// it). Set above observed judge latency so a normal verdict is never cut off.
		dctx, dcancel := context.WithTimeout(ctx, judgeDispatchTimeout)
		quality, reason, jErr = dispatchScenario(dctx, p.hc, ep, comparisonDoc)
		dcancel()
		if !errors.Is(jErr, errJudgeNoVerdict) {
			break // success, or a different (retry-next-pass) error
		}
		if attempt < noVerdictRetries && ctx.Err() == nil {
			p.log.Info("deferred judge: no verdict, retrying", "scenario", scenario, "attempt", attempt+1)
			time.Sleep(3 * time.Second)
		}
	}
	return quality, reason, jErr
}

// recordScore writes a scenario's quality (and reason, when non-empty) into the
// checkpoint and persists it so a restart resumes.
func (p *judgePass) recordScore(scenario string, quality float64, reason string) {
	p.cache.Scores[scenario] = quality
	if reason != "" {
		p.cache.Reasons[scenario] = reason
	}
	p.persist(false) // checkpoint after each scenario so a restart resumes
}

// readDeferredScores returns the cached per-scenario deferred-judge scores (0-100).
// A partial checkpoint is returned as-is — the report shows quality for scenarios
// judged so far while the pass continues.
func readDeferredScores(store objectStore, prefix string) map[string]float64 {
	return loadDeferredCache(store, prefix).Scores
}

// NOTE: the DEFER engine deploys via this operator image. A prior concurrent
// chart-bump race dropped the operator appVersion bump, so this re-release ensures
// the engine ships. See the pipeline-robustness kanban card.
