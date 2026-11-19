import { describe, expect, it, vi } from 'vitest';
import {
	exportFileName,
	exportQuery,
	exportSummary,
	fetchExportReport,
	parseExportReport,
	type MemoryExportReport
} from './crew-memory-export';

const scope = { namespace: 'pilot', crew: 'homelab-pilot' };
const report: MemoryExportReport = {
	namespace: 'pilot',
	crew: 'homelab-pilot',
	memories: 3,
	screened: true,
	leftOut: [{ key: 'pilot.homelab-pilot.#1a2b3c4d', reason: 'its topic or key holds a credential; not exported as memory' }]
};

describe('exportQuery', () => {
	it('is screened and a download by default', () => {
		expect(exportQuery(scope, { allowUnscreened: false, report: false })).toBe('namespace=pilot&crew=homelab-pilot');
	});

	it('asks for the report and for unscreened facts only when told to', () => {
		const q = new URLSearchParams(exportQuery(scope, { allowUnscreened: true, report: true }));
		expect(q.get('allowUnscreened')).toBe('true');
		expect(q.get('report')).toBe('true');
	});
});

describe('parseExportReport', () => {
	it('accepts what the operator sends', () => {
		expect(parseExportReport(report)).toEqual(report);
	});

	it.each([
		['null', null],
		['a string', 'nope'],
		['no count', { screened: true, leftOut: [] }],
		['no leftOut list', { memories: 1, screened: true }],
		['a left-out entry without a reason', { memories: 1, screened: true, leftOut: [{ key: 'k' }] }]
	])('rejects %s', (_name, body) => {
		expect(parseExportReport(body)).toBeNull();
	});
});

describe('exportSummary', () => {
	it('says nothing was left out', () => {
		expect(exportSummary({ ...report, leftOut: [] })).toBe('The export holds 3 facts; nothing was left out.');
	});

	it('counts what was left out, singular and plural', () => {
		expect(exportSummary({ ...report, memories: 1 })).toBe('The export holds 1 fact; 1 entry was left out.');
		const two = { ...report, leftOut: [report.leftOut[0], { key: 'k', reason: 'r' }] };
		expect(exportSummary(two)).toBe('The export holds 3 facts; 2 entries were left out.');
	});

	it('never carries a fact value, only keys and reasons', () => {
		expect(JSON.stringify(report)).not.toMatch(/Bearer/);
	});
});

describe('exportFileName', () => {
	it('names the crew', () => {
		expect(exportFileName(scope)).toBe('homelab-pilot-memory-ompi.zip');
	});
});

describe('fetchExportReport', () => {
	const respond = (body: unknown, status = 200) =>
		vi.fn().mockResolvedValue({ ok: status < 400, status, json: async () => body });

	it('requests the report for the scope and returns it', async () => {
		const fetchFn = respond(report);
		await expect(fetchExportReport(fetchFn, '/api/export', scope, false)).resolves.toEqual(report);
		expect(fetchFn).toHaveBeenCalledWith('/api/export?namespace=pilot&crew=homelab-pilot&report=true');
	});

	it('throws the server message when the export cannot be had', async () => {
		await expect(fetchExportReport(respond({ error: 'memory export service unreachable' }, 502), '/e', scope, false)).rejects.toThrow(
			'memory export service unreachable'
		);
	});

	it('throws when the body is not a report', async () => {
		await expect(fetchExportReport(respond({ hello: 1 }), '/e', scope, false)).rejects.toThrow(/not understood/);
	});
});
