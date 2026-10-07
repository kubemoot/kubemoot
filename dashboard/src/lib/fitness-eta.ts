// Estimated time remaining and finish time for a running fitness suite.

import type { CrewFitnessSuite } from '#lib/types/kubemoot.js';
import { SUITE_TERMINAL_PHASES } from '#lib/fitness-suite-controls.js';

export interface SuiteEta {
	/** Estimated milliseconds until the suite finishes (0 when every iteration is done). */
	remainingMs: number;
	/** Estimated finish as epoch milliseconds. */
	finishAtMs: number;
	/** Elapsed time per completed iteration used for the estimate. */
	meanMs: number;
}

function toMs(iso?: string): number {
	return iso ? new Date(iso).getTime() : Number.NaN;
}

function positiveCount(n?: number): number {
	return typeof n === 'number' && Number.isFinite(n) && n > 0 ? Math.floor(n) : 0;
}

/** Mean of the finite, positive durations, or 0 when there are none. */
export function meanDurationMs(durationsMs: readonly number[]): number {
	const valid = durationsMs.filter((d) => Number.isFinite(d) && d > 0);
	if (valid.length === 0) return 0;
	return valid.reduce((a, b) => a + b, 0) / valid.length;
}

function isFinished(s: NonNullable<CrewFitnessSuite['status']>): boolean {
	return !!s.completedAt || SUITE_TERMINAL_PHASES.has(s.phase ?? '');
}

/**
 * Time left for a running suite: (iterationsTotal - iterationsCompleted) x the
 * suite's elapsed time divided by its completed iterations. Elapsed time
 * includes the per-iteration overhead the wall-clock finish depends on.
 * Returns undefined when no estimate can be made: the suite has finished, has
 * no usable start time, or has not completed an iteration yet.
 */
export function suiteEta(
	status: CrewFitnessSuite['status'] | undefined,
	now = Date.now()
): SuiteEta | undefined {
	if (!status || isFinished(status)) return undefined;
	const completed = positiveCount(status.iterationsCompleted);
	const total = positiveCount(status.iterationsTotal);
	if (completed === 0 || total === 0) return undefined;
	const elapsed = now - toMs(status.startedAt);
	if (!Number.isFinite(elapsed) || elapsed <= 0) return undefined;
	const meanMs = elapsed / completed;
	const remainingMs = Math.round(Math.max(0, total - completed) * meanMs);
	return { remainingMs, finishAtMs: now + remainingMs, meanMs: Math.round(meanMs) };
}

/** Remaining time rounded to the minute ("1h 20m", "5m") or "under 1m". */
export function fmtRemaining(ms: number): string {
	const minutes = Math.round(ms / 60_000);
	if (minutes < 1) return 'under 1m';
	const h = Math.floor(minutes / 60);
	return h > 0 ? `${h}h ${minutes % 60}m` : `${minutes}m`;
}

/** Text for the estimate: "~1h 20m left, finish ~14:32 (estimate)". */
export function etaText(eta: SuiteEta): string {
	const left = fmtRemaining(eta.remainingMs);
	const clock = new Date(eta.finishAtMs).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
	return `~${left} left, finish ~${clock} (estimate)`;
}
