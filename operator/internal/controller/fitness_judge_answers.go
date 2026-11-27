/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// fitness_judge_answers.go keeps the deferred judge's verdict per answer. The
// judge scores every iteration of a scenario; the scenario score is the mean of
// those answer scores, and each answer's score, fabrication flag and gated state
// stay in the checkpoint and in status.judge.scores.
package controller

import (
	"encoding/json"
	"math"
	"sort"
	"strings"

	"github.com/go-logr/logr"
)

const (
	// reasonGatedAnswer is the reason recorded for an iteration that failed the
	// consensus floor or returned an empty answer: it scores 0 without a judge call.
	reasonGatedAnswer = "consensus failed or empty synthesis"

	// reasonNoAnswerScore is the reason recorded for an answer the judge's verdict
	// has no score for; it counts 0, as a gated answer does.
	reasonNoAnswerScore = "the judge returned no score for this answer"

	// reasonNoVerdict is the reason recorded for the answers of a document the judge
	// returned no verdict for on every in-pass retry.
	reasonNoVerdict = "judge returned no verdict after retries (treated as gated/empty)"
)

// answerRecord is the judge's verdict on one iteration of a scenario.
type answerRecord struct {
	Iteration   int     `json:"iteration"`
	Score       float64 `json:"score"` // 0-100
	Fabrication bool    `json:"fabrication,omitempty"`
	Gated       bool    `json:"gated,omitempty"`
	Reason      string  `json:"reason,omitempty"`
}

// judgeBatch is one comparison document and the iteration number of each answer in
// it, in document order: the verdict's index k scores Iterations[k].
type judgeBatch struct {
	Doc        string
	Iterations []int
}

// judgeWork is everything the judge has to score for one scenario: the documents
// to dispatch (one unless the answers did not fit the judge's window together) and
// the iterations that were gated without a judge call.
type judgeWork struct {
	Batches []judgeBatch
	Gated   []int
	Total   int // iterations found for the scenario
}

// answers is how many iterations the judge is asked to score.
func (w judgeWork) answers() int {
	n := 0
	for _, b := range w.Batches {
		n += len(b.Iterations)
	}
	return n
}

// gatedRecords is one zero-score record per gated iteration.
func (w judgeWork) gatedRecords() []answerRecord {
	out := make([]answerRecord, 0, len(w.Gated))
	for _, it := range w.Gated {
		out = append(out, answerRecord{Iteration: it, Gated: true, Reason: reasonGatedAnswer})
	}
	return out
}

// answerOf is one judgeable iteration as read from its transcript.
type answerOf struct {
	iteration int
	answer    judgeAnswer
}

// planBatches splits a scenario's answers into comparison documents that each fit
// budgetChars (zero or less means no limit). Answers are packed in iteration
// order, as many to a document as fit with their evidence whole. A scenario whose
// answers fit together is one document that also tells the judge how many runs
// were gated; a document of several parts carries only its own answers. An answer
// that is too large alone gets its own document, with its evidence cut to fit.
func planBatches(base judgeComparisonDoc, items []answerOf, budgetChars int, log logr.Logger) ([]judgeBatch, error) {
	if len(items) == 0 {
		return nil, nil
	}
	groups := [][]answerOf{items}
	if budgetChars > 0 && docSize(withAnswers(base, items, true)) > budgetChars {
		groups = packAnswers(base, items, budgetChars)
	}
	whole := len(groups) == 1
	batches := make([]judgeBatch, 0, len(groups))
	for _, g := range groups {
		doc, err := marshalFitted(withAnswers(base, g, whole), budgetChars, log)
		if err != nil {
			return nil, err
		}
		batch := judgeBatch{Doc: doc, Iterations: make([]int, len(g))}
		for i, it := range g {
			batch.Iterations[i] = it.iteration
		}
		batches = append(batches, batch)
	}
	return batches, nil
}

// withAnswers is base holding the given answers. keepCounts keeps the scenario's
// gated and total counts; otherwise the document counts only its own answers.
func withAnswers(base judgeComparisonDoc, items []answerOf, keepCounts bool) judgeComparisonDoc {
	cd := base
	cd.Answers = make([]judgeAnswer, len(items))
	for i, it := range items {
		cd.Answers[i] = it.answer
		cd.Answers[i].Index = i
	}
	if !keepCounts {
		cd.Gated, cd.Total = 0, len(items)
	}
	return cd
}

func docSize(cd judgeComparisonDoc) int {
	out, err := json.Marshal(cd)
	if err != nil {
		return 0
	}
	return len(out)
}

