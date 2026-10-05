// The CrewFitness children of a suite run, as the Fitness page tracks them.

import { SUITE_TERMINAL_PHASES } from '#lib/fitness-suite-controls.js';

export const SUITE_LABEL = 'kubemoot.ai/fitness-suite';

// Terminal phases of both suites and their CrewFitness children (Passed is the
// one child phase that is not also a suite phase).
export const TERMINAL_PHASES: ReadonlySet<string> = new Set([...SUITE_TERMINAL_PHASES, 'Passed']);

export interface FitnessTest {
	metadata: {
		name: string;
		namespace: string;
		labels?: Record<string, string>;
		creationTimestamp?: string;
	};
	spec?: { testRef?: string };
	status?: { phase?: string; startedAt?: string; durationMs?: number };
}

/** The suite id (ns/name) a CrewFitness belongs to, or null for a standalone test. */
export function childSuiteId(t: FitnessTest): string | null {
	const suite = t.metadata.labels?.[SUITE_LABEL];
	if (!suite) return null;
	return `${t.metadata.namespace ?? ''}/${suite}`;
}

/** The suite children still in flight, grouped by suite id. */
export function runningBySuite(tests: FitnessTest[]): Record<string, FitnessTest[]> {
	const running: Record<string, FitnessTest[]> = {};
	for (const t of tests) {
		const id = childSuiteId(t);
		if (!id || TERMINAL_PHASES.has(t.status?.phase ?? '')) continue;
		running[id] ??= [];
		running[id].push(t);
	}
	return running;
}
