import { describe, expect, it } from 'vitest';
import {
	AGENT_HEARTBEAT_INTERVAL_SECONDS,
	HEARTBEAT_STALE_AFTER_SECONDS,
	LIVENESS_BADGE,
	secondsSince,
	heartbeatLiveness
} from './agent-liveness';

describe('heartbeat thresholds', () => {
	it('match the agent-runtime heartbeat interval, stale after two intervals', () => {
		expect(AGENT_HEARTBEAT_INTERVAL_SECONDS).toBe(60);
		expect(HEARTBEAT_STALE_AFTER_SECONDS).toBe(120);
	});
});

describe('heartbeatLiveness', () => {
	const healthy = { nats: true, ollama: true };

	it('is live for a heartbeat within the threshold with both dependencies reachable', () => {
		expect(heartbeatLiveness(30, healthy)).toBe('live');
		expect(heartbeatLiveness(HEARTBEAT_STALE_AFTER_SECONDS, healthy)).toBe('live');
	});

	it('is degraded when Ollama or NATS is unreachable', () => {
		expect(heartbeatLiveness(30, { nats: false, ollama: true })).toBe('degraded');
		expect(heartbeatLiveness(30, { nats: true, ollama: false })).toBe('degraded');
	});

	it('is stale past the threshold whatever the dependencies', () => {
		const past = HEARTBEAT_STALE_AFTER_SECONDS + 1;
		expect(heartbeatLiveness(past, healthy)).toBe('stale');
		expect(heartbeatLiveness(past, { nats: false, ollama: false })).toBe('stale');
		expect(heartbeatLiveness(300, healthy)).toBe('stale');
	});

	it('maps every state to a badge', () => {
		expect(LIVENESS_BADGE.live).toEqual({ status: 'success', label: 'Live' });
		expect(LIVENESS_BADGE.degraded).toEqual({ status: 'warning', label: 'Degraded' });
		expect(LIVENESS_BADGE.stale).toEqual({ status: 'error', label: 'Stale' });
	});
});

describe('secondsSince', () => {
	const now = Date.parse('2026-10-01T12:00:00Z');

	it('rounds the age to whole seconds', () => {
		expect(secondsSince('2026-10-01T11:58:59.600Z', now)).toBe(60);
	});

	it('is NaN for an unparseable timestamp, which counts as stale', () => {
		const age = secondsSince('not a date', now);
		expect(age).toBeNaN();
		expect(heartbeatLiveness(age, { nats: true, ollama: true })).toBe('stale');
	});
});
