import { describe, expect, it, vi } from 'vitest';
import { toUse } from './adapter';
import { dcgmAdapter } from './dcgm';
import { baseUrl, readAcceleratorUse, visibleTo } from './index';

const sample = (labels: Record<string, string>, value: number) => ({ labels, value });
const dev = (uuid: string, hostname = 'n1') => ({ UUID: uuid, hostname });

describe('the DCGM adapter turns DCGM series into the generic reading', () => {
	it('averages utilization over devices as a share and sums memory used and total in MiB', () => {
		const use = toUse(dcgmAdapter, {
			utilization: [sample(dev('a'), 100), sample(dev('b'), 0)],
			memoryUsed: [sample(dev('a'), 1000), sample(dev('b'), 500)],
			memoryFree: [sample(dev('a'), 23000), sample(dev('b'), 23500)]
		});
		expect(use).toEqual({ n1: { devices: 2, busy: 0.5, memoryUsedMiB: 1500, memoryTotalMiB: 48000 } });
	});

	it('reads the node from Hostname or hostname or node, and skips a series that names none', () => {
		const use = toUse(dcgmAdapter, { utilization: [sample({ Hostname: 'x', UUID: '1' }, 10), sample({ node: 'y', UUID: '2' }, 20), sample({ UUID: '3' }, 30)], memoryUsed: [], memoryFree: [] });
		expect(Object.keys(use).sort((a, b) => a.localeCompare(b))).toEqual(['x', 'y']);
	});

	it('leaves memory out when only used or only free is reported, and ignores a non-number', () => {
		const use = toUse(dcgmAdapter, { utilization: [sample(dev('a'), Number.NaN)], memoryUsed: [sample(dev('a'), 5)], memoryFree: [] });
		expect(use.n1).toEqual({ devices: 1, busy: undefined, memoryUsedMiB: 5, memoryTotalMiB: undefined });
	});

	it('keeps utilization within 0 to 1', () => {
		expect(toUse(dcgmAdapter, { utilization: [sample(dev('a'), 140)], memoryUsed: [], memoryFree: [] }).n1.busy).toBe(1);
	});

	it('asks for the DCGM metrics by name', () => {
		expect(dcgmAdapter.queries).toEqual({ utilizationPercent: 'DCGM_FI_DEV_GPU_UTIL', memoryUsedMiB: 'DCGM_FI_DEV_FB_USED', memoryFreeMiB: 'DCGM_FI_DEV_FB_FREE' });
	});
});

describe('readAcceleratorUse', () => {
	const vector = (result: object[]) => ({ ok: true, json: async () => ({ status: 'success', data: { result } }) }) as Response;
	const series = (value: string) => ({ metric: { UUID: 'a', Hostname: 'n1' }, value: [1, value] });

	it('queries Prometheus for the three accelerator measures only and returns the generic use', async () => {
		const values: Record<string, string> = { DCGM_FI_DEV_GPU_UTIL: '50', DCGM_FI_DEV_FB_USED: '10', DCGM_FI_DEV_FB_FREE: '30' };
		const get = vi.fn(async (url: string) => vector([series(values[decodeURIComponent(url.split('query=')[1])])]));
		const use = await readAcceleratorUse('https://prom.example.test:9090/', get as never);
		expect(use).toEqual({ n1: { devices: 1, busy: 0.5, memoryUsedMiB: 10, memoryTotalMiB: 40 } });
		const asked = get.mock.calls.map((c) => decodeURIComponent(c[0].split('query=')[1])).sort((x, y) => x.localeCompare(y));
		expect(asked).toEqual(['DCGM_FI_DEV_FB_FREE', 'DCGM_FI_DEV_FB_USED', 'DCGM_FI_DEV_GPU_UTIL']);
		expect(get.mock.calls[0][0].startsWith('https://prom.example.test:9090/api/v1/query?query=')).toBe(true);
	});

	it('is empty when no exporter reports a device', async () => {
		expect(await readAcceleratorUse('https://prom.example.test:9090', async () => vector([]))).toEqual({});
	});

	it('fails with the reason when Prometheus answers an error or is unreachable', async () => {
		await expect(readAcceleratorUse('https://prom.example.test:9090', async () => ({ ok: false, status: 503 }) as Response)).rejects.toThrow('HTTP 503');
		await expect(readAcceleratorUse('https://prom.example.test:9090', async () => ({ ok: true, json: async () => ({ status: 'error', error: 'bad query' }) }) as Response)).rejects.toThrow('bad query');
		await expect(readAcceleratorUse('https://prom.example.test:9090', async () => { throw new Error('connect ECONNREFUSED'); })).rejects.toThrow('ECONNREFUSED');
	});

	it('refuses an address that is not http or https', async () => {
		await expect(readAcceleratorUse('file:///etc/passwd', vi.fn() as never)).rejects.toThrow('not an http or https URL');
		expect(baseUrl('prom')).toBeUndefined();
		expect(baseUrl(' https://prom.example/// ')).toBe('https://prom.example');
	});
});

describe('visibleTo', () => {
	it('keeps only the nodes the caller can list', () => {
		expect(visibleTo(new Set(['a']), { a: { devices: 1 }, b: { devices: 1 } })).toEqual({ a: { devices: 1 } });
	});
});
