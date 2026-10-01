import { describe, expect, it } from 'vitest';
import { SUITE_LABEL, childSuiteId, runningBySuite, type FitnessTest } from './fitness-children';

function test(name: string, suite?: string, phase?: string): FitnessTest {
	return {
		metadata: { name, namespace: 'crew-a', labels: suite ? { [SUITE_LABEL]: suite } : {} },
		status: phase ? { phase } : undefined
	};
}

describe('childSuiteId', () => {
	it('is the namespace and suite label of a suite child', () => {
		expect(childSuiteId(test('run-x-s0-i1', 'nightly'))).toBe('crew-a/nightly');
	});

	it('is null for a standalone test', () => {
		expect(childSuiteId(test('adhoc'))).toBeNull();
	});
});

describe('runningBySuite', () => {
	it('groups in-flight children by suite and skips terminal and standalone tests', () => {
		const running = runningBySuite([
			test('a', 'nightly', 'Running'),
			test('b', 'nightly'),
			test('c', 'nightly', 'Passed'),
			test('d', 'nightly', 'Failed'),
			test('e', 'smoke', 'Pending'),
			test('f', undefined, 'Running')
		]);
		expect(
			Object.fromEntries(
				Object.entries(running).map(([k, v]) => [k, v.map((t) => t.metadata.name)])
			)
		).toEqual({
			'crew-a/nightly': ['a', 'b'],
			'crew-a/smoke': ['e']
		});
	});

	it('is empty without tests', () => {
		expect(runningBySuite([])).toEqual({});
	});
});
