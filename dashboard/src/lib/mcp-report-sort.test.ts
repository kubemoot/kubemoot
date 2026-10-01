import { describe, expect, it } from 'vitest';
import type { MCPServerReport } from '$types/kubemoot.js';
import { reportComparator } from './mcp-report-sort';

function report(serverName?: string, lastTested?: string, successRate?: string): MCPServerReport {
	return {
		spec: { serverName },
		status: { lastTested, successRate }
	} as unknown as MCPServerReport;
}

const names = (list: MCPServerReport[]) => list.map((r) => r.spec?.serverName);

describe('reportComparator', () => {
	const reports = [
		report('beta', '2026-09-01T00:00:00Z', '50'),
		report('alpha', '2026-09-30T00:00:00Z', '12.5'),
		report('gamma')
	];

	it('sorts newest test first, untested last', () => {
		expect(names([...reports].sort(reportComparator('lastTested')))).toEqual([
			'alpha',
			'beta',
			'gamma'
		]);
	});

	it('sorts highest success rate first, a missing rate as 0', () => {
		expect(names([...reports].sort(reportComparator('successRate')))).toEqual([
			'beta',
			'alpha',
			'gamma'
		]);
	});

	it('sorts by server name for any other key', () => {
		expect(names([...reports].sort(reportComparator('name')))).toEqual(['alpha', 'beta', 'gamma']);
		expect(names([...reports].sort(reportComparator('toString')))).toEqual([
			'alpha',
			'beta',
			'gamma'
		]);
	});

	it('treats a report without spec as an empty server name', () => {
		const bare = { status: {} } as unknown as MCPServerReport;
		expect(names([report('a'), bare].sort(reportComparator('name')))).toEqual([undefined, 'a']);
	});
});
