// How reliably a suite's scenarios pass, shown beside the pass/fail counts:
// the mean pass^k (the chance all k trials pass), the scenarios flagged
// "check the scenario" (never passed), and whether the trials shared crew memory.

import type { CrewFitnessSuite } from '#lib/types/kubemoot.js';

type SuiteLike = Pick<CrewFitnessSuite, 'status'>;

/** Condition type the operator sets to say whether trials shared crew memory. */
const TRIALS_INDEPENDENT = 'TrialsIndependent';

export interface ConsistencyView {
	/** Mean pass^k as "42%", or "-" before any trial finishes. */
	passAllK: string;
	/** Scenarios that never passed across enough trials. */
	toCheck: number;
	/** Names of those scenarios, for a tooltip. */
	toCheckNames: string[];
	/** Regression suites only: scenarios below a 100% pass rate. */
	regressions?: number;
}

export function consistencyView(suite: SuiteLike): ConsistencyView {
	const c = suite.status?.consistency;
	const names = (suite.status?.scenarios ?? []).filter((s) => s.checkScenario).map((s) => s.name);
	return {
		passAllK: c?.meanPassAllK === undefined ? '-' : `${c.meanPassAllK}%`,
		toCheck: c?.scenariosToCheck ?? 0,
		toCheckNames: names,
		regressions: c?.regressions
	};
}

/**
 * The operator's statement about trial independence: undefined when the suite
 * has not started, otherwise whether the trials were independent and why not.
 */
export function trialsIndependence(suite: SuiteLike): { independent: boolean; reason: string } | undefined {
	const c = suite.status?.conditions?.find((x) => x.type === TRIALS_INDEPENDENT);
	if (!c) return undefined;
	return { independent: c.status === 'True', reason: c.message ?? c.reason ?? '' };
}
