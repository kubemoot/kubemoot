import { describe, expect, it } from 'vitest';
import { nodeCounts, overviewNamespace, readyCount } from './overview-counts';

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

	it('counts Ready nodes and the accelerators on them', () => {
		expect(
			nodeCounts([
				{ ...ready, gpu: { present: true, count: 2 } },
				{ ...notReady, gpu: { present: true, count: 4 } },
				{},
				{ ...ready, gpu: { present: false } }
			])
		).toEqual({
			nodes: { total: 4, ready: 2 },
			accelerators: { total: 6, ready: 2 }
		});
	});

	it('counts an accelerator node that reports no device count as one', () => {
		expect(nodeCounts([{ ...ready, gpu: { present: true } }]).accelerators).toEqual({ total: 1, ready: 1 });
	});

	it('ignores conditions other than Ready', () => {
		const other = { status: { conditions: [{ type: 'DiskPressure', status: 'True' }] } };
		expect(nodeCounts([other]).nodes).toEqual({ total: 1, ready: 0 });
	});

	it('is zero without nodes', () => {
		expect(nodeCounts([])).toEqual({ nodes: { total: 0, ready: 0 }, accelerators: { total: 0, ready: 0 } });
	});
});

describe('overviewNamespace', () => {
	it('counts cluster-owned resources across all namespaces whatever crew is selected', () => {
		for (const plural of ['modelservers', 'models', 'embeddingmodels']) {
			expect(overviewNamespace(plural, 'crew-oguide')).toBe('');
		}
	});

	it('scopes every other resource to the selected crew', () => {
		expect(overviewNamespace('mcpservers', 'crew-oguide')).toBe('crew-oguide');
		expect(overviewNamespace('agents', '')).toBe('');
	});
});
