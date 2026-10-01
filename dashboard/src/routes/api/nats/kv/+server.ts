import { json } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { getNatsConnection, sc } from '$lib/server/nats-client';

/**
 * Reads all entries from a NATS KV bucket.
 * Default bucket: kubemoot_agent_state (agent heartbeats with 300s TTL, keyed
 * `<namespace>.<agent>`). Entries are returned under their raw keys.
 *
 * Uses kv.watch() with initializedFn to reliably read all current values.
 * The keys()+get() approach missed entries due to async iterator timing.
 *
 * GET /api/nats/kv?bucket=kubemoot_agent_state
 */
export const GET: RequestHandler = async ({ url }) => {
	const bucket = url.searchParams.get('bucket') || 'kubemoot_agent_state';

	try {
		const nc = await getNatsConnection();
		const js = nc.jetstream();
		const kv = await js.views.kv(bucket);

		const agents: Record<string, unknown> = {};

		// Use watch() to get all current values - initializedFn fires
		// after all existing entries have been delivered
		let initialized = false;
		const watch = await kv.watch({
			initializedFn: () => {
				initialized = true;
			}
		});

		for await (const entry of watch) {
			if (entry.operation === 'PUT' && entry.value && entry.value.length > 0) {
				try {
					agents[entry.key] = JSON.parse(sc.decode(entry.value));
				} catch {
					// Skip entries that can't be parsed
				}
			}
			if (initialized) {
				break;
			}
		}
		watch.stop();

		return json({ agents });
	} catch (err) {
		const message = err instanceof Error ? err.message : 'Failed to read KV bucket';
		return json({ agents: {}, error: message }, { status: 200 });
	}
};