// packAnswers fills documents in order: an answer joins the current document while
// the document, evidence whole, stays within budgetChars; otherwise it opens the next.
func packAnswers(base judgeComparisonDoc, items []answerOf, budgetChars int) [][]answerOf {
	var groups [][]answerOf
	var cur []answerOf
	for _, it := range items {
		next := append(append([]answerOf(nil), cur...), it)
		if len(cur) > 0 && docSize(withAnswers(base, next, false)) > budgetChars {
			groups = append(groups, cur)
			next = []answerOf{it}
		}
		cur = next
	}
	return append(groups, cur)
}

// assignAnswerScores turns one verdict into a record per answer of its batch. Each
// answer in the document carries its position as an index, which the judge echoes
// on its score. The indexes are used only when they are exactly the document's:
// one score per answer, every score with an index, no index repeated or out of
// range. Otherwise the scores are read by position, but only when there is one
// per answer. Otherwise no score can be tied to an answer, and every answer counts
// 0 with the reason.
func assignAnswerScores(bv batchVerdict, iterations []int) []answerRecord {
	n := len(iterations)
	scored := make([]*answerScore, n)
	switch {
	case echoesEveryIndex(bv.Scores, n):
		for i := range bv.Scores {
			scored[*bv.Scores[i].Index] = &bv.Scores[i]
		}
	case len(bv.Scores) == n:
		for i := range bv.Scores {
			scored[i] = &bv.Scores[i]
		}
	}
	out := make([]answerRecord, n)
	for i, it := range iterations {
		out[i] = answerRecord{Iteration: it, Reason: reasonNoAnswerScore}
		if s := scored[i]; s != nil {
			out[i] = answerRecord{
				Iteration:   it,
				Score:       100 * normalizeAnswerScore(s.Score, s.Reason),
				Fabrication: s.Fabrication,
				Reason:      s.Reason,
			}
		}
	}
	return out
}

// echoesEveryIndex reports whether the scores carry exactly the indexes 0 to n-1,
// each once.
func echoesEveryIndex(scores []answerScore, n int) bool {
	if len(scores) != n {
		return false
	}
	seen := make([]bool, n)
	for _, s := range scores {
		if s.Index == nil || *s.Index < 0 || *s.Index >= n || seen[*s.Index] {
			return false
		}
		seen[*s.Index] = true
	}
	return true
}

// transcriptKeysInOrder keeps the keys that are iteration transcripts and orders
// them by iteration number (not by name, which puts iteration 10 before 2).
func transcriptKeysInOrder(keys []string) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if iterationNumber(k) > 0 {
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool { return iterationNumber(out[i]) < iterationNumber(out[j]) })
	return out
}

// zeroRecords scores every iteration 0 with the reason.
func zeroRecords(iterations []int, reason string) []answerRecord {
	out := make([]answerRecord, len(iterations))
	for i, it := range iterations {
		out[i] = answerRecord{Iteration: it, Reason: reason}
	}
	return out
}

// sortAnswers orders records by iteration.
func sortAnswers(recs []answerRecord) {
	sort.Slice(recs, func(i, j int) bool { return recs[i].Iteration < recs[j].Iteration })
}

// answersMean is the mean score of the records, 0-100 (0 for none).
func answersMean(recs []answerRecord) float64 {
	if len(recs) == 0 {
		return 0
	}
	sum := 0.0
	for _, r := range recs {
		sum += r.Score
	}
	return math.Max(0, math.Min(100, sum/float64(len(recs))))
}

// answersReason is the most diagnostic reason of the records: that of the
// lowest-scoring judged answer that has one. The reason of a gated answer is the
// fallback, for a scenario with no judged answer.
func answersReason(recs []answerRecord) string {
	if reason := lowestReason(recs, false); reason != "" {
		return reason
	}
	return lowestReason(recs, true)
}

// lowestReason is the reason of the lowest-scoring record that has one, among
// the gated records or among the judged ones.
func lowestReason(recs []answerRecord, gated bool) string {
	reason := ""
	lowest := math.MaxFloat64
	for _, r := range recs {
		if r.Gated == gated && r.Reason != "" && r.Score <= lowest {
			lowest = r.Score
			reason = r.Reason
		}
	}
	return reason
}

// answerTally counts what a scenario's records hold.
type answerTally struct {
	answers, gated, fabrications int32
}

func tallyAnswers(recs []answerRecord) answerTally {
	t := answerTally{answers: int32(len(recs))}
	for _, r := range recs {
		if r.Gated {
			t.gated++
		}
		if r.Fabrication {
			t.fabrications++
		}
	}
	return t
}

// iterationNumber reads the iteration out of a transcript key, 0 when it is not one.
func iterationNumber(key string) int {
	_, iter, ok := parseTranscriptKey(key)
	if !ok || !strings.HasSuffix(key, ".json") {
		return 0
	}
	return iter
}
