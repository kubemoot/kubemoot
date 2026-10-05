// Agent liveness from the heartbeat each agent writes to the kubemoot_agent_state KV bucket.

import type { AgentHeartbeat } from '#lib/types/kubemoot.js';

/** Interval assumed for a heartbeat that carries no usable intervalSeconds (older agents). */
export const FALLBACK_HEARTBEAT_INTERVAL_SECONDS = 60;

/** Intervals above this (one hour) are treated as corrupt and replaced by the fallback. */
export const MAX_HEARTBEAT_INTERVAL_SECONDS = 3600;

/** The heartbeat's own interval, or the fallback when it is missing, non-numeric, not positive, or implausibly large. */
export function heartbeatIntervalSeconds(intervalSeconds: unknown): number {
	if (
		typeof intervalSeconds !== 'number' ||
		!Number.isFinite(intervalSeconds) ||
		intervalSeconds <= 0 ||
		intervalSeconds > MAX_HEARTBEAT_INTERVAL_SECONDS
	) {
		return FALLBACK_HEARTBEAT_INTERVAL_SECONDS;
	}
	return intervalSeconds;
}

/**
 * A heartbeat is stale once two of its own intervals pass without a newer one: the next
 * beat was due and a further interval of slack went by without it. The KV entry itself
 * expires later (the bucket TTL is 300 s), at which point there is no heartbeat at all.
 */
export function staleAfterSeconds(intervalSeconds: unknown): number {
	return 2 * heartbeatIntervalSeconds(intervalSeconds);
}

export type HeartbeatLiveness = 'live' | 'degraded' | 'stale';

/** How the liveness of a heartbeat shows as a badge. */
export const LIVENESS_BADGE: Record<
	HeartbeatLiveness,
	{ status: 'success' | 'warning' | 'error'; label: string }
> = {
	live: { status: 'success', label: 'Live' },
	degraded: { status: 'warning', label: 'Degraded' },
	stale: { status: 'error', label: 'Stale' }
};

/** Whole seconds since an ISO timestamp, such as a heartbeat's or its last inference. */
export function secondsSince(timestamp: string, now: number = Date.now()): number {
	return Math.round((now - new Date(timestamp).getTime()) / 1000);
}

/** Stale past two of the heartbeat's own intervals (or with no readable age), degraded when Ollama or NATS is unreachable, live otherwise. */
export function heartbeatLiveness(
	ageSeconds: number,
	heartbeat: Pick<AgentHeartbeat, 'nats' | 'ollama' | 'intervalSeconds'>
): HeartbeatLiveness {
	if (Number.isNaN(ageSeconds) || ageSeconds > staleAfterSeconds(heartbeat.intervalSeconds)) return 'stale';
	return heartbeat.ollama && heartbeat.nats ? 'live' : 'degraded';
}
