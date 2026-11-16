/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"github.com/kubemoot/kubemoot/operator/pkg/evidencenames"
)

// inventedNamesFromTranscript returns the resource names the run's answer
// asserts that no tool result of the run held: the names of the synthesis, less
// the ones in the tool results the runner captured, the question, the
// assertions (the scenario's reference and terms) and the crew's own names (the
// agents and accelerators of the stream). It returns nil when the transcript has
// no captured tool results, because without them an absent name proves nothing.
// It is a flag: it changes no score and no verdict.
func inventedNamesFromTranscript(doc transcriptDoc, synthesis string) []string {
	if !doc.EvidenceCaptured || synthesis == "" {
		return nil
	}
	return evidencenames.Invented(synthesis,
		evidencenames.EvidenceFromTokens(doc.EvidenceNames), allowedNames(doc))
}

// allowedNames are the tokens the answer may repeat without having read them
// from a tool: those of the question, the assertions and the stream's agents.
func allowedNames(doc transcriptDoc) *evidencenames.Evidence {
	ok := evidencenames.NewEvidence()
	ok.Add(doc.Question)
	for _, a := range doc.Assertions {
		ok.Add(a.Raw)
	}
	for _, e := range doc.Events {
		ok.Add(e.Agent)
		ok.Add(e.GPU)
	}
	return ok
}
