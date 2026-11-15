import { describe, it, expect } from 'vitest';
import { fetchDashboardVersion, versionLabel, VERSION_UNKNOWN } from './dashboard-version.js';

const json = (body: unknown, status = 200) => async () => new Response(JSON.stringify(body), { status });

describe('fetchDashboardVersion', () => {
	it('returns the reported version', async () => {
		expect(await fetchDashboardVersion(json({ version: '0.94.0' }), '/v')).toBe('0.94.0');
	});
	it('is unknown when the call fails, errors or answers without a version', async () => {
		const down = async () => {
			throw new TypeError('Failed to fetch');
		};
		expect(await fetchDashboardVersion(down, '/v')).toBe(VERSION_UNKNOWN);
		expect(await fetchDashboardVersion(json({}, 500), '/v')).toBe(VERSION_UNKNOWN);
		expect(await fetchDashboardVersion(json({}), '/v')).toBe(VERSION_UNKNOWN);
		expect(await fetchDashboardVersion(json({ version: 3 }), '/v')).toBe(VERSION_UNKNOWN);
		const html = async () => new Response('<html>login</html>', { status: 200 });
		expect(await fetchDashboardVersion(html, '/v')).toBe(VERSION_UNKNOWN);
	});
});

describe('versionLabel', () => {
	it('prefixes a version and spells out an unknown one', () => {
		expect(versionLabel('1.2.3')).toBe('v1.2.3');
		expect(versionLabel(VERSION_UNKNOWN)).toBe('version unknown');
	});
});
