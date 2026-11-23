import type { AcceleratorUseByNode } from '#lib/accelerator-use.js';
import { toUse, type ExporterAdapter, type Measures, type Sample } from './adapter.js';
import { dcgmAdapter } from './dcgm.js';

export { toUse } from './adapter.js';
export type { ExporterAdapter, Sample } from './adapter.js';

/** The exporters the dashboard understands, in the order they are tried; DCGM first. */
export const ADAPTERS: readonly ExporterAdapter[] = [dcgmAdapter];

const TIMEOUT_MS = 4000;

type Fetch = typeof fetch;

interface PromResult {
	status?: string;
	error?: string;
	data?: { result?: { metric?: Record<string, string>; value?: [number, string] }[] };
}

/** The base address when it is an http or https URL without a trailing slash; undefined otherwise. */
export function baseUrl(text: string): string | undefined {
	try {
		const url = new URL(text.trim());
		if (url.protocol !== 'http:' && url.protocol !== 'https:') return undefined;
		let base = text.trim();
		while (base.endsWith('/')) base = base.slice(0, -1);
		return base;
	} catch {
		return undefined;
	}
}

async function query(base: string, promql: string, fetchFn: Fetch): Promise<Sample[]> {
	const res = await fetchFn(`${base}/api/v1/query?query=${encodeURIComponent(promql)}`, { signal: AbortSignal.timeout(TIMEOUT_MS) });
	if (!res.ok) throw new Error(`HTTP ${res.status}`);
	const body = (await res.json()) as PromResult;
	if (body.status !== 'success') throw new Error(body.error ?? 'the query failed');
	return (body.data?.result ?? []).map((r) => ({ labels: r.metric ?? {}, value: Number(r.value?.[1]) }));
}

async function measure(base: string, adapter: ExporterAdapter, fetchFn: Fetch): Promise<Measures> {
	const q = adapter.queries;
	const [utilization, memoryUsed, memoryFree] = await Promise.all([
		query(base, q.utilizationPercent, fetchFn),
		query(base, q.memoryUsedMiB, fetchFn),
		query(base, q.memoryFreeMiB, fetchFn)
	]);
	return { utilization, memoryUsed, memoryFree };
}

/**
 * Accelerator use per node, read from Prometheus with the first adapter that finds devices. Node-level
 * accelerator measures only; no other metric is asked for. Throws when Prometheus cannot be read.
 */
export async function readAcceleratorUse(prometheusUrl: string, fetchFn: Fetch = fetch, adapters: readonly ExporterAdapter[] = ADAPTERS): Promise<AcceleratorUseByNode> {
	const base = baseUrl(prometheusUrl);
	if (!base) throw new Error('the metrics address is not an http or https URL');
	for (const adapter of adapters) {
		const use = toUse(adapter, await measure(base, adapter, fetchFn));
		if (Object.keys(use).length > 0) return use;
	}
	return {};
}

/** Keeps only the nodes the caller may read, so the metrics source never shows more than their own token would. */
export function visibleTo(names: ReadonlySet<string>, use: AcceleratorUseByNode): AcceleratorUseByNode {
	return Object.fromEntries(Object.entries(use).filter(([node]) => names.has(node)));
}
