// The Memory page's Export: the request it sends and how it shows the selection
// report. The export itself is built by the operator (the OMPI format has one
// implementation, in Go); nothing here maps facts to the format.

import type { CrewScope } from '#lib/crewScope.js';

/** One entry the export left out: its key (an opaque id when the key held a credential) and why. Never the text. */
export interface LeftOutEntry {
	key: string;
	reason: string;
}

/** What the operator reports about one export. */
export interface MemoryExportReport {
	namespace: string;
	crew: string;
	memories: number;
	leftOut: LeftOutEntry[];
}

/**
 * The query string of an export request: the report as JSON, or the zip when `report` is false.
 * There is no unscreened option: the dashboard acts with its own service account, so it only
 * offers the screened export. The unscreened export is `kmctl`, which runs as the caller.
 */
export function exportQuery(scope: CrewScope, options: { report: boolean }): string {
	const q = new URLSearchParams({ namespace: scope.namespace, crew: scope.crew });
	if (options.report) q.set('report', 'true');
	return q.toString();
}

function isLeftOut(e: unknown): e is LeftOutEntry {
	const o = e as Record<string, unknown> | null;
	return !!o && typeof o.key === 'string' && typeof o.reason === 'string';
}

/** The report in a response body, or null when the body is not a report. */
export function parseExportReport(data: unknown): MemoryExportReport | null {
	const o = data as Record<string, unknown> | null;
	if (!o || typeof o.memories !== 'number') return null;
	if (!Array.isArray(o.leftOut) || !o.leftOut.every(isLeftOut)) return null;
	return {
		namespace: typeof o.namespace === 'string' ? o.namespace : '',
		crew: typeof o.crew === 'string' ? o.crew : '',
		memories: o.memories,
		leftOut: o.leftOut
	};
}

function plural(n: number, one: string, many: string): string {
	return `${n} ${n === 1 ? one : many}`;
}

/** One line for the person: what the export holds and what it left out. */
export function exportSummary(report: MemoryExportReport): string {
	const held = plural(report.memories, 'fact', 'facts');
	if (report.leftOut.length === 0) return `The export holds ${held}; nothing was left out.`;
	return `The export holds ${held}; ${plural(report.leftOut.length, 'entry was', 'entries were')} left out.`;
}

/** The download's file name. */
export function exportFileName(scope: CrewScope): string {
	return `${scope.crew}-memory-ompi.zip`;
}

/** Asks for the selection report; throws the server's message when it cannot be had. */
export async function fetchExportReport(
	fetchFn: typeof fetch,
	endpoint: string,
	scope: CrewScope
): Promise<MemoryExportReport> {
	const res = await fetchFn(`${endpoint}?${exportQuery(scope, { report: true })}`);
	const data = await res.json().catch(() => null);
	if (!res.ok) throw new Error((data as { error?: string } | null)?.error ?? `Export failed (${res.status})`);
	const report = parseExportReport(data);
	if (!report) throw new Error('The export report was not understood');
	return report;
}
