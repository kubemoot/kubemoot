import { json } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { listFitnessObjects, readFitnessTranscript } from '$lib/server/nats-object-store';
import { guardNamespace } from '$lib/server/scope';

/**
 * GET /api/kubemoot/crewfitnesssuites/{namespace}/{name}/scores
 *
 * Returns the post-suite DEFER/REFLECTS reference-grounded quality scores for the
 * suite's run as { scenario: score }, plus judging progress. Read from the run's
 * deferred-scores-v2.json checkpoint (written incrementally by the operator's
 * resumable judge pass, one scenario at a time), with a v1 fallback for pre-v2 runs.
 *
 * Response: { scores, complete, judged } - judged < scenario count with
 * complete=false means judging is still in progress (the page shows "judging X");
 * complete=true means the pass finished.
 */
export const GET: RequestHandler = async ({ params }) => {
	const { namespace, name } = params;
	const denied = await guardNamespace(namespace);
	if (denied) return denied;
	const prefix = `${namespace}/${name}/`;
	try {
		const keys = await listFitnessObjects(prefix);
		const v2Key = keys.find((k) => k.endsWith('deferred-scores-v2.json'));
		if (v2Key) {
			const cache = ((await readFitnessTranscript(v2Key)) ?? {}) as {
				scores?: Record<string, number>;
				reasons?: Record<string, string>;
				complete?: boolean;
			};
			const scores = cache.scores ?? {};
			const reasons = cache.reasons ?? {};
			return json({ scores, reasons, complete: !!cache.complete, judged: Object.keys(scores).length });
		}
		// Back-compat: pre-v2 runs stored a bare { scenario: score } map (always final).
		const v1Key = keys.find((k) => k.endsWith('deferred-scores-v1.json'));
		if (!v1Key) return json({ scores: {}, complete: false, judged: 0 });
		const scores = ((await readFitnessTranscript(v1Key)) ?? {}) as Record<string, number>;
		return json({ scores, complete: true, judged: Object.keys(scores).length });
	} catch (e) {
		const message = e instanceof Error ? e.message : 'failed to read scores';
		return json({ error: message, scores: {}, complete: false, judged: 0 }, { status: 500 });
	}
};
