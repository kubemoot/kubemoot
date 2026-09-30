// Pause, resume and stop for a CrewFitnessSuite. The operator reads
// spec.suspend (pause between iterations) and spec.cancel (stop the suite);
// the dashboard only flips those two fields with a merge patch.

import type { CrewFitnessSuite } from '$types/kubemoot.js';

export type SuiteAction = 'pause' | 'resume' | 'stop';

/** Terminal suite phases whose runs the post-suite quality judge scores. */
const SUITE_JUDGED_PHASES: ReadonlySet<string> = new Set(['Completed', 'Failed', 'Error']);

/** Suite phases after which the operator schedules nothing more. */
export const SUITE_TERMINAL_PHASES: ReadonlySet<string> = new Set([...SUITE_JUDGED_PHASES, 'Cancelled']);

/**
 * The spec merge patch for an action, or null for an unknown action. Resume
 * clears suspend; stop sets cancel and leaves suspend as it is (cancel wins).
 */
export function suiteActionPatch(action: unknown): { spec: { suspend?: boolean; cancel?: boolean } } | null {
	switch (action) {
		case 'pause':
			return { spec: { suspend: true } };
		case 'resume':
			return { spec: { suspend: false } };
		case 'stop':
			return { spec: { cancel: true } };
		default:
			return null;
	}
}

/** The actions that apply to a suite in its current state, in button order. */
export function availableSuiteActions(suite: Pick<CrewFitnessSuite, 'spec' | 'status'>): SuiteAction[] {
	const phase = suite.status?.phase ?? '';
	if (SUITE_TERMINAL_PHASES.has(phase) || suite.spec.cancel) return [];
	return [suite.spec.suspend ? 'resume' : 'pause', 'stop'];
}

/**
 * The status label to show. A suspended suite still reads Running from the
 * operator until its in-flight iteration finishes; show that as Pausing. A
 * cancel that the operator has not yet applied shows as Stopping.
 */
export function suiteDisplayPhase(suite: Pick<CrewFitnessSuite, 'spec' | 'status'>): string | undefined {
	const phase = suite.status?.phase;
	if (SUITE_TERMINAL_PHASES.has(phase ?? '')) return phase;
	if (suite.spec.cancel) return 'Stopping';
	if (suite.spec.suspend && phase === 'Running') return 'Pausing';
	return phase;
}

/**
 * Whether the post-suite quality judge runs for this phase. A Cancelled suite
 * is never judged, so the UI must not poll or show "judging" for it.
 */
export function suiteIsJudged(phase?: string): boolean {
	return SUITE_JUDGED_PHASES.has(phase ?? '');
}
