// Gates and judge score are two separate results of a suite. The gates are the
// script's deterministic assertions (passed, failed, errored); the score is the
// judge's rating of the answers against the references. They can disagree, so
// the pages label them apart and say why a suite or scenario did not pass.

import type { CrewFitnessSuite } from '#lib/types/kubemoot.js';

type SuiteLike = Pick<CrewFitnessSuite, 'status'>;

/** Condition type that carries the tool smoke result. */
const TOOL_SMOKE = 'ToolSmoke';

/** Text of the (i) note beside the gate and score headers. */
export const GATES_LEGEND: readonly string[] = [
	'Gates are the scenario script\'s own checks (the agree floor, required and forbidden terms). A trial passes when every gate holds.',
	'Judge score is the judge\'s 0 to 100 rating of the answers against the references. It does not read the gates.',
	'The two are independent: all gates can pass with a low score, and a gate can fail on an answer the judge liked.'
];

/** The judge's mean score as "42", or "-" until a scenario is judged. */
export function judgeScoreText(suite: SuiteLike): string {
	const mean = suite.status?.judge?.mean;
	return mean === undefined ? '-' : String(mean);
}

/** Band of a 0 to 100 score for colouring: ok at 70 and above, mid at 40 and above, else bad. */
export function scoreBandOf(n: number): 'ok' | 'mid' | 'bad' {
	if (n >= 70) return 'ok';
	if (n >= 40) return 'mid';
	return 'bad';
}

/**
 * Why a suite that did not execute says it failed: status.error for phase
 * Error (and Failed), the ToolSmoke condition message for ToolSmokeFailed.
 * Undefined for every other phase and when the operator wrote no reason.
 */
export function suiteFailureText(suite: SuiteLike): string | undefined {
	const s = suite.status;
	if (!s) return undefined;
	if (s.phase === 'Error' || s.phase === 'Failed') return s.error || undefined;
	if (s.phase === 'ToolSmokeFailed') {
		return s.conditions?.find((c) => c.type === TOOL_SMOKE)?.message || s.error || undefined;
	}
	return undefined;
}

export interface ScenarioFailure {
	name: string;
	reason: string;
}

/** The scenarios that carry a failureReason, in spec order. */
export function scenarioFailures(suite: SuiteLike): ScenarioFailure[] {
	return (suite.status?.scenarios ?? [])
		.filter((s) => !!s.failureReason)
		.map((s) => ({ name: s.name, reason: s.failureReason as string }));
}

/** failureReason of one scenario by name, or undefined. */
export function failureReasonOf(suite: SuiteLike, scenario: string): string | undefined {
	return suite.status?.scenarios?.find((s) => s.name === scenario)?.failureReason || undefined;
}
