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

package v1alpha1

import (
	"encoding/json"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// MootArchetypeSpec defines a coordination archetype — the phase vocabulary
// and state machine that a Crew's coordinator uses to run discussions.
//
// Today's default archetype is "consent-3" (sociocracy 3.0): phases
// triaging → evaluating → synthesis. Future archetypes (Robert's Rules,
// debate, expert panel, devil's advocate) each define their own vocabulary
// without changing the scheduler.
//
// CrewSchedulingPolicy.spec.rules[].phase is validated against the active
// archetype's Phases at admission time.
type MootArchetypeSpec struct {
	// Description is the human-readable explanation of this archetype.
	// +optional
	Description string `json:"description,omitempty"`

	// Phases is the ordered set of phase definitions emitted by a coordinator
	// running this archetype.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Phases []ArchetypePhase `json:"phases"`

	// Signals are the kinds of verdict an agent may record in a discussion, each with
	// the meaning the model is shown. The runtime offers every agent of a crew on this
	// archetype a signal tool whose kinds are these names. Absent, agents record their
	// verdict as before the tool existed (the reply's opening words).
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=16
	Signals []ArchetypeSignal `json:"signals,omitempty"`

	// Recheck declares the clarifying-question round: an agent that doubts a finding
	// asks the agent that made it, which re-runs its assessment and answers. Absent
	// means one round per thread.
	// +optional
	Recheck *ArchetypeRecheck `json:"recheck,omitempty"`

	// Review declares which analysts the coordinator convenes for the review
	// phase. Optional; absent means every analyst reviews.
	// +optional
	Review *ArchetypeReview `json:"review,omitempty"`

	// Ratify declares whether the agents that contributed to a turn ratify the
	// coordinator's draft answer before it is released, and how. Optional;
	// absent means they do not: ratification is opt-in and experimental.
	// +optional
	Ratify *ArchetypeRatify `json:"ratify,omitempty"`

	// StateMachine declares the legal phase transitions. Optional; absence
	// implies sequential progression through Phases in declaration order.
	// +optional
	StateMachine *ArchetypeStateMachine `json:"stateMachine,omitempty"`
}

// ArchetypePhase declares one phase by name plus optional metadata.
type ArchetypePhase struct {
	// Name of the phase as emitted by the coordinator and referenced from
	// CrewSchedulingPolicy.spec.rules[].phase.
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Role categorizes what the phase accomplishes (gatekeeping, deliberation,
	// closure, vote, …). Informational; the scheduler does not interpret it.
	// +optional
	Role string `json:"role,omitempty"`

	// Description is a one-line explanation shown in dashboards.
	// +optional
	Description string `json:"description,omitempty"`
}

// ArchetypeSignal declares one kind of verdict an agent may record.
type ArchetypeSignal struct {
	// Name is the kind the agent passes to the signal tool, such as agree or concern.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[a-z][a-z0-9_]*$`
	// +kubebuilder:validation:MaxLength=32
	Name string `json:"name"`

	// Meaning is what the kind says, in plain words; the model reads it in the
	// tool's description to choose a kind.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=300
	Meaning string `json:"meaning"`

	// Terminal is true when the kind ends the agent's turn in the phase, so the
	// phase can settle on it. A kind that is not terminal (a question) leaves the
	// agent's turn open.
	// +kubebuilder:default=true
	// +optional
	Terminal *bool `json:"terminal,omitempty"`

	// Addressed is true when the kind is said to one named agent of the crew, so
	// the tool call must name it (a question to the agent that made a finding).
	// +optional
	Addressed bool `json:"addressed,omitempty"`

	// StartsRecheck is true when the kind, said to a named agent, starts that agent's
	// recheck: the agent is asked to run its assessment again and answer, and the agent
	// that said the kind signals again. It applies within recheck.maxRounds. The kind
	// stays a note on the table when it names no agent, names one that is not in the
	// crew, or no round is left. A kind that is not addressed ignores it. Unset keeps
	// the earlier behavior: only a question starts a recheck.
	// +optional
	StartsRecheck bool `json:"startsRecheck,omitempty"`
}

// UnmarshalJSON accepts the earlier form of a signal, a bare name, as well as the
// object. An archetype stored before signals were declared keeps decoding; a bare
// name has no meaning, so it is never offered to a model.
func (s *ArchetypeSignal) UnmarshalJSON(data []byte) error {
	var name string
	if err := json.Unmarshal(data, &name); err == nil {
		*s = ArchetypeSignal{Name: name}
		return nil
	}
	type plain ArchetypeSignal
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*s = ArchetypeSignal(p)
	return nil
}

// IsTerminal reports whether the kind ends the agent's turn; unset means it does.
func (s ArchetypeSignal) IsTerminal() bool {
	return s.Terminal == nil || *s.Terminal
}

// ArchetypeRecheck declares the clarifying-question round.
type ArchetypeRecheck struct {
	// MaxRounds is the most questions relayed per thread. 0 turns the round off:
	// a question is then recorded as a concern.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=5
	// +kubebuilder:default=1
	// +optional
	MaxRounds *int32 `json:"maxRounds,omitempty"`
}

// ReviewConvene says which analysts review a discussion.
// +kubebuilder:validation:Enum=all;domain
type ReviewConvene string

// ReviewFallback says who reviews when no analyst matches by domain.
// +kubebuilder:validation:Enum=primary;all
type ReviewFallback string

// ReviewGeneralists says whether an analyst that declares no domain reviews every discussion.
// +kubebuilder:validation:Enum=always;selected
type ReviewGeneralists string

// ReviewOrder says whether the convened analysts review together or the generalists first.
// +kubebuilder:validation:Enum=together;generalistsFirst
type ReviewOrder string

const (
	// ReviewOrderTogether convenes every reviewer at once.
	ReviewOrderTogether ReviewOrder = "together"
	// ReviewOrderGeneralistsFirst convenes the analysts that declare no domain first; the
	// analysts that declare a domain are convened once every generalist has replied, and see
	// the generalists' findings and stored working.
	ReviewOrderGeneralistsFirst ReviewOrder = "generalistsFirst"
)

const (
	// ReviewGeneralistsAlways has an analyst that declares no domain review every discussion.
	ReviewGeneralistsAlways ReviewGeneralists = "always"
	// ReviewGeneralistsSelected has an analyst that declares no domain review only the
	// discussions where the coordinator selected it for the question.
	ReviewGeneralistsSelected ReviewGeneralists = "selected"
)

const (
	// ReviewConveneAll convenes every analyst of the crew.
	ReviewConveneAll ReviewConvene = "all"
	// ReviewConveneDomain convenes only the analysts whose declared domains
	// touch the question or the data the toolers gathered.
	ReviewConveneDomain ReviewConvene = "domain"
	// ReviewFallbackPrimary convenes the single analyst that best matches.
	ReviewFallbackPrimary ReviewFallback = "primary"
	// ReviewFallbackAll convenes every analyst.
	ReviewFallbackAll ReviewFallback = "all"
)

// ArchetypeReview declares who reviews. The review phase never runs with no
// reviewer while the crew has an analyst: the fallback names who is convened
// when no analyst matches.
type ArchetypeReview struct {
	// Convene is "all" (every analyst reviews) or "domain" (only analysts whose
	// declared domains touch the question or the gathered data).
	// +kubebuilder:default=all
	// +optional
	Convene ReviewConvene `json:"convene,omitempty"`

	// Fallback applies when Convene is "domain" and no analyst matches: "primary"
	// convenes the best-matching analyst alone, "all" convenes every analyst.
	// +kubebuilder:default=primary
	// +optional
	Fallback ReviewFallback `json:"fallback,omitempty"`

	// Generalists applies when Convene is "domain" and says whether an analyst that
	// declares no domain (a cross-cutting reviewer such as arithmetic) reviews every
	// discussion ("always") or only those where the coordinator selected it ("selected").
	// +kubebuilder:default=always
	// +optional
	Generalists ReviewGeneralists `json:"generalists,omitempty"`

	// Order is "together" (every convened analyst reviews at once) or "generalistsFirst"
	// (the convened analysts that declare no domain review first; the convened analysts
	// that declare a domain review after every generalist has replied, with the
	// generalists' findings and the working behind them in front of them). One pass runs
	// when the convened analysts are all generalists or all domain analysts.
	// +kubebuilder:default=together
	// +optional
	Order ReviewOrder `json:"order,omitempty"`
}

// RatifyRoster says who ratifies the draft.
// +kubebuilder:validation:Enum=contributors;crew
type RatifyRoster string

const (
	// RatifyRosterContributors asks every Tooler with a terminal agree, concern
	// or block in the turn and every Analyst that reviewed.
	RatifyRosterContributors RatifyRoster = "contributors"
	// RatifyRosterCrew also asks every other Tooler and Analyst of the crew.
	RatifyRosterCrew RatifyRoster = "crew"
)

// ArchetypeRatify declares how the draft answer is ratified. The coordinator's
// synthesis is a draft: the roster checks it against what they found, the
// coordinator revises on their objections, and the answer is released when the
// roster has ratified or the rounds are used up, with any standing objection
// attached.
type ArchetypeRatify struct {
	// Enabled turns ratification on or off. Ratification is off by default,
	// whether the block is absent or omits this field; true opts the crew in,
	// and the draft is otherwise released as the answer.
	// +kubebuilder:default=false
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// MaxRounds is the most rounds the draft is put to the table: round 1 is
	// the draft and each further round a revision on the objections. The default
	// of 2 revises once.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=5
	// +kubebuilder:default=2
	// +optional
	MaxRounds *int32 `json:"maxRounds,omitempty"`

	// Roster is "contributors" (the agents that contributed to the turn) or
	// "crew" (every Tooler and Analyst of the crew).
	// +kubebuilder:default=contributors
	// +optional
	Roster RatifyRoster `json:"roster,omitempty"`
}

// ArchetypeStateMachine declares the legal phase transitions.
type ArchetypeStateMachine struct {
	// Initial is the name of the starting phase.
	// +kubebuilder:validation:Required
	Initial string `json:"initial"`

	// Transitions are the legal edges of the state machine.
	// +kubebuilder:validation:MinItems=1
	Transitions []PhaseTransition `json:"transitions"`
}

// PhaseTransition is one edge in the state machine.
type PhaseTransition struct {
	// From phase name.
	// +kubebuilder:validation:Required
	From string `json:"from"`

	// To phase name.
	// +kubebuilder:validation:Required
	To string `json:"to"`

	// On is the trigger condition (e.g., "all-triaged", "settled",
	// "majority-vote"). Informational; the coordinator interprets it.
	// +optional
	On string `json:"on,omitempty"`
}

// MootArchetypeStatus reflects validation state.
type MootArchetypeStatus struct {
	// Valid is true when the spec parses cleanly (Phases referenced by
	// StateMachine edges all exist, no cycles in sequential interpretation).
	// +optional
	Valid bool `json:"valid,omitempty"`

	// Message provides additional status information.
	// +optional
	Message string `json:"message,omitempty"`

	// Conditions represent the latest available observations.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=moot
// +kubebuilder:printcolumn:name="Phases",type=string,JSONPath=`.spec.phases[*].name`
// +kubebuilder:printcolumn:name="Valid",type=boolean,JSONPath=`.status.valid`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// MootArchetype declares a coordination archetype's phase vocabulary and
// state machine. Cluster-scoped — one MootArchetype CR can be referenced
// by many CrewSchedulingPolicy CRs across namespaces.
//
// See kubemoot/docs/scheduler-v2.md and kubemoot/docs/agentic-consensus.md
// (the consent-3 archetype's semantics).
type MootArchetype struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MootArchetypeSpec   `json:"spec,omitempty"`
	Status MootArchetypeStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// MootArchetypeList contains a list of MootArchetype.
type MootArchetypeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MootArchetype `json:"items"`
}

func init() {
	registerTypes(&MootArchetype{}, &MootArchetypeList{})
}
