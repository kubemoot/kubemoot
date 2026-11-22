// One health model for the whole dashboard: every tile and every list item is in one of five
// states, and a group takes the state of the worst thing beneath it.

import type { BadgeStatus } from './status.js';

export type Health = 'healthy' | 'degraded' | 'down' | 'idle' | 'unknown';

/** Worst first: a group is as unwell as its unwellest member. Idle is the quietest state. */
const SEVERITY: Record<Health, number> = { down: 4, degraded: 3, unknown: 2, healthy: 1, idle: 0 };

/** How unwell a state is: higher is worse, so a list can put the worst first. */
export function severityRank(state: Health): number {
	return SEVERITY[state];
}

/** The worst of the given states; idle when there are none (nothing is running, nothing is wrong). */
export function worstHealth(states: readonly Health[]): Health {
	let worst: Health = 'idle';
	for (const state of states) if (SEVERITY[state] > SEVERITY[worst]) worst = state;
	return worst;
}

/** True when the state needs a person's eyes: it is the only thing the "Needs attention" list shows. */
export function needsAttention(state: Health): boolean {
	return state === 'down' || state === 'degraded';
}

const LABELS: Record<Health, string> = {
	healthy: 'Healthy',
	degraded: 'Degraded',
	down: 'Down',
	idle: 'Idle',
	unknown: 'Unknown'
};

const BADGES: Record<Health, BadgeStatus> = {
	healthy: 'success',
	degraded: 'warning',
	down: 'error',
	idle: 'unknown',
	unknown: 'unknown'
};

/** The word a person reads for a state. */
export function healthLabel(state: Health): string {
	return LABELS[state];
}

/** The StatusBadge state that draws a health state. */
export function healthBadge(state: Health): BadgeStatus {
	return BADGES[state];
}

/**
 * A resource that reports `ready` and a `phase`: ready is healthy, a failure phase is down,
 * anything else (starting, waiting, not ready) is degraded.
 */
export function readyHealth(
	status: { ready?: boolean; phase?: string } | undefined,
	failedPhases: readonly string[] = ['Error', 'Failed']
): 'healthy' | 'degraded' | 'down' {
	if (status?.ready) return 'healthy';
	return status?.phase !== undefined && failedPhases.includes(status.phase) ? 'down' : 'degraded';
}
