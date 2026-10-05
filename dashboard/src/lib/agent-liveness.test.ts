import { describe, expect, it } from 'vitest';
import {
	FALLBACK_HEARTBEAT_INTERVAL_SECONDS,
	MAX_HEARTBEAT_INTERVAL_SECONDS,
	heartbeatIntervalSeconds,
	staleAfterSeconds,
	LIVENESS_BADGE,
	secondsSince,
	heartbeatLiveness
} from './agent-liveness';

describe('heartbeatIntervalSeconds', () => {
	it('uses the interval the heartbeat carries', () => {
		expect(heartbeatIntervalSeconds(15)).toBe(15);
		expect(heartbeatIntervalSeconds(MAX_HEARTBEAT_INTERVAL_SECONDS)).toBe(
			MAX_HEARTBEAT_INTERVAL_SECONDS
		);
	});

	it('falls back for a missing, zero, negative, huge, or non-numeric interval', () => {
		const fallback = FALLBACK_HEARTBEAT_INTERVAL_SECONDS;
		for (const bad of [undefined, null, 0, -30, MAX_HEARTBEAT_INTERVAL_SECONDS + 1, 1e12,
			NaN, Infinity, '30', {}]) {
			expect(heartbeatIntervalSeconds(bad)).toBe(fallback);
		}
	});
});

describe('staleAfterSeconds', () => {
	it('is two intervals', () => {
		expect(staleAfterSeconds(15)).toBe(30);
		expect(staleAfterSeconds(undefined)).toBe(2 * FALLBACK_HEARTBEAT_INTERVAL_SECONDS);
	});
});

describe('heartbeatLiveness', () => {
	const healthy = { nats: true, ollama: true };

	it('is live for a heartbeat within the threshold with both dependencies reachable', () => {
		expect(heartbeatLiveness(30, healthy)).toBe('live');
		expect(heartbeatLiveness(120, healthy)).toBe('live');
	});

	it('uses the heartbeat own interval for the threshold', () => {
		const fast = { ...healthy, intervalSeconds: 10 };
		expect(heartbeatLiveness(20, fast)).toBe('live');
		expect(heartbeatLiveness(21, fast)).toBe('stale');
		const slow = { ...healthy, intervalSeconds: 300 };
		expect(heartbeatLiveness(500, slow)).toBe('live');
		expect(heartbeatLiveness(601, slow)).toBe('stale');
	});

	it('uses the 60 s fallback for a missing, zero, negative, or huge interval', () => {
		for (const intervalSeconds of [undefined, 0, -10, 1e9]) {
			const hb = { ...healthy, intervalSeconds };
			expect(heartbeatLiveness(120, hb)).toBe('live');
			expect(heartbeatLiveness(121, hb)).toBe('stale');
		}
	});

	it('is degraded when Ollama or NATS is unreachable', () => {
		expect(heartbeatLiveness(30, { nats: false, ollama: true })).toBe('degraded');
		expect(heartbeatLiveness(30, { nats: true, ollama: false })).toBe('degraded');
	});

	it('is stale past the threshold whatever the dependencies', () => {
		const past = 121;
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
