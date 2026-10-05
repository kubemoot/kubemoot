import { describe, expect, it } from 'vitest';
import type { CrewFitnessSuite } from '#lib/types/kubemoot.js';
import { NO_VALUE } from './resource-status';
import {
	durTitle,
	fmtDateTime,
	fmtDur,
	formatBytes,
	iterDurText,
	suiteDurMs,
	suiteMarkdown
} from './fitness-format';

describe('fmtDur', () => {
	it('formats milliseconds, seconds, and minutes', () => {
		expect(fmtDur(850)).toBe('850ms');
		expect(fmtDur(12_340)).toBe('12.3s');
		expect(fmtDur(125_000)).toBe('2m 5s');
	});

	it('shows the no-value mark for zero or a missing duration', () => {
		expect(fmtDur(0)).toBe(NO_VALUE);
		expect(fmtDur()).toBe(NO_VALUE);
	});
});

describe('formatBytes', () => {
	it('formats bytes, KB, and MB', () => {
		expect(formatBytes(512)).toBe('512 B');
		expect(formatBytes(2048)).toBe('2.0 KB');
		expect(formatBytes(3 * 1024 * 1024)).toBe('3.00 MB');
		expect(formatBytes()).toBe(NO_VALUE);
	});
});

describe('fmtDateTime and durTitle', () => {
	it('shows the no-value mark for a missing or invalid time', () => {
		expect(fmtDateTime()).toBe(NO_VALUE);
		expect(fmtDateTime('not a time')).toBe(NO_VALUE);
		expect(fmtDateTime('2026-09-30T12:00:00Z')).toBe(
			new Date('2026-09-30T12:00:00Z').toLocaleString()
		);
	});

	it('describes a finished, running, or unstarted run', () => {
		const start = '2026-09-30T12:00:00Z';
		expect(durTitle(start, start)).toBe(
			`Started: ${fmtDateTime(start)}\nEnded: ${fmtDateTime(start)}`
		);
		expect(durTitle(start)).toMatch(/Ended: running$/);
		expect(durTitle()).toBe('No start time recorded');
	});
});

describe('suiteDurMs', () => {
	it('measures to completion, or to now while running', () => {
		const startedAt = '2026-09-30T12:00:00Z';
		expect(suiteDurMs({ startedAt, completedAt: '2026-09-30T12:01:00Z' })).toBe(60_000);
		expect(suiteDurMs({ startedAt }, Date.parse(startedAt) + 5000)).toBe(5000);
		expect(suiteDurMs({})).toBeUndefined();
		expect(suiteDurMs()).toBeUndefined();
	});
});

describe('suiteMarkdown', () => {
	const base = {
		metadata: { name: 'nightly', namespace: 'crew-a' },
		spec: { crewRef: 'homelab-pilot', scripts: [{ testRef: 'gpu' }, { testRef: 'pods' }] }
	} as unknown as CrewFitnessSuite;

	it('lists every set field of a finished, judged suite', () => {
		const suite = {
			...base,
			metadata: { ...base.metadata, creationTimestamp: '2026-09-30T11:59:00Z' },
			spec: { ...base.spec, description: 'baseline' },
			status: {
				phase: 'Completed',
				iterationsCompleted: 4,
				iterationsTotal: 4,
				passed: 3,
				failed: 1,
				errored: 0,
				startedAt: '2026-09-30T12:00:00Z',
				completedAt: '2026-09-30T12:02:05Z',
				runId: 'r1',
				artifactRef: { objectKey: 'crew-a/nightly/r1.xlsx', sizeBytes: 2048 }
			}
		} as unknown as CrewFitnessSuite;
		expect(suiteMarkdown(suite, { complete: false, judged: 1 })).toBe(
			[
				'# Fitness Suite: nightly',
				'- Crew: homelab-pilot',
				'- Namespace: crew-a',
				'- Description: baseline',
				'- Phase: Completed',
				'- Iterations: 4 / 4',
				'- Results: 3 passed / 1 failed / 0 errored',
				'- Duration: 2m 5s',
				'- Scenarios (2): gpu, pods',
				'- Judge: judging 1/2',
				'- Run ID: r1',
				'- Created: 2026-09-30T11:59:00Z',
				'- Artifact: crew-a/nightly/r1.xlsx (2.0 KB)'
			].join('\n')
		);
	});

	it('defaults a suite without status and leaves out the unset lines', () => {
		expect(suiteMarkdown(base, undefined)).toBe(
			[
				'# Fitness Suite: nightly',
				'- Crew: homelab-pilot',
				'- Namespace: crew-a',
				'- Phase: Unknown',
				'- Iterations: 0 / 0',
				'- Results: 0 passed / 0 failed / 0 errored',
				'- Duration: -',
				'- Scenarios (2): gpu, pods'
			].join('\n')
		);
	});

	it('reports a completed judge', () => {
		expect(suiteMarkdown(base, { complete: true, judged: 2 })).toContain('- Judge: complete');
	});
});

describe('iterDurText', () => {
	const start = '2026-10-04T12:00:00Z';
	const t0 = new Date(start).getTime();

	it('shows the time elapsed since start for a running iteration', () => {
		expect(iterDurText({ running: true, startedAt: start }, t0 + 125_000)).toBe('2m 5s');
		expect(iterDurText({ running: true, startedAt: start }, t0 + 12_300)).toBe('12.3s');
	});

	it('advances as the clock ticks', () => {
		const it0 = { running: true, startedAt: start };
		expect(iterDurText(it0, t0 + 61_000)).toBe('1m 1s');
		expect(iterDurText(it0, t0 + 62_000)).toBe('1m 2s');
	});

	it('keeps the final duration for a completed iteration, ignoring the clock', () => {
		expect(iterDurText({ durationMs: 90_000, startedAt: start }, t0 + 999_999)).toBe('1m 30s');
		expect(iterDurText({ running: false, durationMs: 4_200 })).toBe('4.2s');
	});

	it('shows the no-value mark for a completed iteration without a duration', () => {
		expect(iterDurText({ durationMs: 0 })).toBe(NO_VALUE);
		expect(iterDurText({})).toBe(NO_VALUE);
	});

	it('shows the no-value mark for a running iteration without a usable start', () => {
		expect(iterDurText({ running: true }, t0)).toBe(NO_VALUE);
		expect(iterDurText({ running: true, startedAt: '' }, t0)).toBe(NO_VALUE);
		expect(iterDurText({ running: true, startedAt: 'not a time' }, t0)).toBe(NO_VALUE);
	});

	it('shows 0s when the browser clock is behind the start (clock skew)', () => {
		expect(iterDurText({ running: true, startedAt: start }, t0 - 30_000)).toBe('0s');
	});

	it('shows 0s in the first second of a run', () => {
		expect(iterDurText({ running: true, startedAt: start }, t0)).toBe('0s');
		expect(iterDurText({ running: true, startedAt: start }, t0 + 999)).toBe('0s');
	});
});
