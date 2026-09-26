import { json } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { getCrewFitnessSuite } from '$lib/server/k8s';
import { listFitnessObjects, readFitnessTranscript } from '$lib/server/nats-object-store';

interface TranscriptSummary {
	assertions?: { passed: boolean }[];
	durationMs?: number;
}

/**
 * Bounded-concurrency map — reads transcripts in parallel without firing N
 * simultaneous object-store fetches for a large (e.g. 260-iteration) suite.
 */
async function mapLimit<T, R>(items: T[], limit: number, fn: (t: T) => Promise<R>): Promise<R[]> {
	const out: R[] = new Array(items.length);
	let next = 0;
	async function worker() {
		while (next < items.length) {
			const idx = next++;
			out[idx] = await fn(items[idx]);
		}
	}
	await Promise.all(Array.from({ length: Math.min(limit, items.length) }, worker));
	return out;
}

/**
 * GET /api/kubemoot/crewfitnesssuites/{namespace}/{name}/iterations
 *
 * Returns one summary row per iteration (scenario, status, assertions, duration)
 * for the suite's drill-down sub-table. Enumerates the per-iteration transcript
 * blobs in NATS Object Store and reads each for its summary; the full event
 * timeline is fetched lazily via the /transcript endpoint when a row is opened.
 * Children are auto-reaped after a run, so this is transcript-backed.
 */
export const GET: RequestHandler = async ({ params }) => {
	const { namespace, name } = params;
	try {
		const suite = await getCrewFitnessSuite(namespace, name);
		const runId = suite.status?.runId;
		if (!runId) {
			return json({ iterations: [] });
		}
		const scripts = suite.spec?.scripts ?? [];
		const prefix = `${namespace}/${name}/${runId}/`;
		const allKeys = await listFitnessObjects(prefix);
		// Skip non-transcript sidecars (judge-v1.json, consistency-v1.json, *.xlsx);
		// only s{idx}-i{iter}.json blobs are per-iteration transcripts. Without this,
		// the cache objects parse to scriptIdx -1 and render a phantom "script -1" group.
		const keys = allKeys.filter((k) => /^s\d+-i\d+\.json$/.test(k.slice(prefix.length)));

		const iterations = await mapLimit(keys, 16, async (key) => {
			const tail = key.slice(prefix.length);
			const m = tail.match(/^s(\d+)-i(\d+)\.json$/);
			const scriptIdx = m ? parseInt(m[1], 10) : -1;
			const iter = m ? parseInt(m[2], 10) : -1;
			const scenario =
				scriptIdx >= 0 && scripts[scriptIdx]?.testRef
					? scripts[scriptIdx].testRef
					: `script ${scriptIdx}`;

			let assertionsPassed = 0;
			let assertionsTotal = 0;
			let durationMs = 0;
			let status = 'Unknown';
			try {
				const t = (await readFitnessTranscript(key)) as TranscriptSummary | null;
				if (t) {
					const a = t.assertions ?? [];
					assertionsTotal = a.length;
					assertionsPassed = a.filter((x) => x.passed).length;
					durationMs = t.durationMs ?? 0;
					status =
						assertionsTotal > 0
							? assertionsPassed === assertionsTotal
								? 'Passed'
								: 'Failed'
							: 'Unknown';
				}
			} catch {
				/* leave defaults — row still renders, conversation lazy-loads */
			}
			return { key, scriptIdx, iter, scenario, status, assertionsPassed, assertionsTotal, durationMs };
		});

		iterations.sort((a, b) => a.scriptIdx - b.scriptIdx || a.iter - b.iter);
		return json({ runId, iterations });
	} catch (e) {
		const message = e instanceof Error ? e.message : 'Failed to list iterations';
		return json({ error: message }, { status: 500 });
	}
};
