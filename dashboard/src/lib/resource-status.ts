// How a Kubemoot resource's status maps to the StatusBadge states, shared by the
// resource cards and the detail pages so the two views always agree.

import type { BadgeStatus } from './status.js';

export type ReadinessStatus = 'success' | 'error' | 'pending';

/** Mark shown for an unset value in tables and info rows (an em dash). */
export const NO_VALUE = '\u2014';

/** success when ready, error in the failure phase, pending otherwise. */
export function readinessStatus(
	status: { ready?: boolean; phase?: string } | undefined,
	failedPhase = 'Error'
): ReadinessStatus {
	if (status?.ready) return 'success';
	return status?.phase === failedPhase ? 'error' : 'pending';
}

/** success in the Ready phase, error in the Error phase, pending otherwise. */
export function phaseStatus(phase: string | undefined): ReadinessStatus {
	return readinessStatus({ ready: phase === 'Ready', phase });
}

/** An MCPServerReport verdict as a badge state: use, avoid, caution, or not yet tested. */
export function verdictStatus(
	verdict: string | undefined
): 'success' | 'error' | 'warning' | 'pending' {
	switch (verdict) {
		case 'use':
			return 'success';
		case 'avoid':
			return 'error';
		case 'caution':
			return 'warning';
		default:
			return 'pending';
	}
}

/** A ready-of-total count as a badge state: none to show, all, some, or none ready. */
export function readyCountStatus(
	ready: number,
	total: number
): 'unknown' | 'success' | 'warning' | 'error' {
	if (total === 0) return 'unknown';
	if (ready === total) return 'success';
	return ready > 0 ? 'warning' : 'error';
}

/** Yes or No for a known readiness flag, NO_VALUE when it is unset. */
export function yesNo(flag: boolean | undefined): string {
	if (flag === undefined) return NO_VALUE;
	return flag ? 'Yes' : 'No';
}

/** The phase of a model server with no ready endpoint: a state, not a fault. */
export const UNAVAILABLE_PHASE = 'Unavailable';

/** True for a model server with no ready endpoint: phase Unavailable and not ready. */
export function isModelServerUnavailable(status: { ready?: boolean; phase?: string } | undefined): boolean {
	return !status?.ready && status?.phase === UNAVAILABLE_PHASE;
}

/** A model server's badge: Unavailable is neutral, Failed is an error, ready is success. */
export function modelServerStatus(status: { ready?: boolean; phase?: string } | undefined): BadgeStatus {
	if (isModelServerUnavailable(status)) return 'unknown';
	return readinessStatus(status, 'Failed');
}
