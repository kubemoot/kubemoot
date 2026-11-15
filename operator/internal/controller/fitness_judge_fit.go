/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"encoding/json"
	"fmt"
	"sort"
	"unicode/utf8"

	"github.com/go-logr/logr"
)

// The judge reads one comparison document per scenario. Its question, reference and
// each answer's synthesis are the claims the judge checks and are always sent in
// full. The evidence behind the answers (tool output such as a hypervisor's
// per-VM metric rows) can be many times larger than the model's window, so when
// the document does not fit, the evidence is cut to fit and the cut is marked.
const (
	// judgeCharsPerToken is how many characters one token is taken to hold. It is
	// the dense ratio agent-runtime assumes for JSON and tool output (CharsPerToken.DENSE),
	// so the operator never counts text as fewer tokens than the runtime does.
	judgeCharsPerToken = 3.0

	// judgeMinReplyReserveTokens and judgeReplyReserveDivisor keep room for the judge's
	// reply the way agent-runtime does (ToolResultBudget.replyReserve): the larger of
	// 1024 tokens and an eighth of the window.
	judgeMinReplyReserveTokens = 1024
	judgeReplyReserveDivisor   = 8

	// judgePromptShareDivisor keeps a quarter of the window for what the judge crew
	// adds around the document: its system prompts, the discussion protocol and the
	// coordinator's framing.
	judgePromptShareDivisor = 4

	// judgeMinEvidenceKeepChars is the least an evidence item keeps when it is cut;
	// below it the item is replaced by the omission note alone.
	judgeMinEvidenceKeepChars = 120

	// judgeFitRounds bounds the passes that correct for JSON escaping and the
	// omission notes, which the first estimate does not count.
	judgeFitRounds = 4
)

// evidenceTrim records how much evidence was cut from a document, so the judge and a
// reader of the document know the evidence is not complete.
type evidenceTrim struct {
	Items        int `json:"items"`        // evidence items cut
	CharsOmitted int `json:"charsOmitted"` // characters left out of them
}

// judgeDocBudgetChars is how many characters of comparison document fit a judge
// whose model has a window of windowTokens tokens: the window less the reply
// reserve and the prompt share, at judgeCharsPerToken. ok is false when the window
// is unknown (zero or less), which means no limit is applied.
func judgeDocBudgetChars(windowTokens int) (chars int, ok bool) {
	if windowTokens <= 0 {
		return 0, false
	}
	reserve := max(judgeMinReplyReserveTokens, windowTokens/judgeReplyReserveDivisor)
	tokens := windowTokens - reserve - windowTokens/judgePromptShareDivisor
	return max(1, int(float64(tokens)*judgeCharsPerToken)), true
}

// marshalFitted returns cd as JSON that fits budgetChars when the evidence can be
// cut to make it so. Questions, references and syntheses are never cut: when they
// alone exceed the budget the document is sent with its evidence removed and the
// judge reports the window overrun as before. A budget of zero or less means no limit.
func marshalFitted(cd judgeComparisonDoc, budgetChars int, log logr.Logger) (string, error) {
	out, err := json.Marshal(cd)
	if err != nil {
		return "", fmt.Errorf("marshal comparison doc: %w", err)
	}
	if budgetChars <= 0 || len(out) <= budgetChars {
		return string(out), nil
	}
	original := evidenceItems(cd)
	rawTotal := 0
	for _, it := range original {
		rawTotal += len(it)
	}
	shrink := 0
	for round := 0; round < judgeFitRounds && len(out) > budgetChars; round++ {
		shrink += len(out) - budgetChars
		trimmed := trimEvidenceTo(cd, original, max(0, rawTotal-shrink))
		if out, err = json.Marshal(trimmed); err != nil {
			return "", fmt.Errorf("marshal comparison doc: %w", err)
		}
		if rawTotal-shrink <= 0 {
			break
		}
	}
	if len(out) > budgetChars {
		log.Info("deferred judge: comparison document still over the judge window after cutting evidence",
			"bytes", len(out), "budgetBytes", budgetChars)
	}
	return string(out), nil
}

// evidenceItems is every evidence item of every answer, in document order.
func evidenceItems(cd judgeComparisonDoc) []string {
	var items []string
	for _, a := range cd.Answers {
		items = append(items, a.Evidence...)
	}
	return items
}

// trimEvidenceTo returns a copy of cd whose evidence items, taken from original (the
// untrimmed items in document order), hold about targetChars characters in all. The
// longest items give up the most: every item longer than the common cap is cut to it.
func trimEvidenceTo(cd judgeComparisonDoc, original []string, targetChars int) judgeComparisonDoc {
	limit := evidenceCap(original, targetChars)
	out := cd
	out.Answers = make([]judgeAnswer, len(cd.Answers))
	trim := evidenceTrim{}
	next := 0
	for i, a := range cd.Answers {
		out.Answers[i] = a
		out.Answers[i].Evidence = make([]string, len(a.Evidence))
		for j := range a.Evidence {
			cut, omitted := cutEvidence(original[next], limit)
			next++
			out.Answers[i].Evidence[j] = cut
			if omitted > 0 {
				trim.Items++
				trim.CharsOmitted += omitted
			}
		}
	}
	if trim.Items > 0 {
		out.EvidenceTrimmed = &trim
	}
	return out
}

// evidenceCap is the largest per-item length at which the items together stay within
// targetChars: short items stay whole and the rest share what is left equally.
func evidenceCap(items []string, targetChars int) int {
	lengths := make([]int, len(items))
	for i, it := range items {
		lengths[i] = len(it)
	}
	sort.Ints(lengths)
	remaining := targetChars
	for i, l := range lengths {
		share := remaining / (len(lengths) - i)
		if l > share {
			return share
		}
		remaining -= l
	}
	return -1 // every item fits whole
}

// cutEvidence keeps the start and the end of an item longer than limit (the lead of
// tool output names what it is; the end often holds the totals) and says how many
// characters it left out. A limit below zero keeps the item whole.
func cutEvidence(item string, limit int) (string, int) {
	if limit < 0 || len(item) <= limit {
		return item, 0
	}
	if limit < judgeMinEvidenceKeepChars {
		n := utf8.RuneCountInString(item)
		return fmt.Sprintf("[%d characters of evidence omitted]", n), n
	}
	head := limit * 3 / 5
	tail := limit - head
	for head > 0 && !utf8.RuneStart(item[head]) {
		head--
	}
	start := len(item) - tail
	for start < len(item) && !utf8.RuneStart(item[start]) {
		start++
	}
	omitted := utf8.RuneCountInString(item[head:start])
	return fmt.Sprintf("%s [... %d characters of evidence omitted ...] %s", item[:head], omitted, item[start:]), omitted
}
