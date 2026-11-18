import { describe, expect, it } from 'vitest';
import { differingParts, previousRun, realmView } from './fitness-realm';
import type { CrewFitnessSuite, RealmDigests } from '#lib/types/kubemoot.js';

type S = Pick<CrewFitnessSuite, 'metadata' | 'spec' | 'status'>;

const D: RealmDigests = { servers: 'aaaa1111', models: 'bbbb2222', versions: 'cccc3333', resources: 'dddd4444' };

function suite(name: string, startedAt: string | undefined, status: Partial<NonNullable<S['status']>> = {}, crew = 'c', ns = 'n'): S {
	return {
		metadata: { name, namespace: ns },
		spec: { crewRef: crew, iterations: 1, scripts: [] },
		status: { startedAt, ...status }
	};
}

describe('differingParts', () => {
	it('is empty for equal digests and names each differing part', () => {
		expect(differingParts(D, { ...D })).toEqual([]);
		expect(differingParts(D, { ...D, versions: 'x', servers: 'y' })).toEqual(['servers', 'versions']);
	});

	it('is unknown when either run has no realm', () => {
		expect(differingParts(undefined, D)).toEqual(['unknown']);
		expect(differingParts(D, undefined)).toEqual(['unknown']);
	});
});

describe('previousRun', () => {
	it('picks the latest earlier run of the same crew in the same namespace', () => {
		const a = suite('a', '2026-01-01T00:00:00Z');
		const b = suite('b', '2026-01-02T00:00:00Z');
		const c = suite('c', '2026-01-03T00:00:00Z');
		const otherCrew = suite('x', '2026-01-02T12:00:00Z', {}, 'other');
		const otherNs = suite('y', '2026-01-02T13:00:00Z', {}, 'c', 'other-ns');
		expect(previousRun(c, [a, b, c, otherCrew, otherNs])?.metadata.name).toBe('b');
		expect(previousRun(a, [a, b, c])).toBeUndefined();
		expect(previousRun(suite('n', undefined), [a])).toBeUndefined();
	});
});

describe('realmView', () => {
	it('warns when no realm was recorded', () => {
		const v = realmView(suite('a', '2026-01-01T00:00:00Z'), []);
		expect(v.label).toBe('not recorded');
		expect(v.warnings[0]).toMatch(/cannot be compared/);
	});

	it('has no warnings for a run that matches the previous run', () => {
		const prev = suite('prev', '2026-01-01T00:00:00Z', { realm: { digests: D } });
		const cur = suite('cur', '2026-01-02T00:00:00Z', { realm: { digests: { ...D } } });
		const v = realmView(cur, [prev, cur]);
		expect(v.warnings).toEqual([]);
		expect(v.label).toBe('aaaa.bbbb.cccc.dddd');
	});

	it('warns which parts differ from the previous run', () => {
		const prev = suite('prev', '2026-01-01T00:00:00Z', { realm: { digests: D } });
		const cur = suite('cur', '2026-01-02T00:00:00Z', { realm: { digests: { ...D, models: 'zzzz' } } });
		expect(realmView(cur, [prev, cur]).warnings).toEqual([
			'realm differs from prev (models): do not compare the scores'
		]);
	});

	it('warns when the previous run recorded no realm', () => {
		const prev = suite('prev', '2026-01-01T00:00:00Z');
		const cur = suite('cur', '2026-01-02T00:00:00Z', { realm: { digests: D } });
		expect(realmView(cur, [prev, cur]).warnings[0]).toMatch(/\(unknown\)/);
	});

	it('warns about drift, concurrent suites and resource notes', () => {
		const cur = suite('cur', '2026-01-02T00:00:00Z', {
			realm: {
				digests: D,
				drift: ['a', 'b'],
				start: { observedAt: 't', concurrentSuites: ['ns/other'], resourceNotes: ['n1'] }
			}
		});
		expect(realmView(cur, [cur]).warnings).toEqual([
			'2 change(s) between start and end',
			'1 other suite(s) ran at the same time',
			'1 agent resource note(s)'
		]);
	});
});
