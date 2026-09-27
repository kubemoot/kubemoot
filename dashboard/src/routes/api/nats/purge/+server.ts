import { json } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { getNatsConnection } from '$lib/server/nats-client';

/**
 * Purges messages from a NATS JetStream stream by subject filter.
 * Used to permanently delete discussion threads from the stream.
 *
 * Usage: POST /api/nats/purge
 * Body: { "stream": "KUBEMOOT_DISCUSS", "filter": "kubemoot.discuss.<ns>.<crew>.*.<threadId>" }
 */
export const POST: RequestHandler = async ({ request }) => {
	try {
		const { stream, filter } = await request.json();

		if (!stream || !filter) {
			return json({ error: 'stream and filter are required' }, { status: 400 });
		}

		const nc = await getNatsConnection();
		const jsm = await nc.jetstreamManager();

		const result = await jsm.streams.purge(stream, { filter });

		return json({ success: true, purged: result.purged });
	} catch (err) {
		const message = err instanceof Error ? err.message : 'Failed to purge';
		return json({ error: message }, { status: 500 });
	}
};
