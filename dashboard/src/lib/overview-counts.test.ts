import { describe, expect, it } from 'vitest';
import { nodeCounts, readyCount } from './overview-counts';

describe('readyCount', () => {
	it('counts all items and the ready ones', () => {
		expect(
			readyCount([
				{ status: { ready: true } },
				{ status: { ready: false } },
				{},
				{ status: { ready: true } }
			])
		).toEqual({ total: 4, ready: 2 });
	});

	it('is zero for a missing or empty list', () => {
		expect(readyCount(undefined)).toEqual({ total: 0, ready: 0 });
		expect(readyCount([])).toEqual({ total: 0, ready: 0 });
	});
});

describe('nodeCounts', () => {
	const ready = { status: { conditions: [{ type: 'Ready', status: 'True' }] } };
	const notReady = { status: { conditions: [{ type: 'Ready', status: 'False' }] } };

	it('counts Ready nodes and GPU nodes', () => {
		expect(
			nodeCounts([
				{ ...ready, gpu: { present: true } },
				notReady,
				{},
				{ ...ready, gpu: { present: false } }
			])
		).toEqual({
			nodes: { total: 4, ready: 2 },
			gpus: { total: 1, ready: 1 }
		});
	});

	it('ignores conditions other than Ready', () => {
		const other = { status: { conditions: [{ type: 'DiskPressure', status: 'True' }] } };
		expect(nodeCounts([other]).nodes).toEqual({ total: 1, ready: 0 });
	});

	it('is zero without nodes', () => {
		expect(nodeCounts([])).toEqual({ nodes: { total: 0, ready: 0 }, gpus: { total: 0, ready: 0 } });
	});
});
