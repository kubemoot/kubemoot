// Agent liveness from the heartbeat each agent writes to the kubemoot_agent_state KV bucket.

import type { AgentHeartbeat } from '#lib/types/kubemoot.js';

/**
 * How often an agent writes its heartbeat: agent-runtime AgentHeartbeatService runs
 * at kubemoot.heartbeat.interval-seconds, default 60 (AgentProperties.Heartbeat), and
 * the operator does not override it.
 */
export const AGENT_HEARTBEAT_INTERVAL_SECONDS = 60;

/**
 * A heartbeat is stale once two intervals pass without a newer one: the next beat
 * was due and a further interval of slack went by without it. The KV entry itself
 * expires later (the bucket TTL is 300 s), at which point there is no heartbeat at all.
 */
export const HEARTBEAT_STALE_AFTER_SECONDS = 2 * AGENT_HEARTBEAT_INTERVAL_SECONDS;

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

/** Stale past the threshold (or with no readable age), degraded when Ollama or NATS is unreachable, live otherwise. */
export function heartbeatLiveness(
	ageSeconds: number,
	heartbeat: Pick<AgentHeartbeat, 'nats' | 'ollama'>
): HeartbeatLiveness {
	if (Number.isNaN(ageSeconds) || ageSeconds > HEARTBEAT_STALE_AFTER_SECONDS) return 'stale';
	return heartbeat.ollama && heartbeat.nats ? 'live' : 'degraded';
}
