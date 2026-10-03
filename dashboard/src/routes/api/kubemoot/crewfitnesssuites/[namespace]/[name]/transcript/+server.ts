import { json } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { readFitnessTranscript } from '$lib/server/nats-object-store';
import { guardNamespace } from '$lib/server/scope';

/**
 * GET /api/kubemoot/crewfitnesssuites/{namespace}/{name}/transcript?key=...
 *
 * Returns a single per-iteration transcript JSON (the fitness-runner's
 * RunOutcome: assertions + the full SSE event timeline + run metadata). The
 * key must fall within this suite's prefix - guards against reading another
 * suite's (or arbitrary) objects via a crafted key.
 */
export const GET: RequestHandler = async ({ params, url }) => {
	const { namespace, name } = params;
	const denied = await guardNamespace(namespace);
	if (denied) return denied;
	const key = url.searchParams.get('key');
	if (!key) {
		return json({ error: 'key required' }, { status: 400 });
	}
	const prefix = `${namespace}/${name}/`;
	if (!key.startsWith(prefix) || key.includes('..')) {
		return json({ error: 'key outside this suite' }, { status: 400 });
	}
	try {
		const transcript = await readFitnessTranscript(key);
		if (!transcript) {
			return json({ error: 'transcript not found (TTL-pruned or never written)' }, { status: 404 });
		}
		return json(transcript);
	} catch (e) {
		const message = e instanceof Error ? e.message : 'Failed to read transcript';
		return json({ error: message }, { status: 500 });
	}
};
