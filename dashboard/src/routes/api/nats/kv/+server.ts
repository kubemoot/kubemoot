import type { RequestHandler } from './$types';
import type { KvEntry } from 'nats';
import { getNatsConnection, sc } from '#lib/server/nats-client.js';
import { readKvSnapshot } from '#lib/server/kv-snapshot.js';
import { guardKvRead, keyNamespaceAllowed } from '#lib/server/scope.js';

/** A live entry (not a delete marker, not empty) in a namespace this dashboard may show. */
function isShownEntry(entry: KvEntry): boolean {
	return entry.operation === 'PUT' && !!entry.value && entry.value.length > 0 && keyNamespaceAllowed(entry.key);
}

/** An entry's JSON value when it is shown, or undefined to leave it out. */
function parsedEntry(entry: KvEntry): unknown {
	if (!isShownEntry(entry)) return undefined;
	try {
		return JSON.parse(sc.decode(entry.value));
	} catch {
		return undefined;
	}
}

/**
 * Reads all entries from the agent-state NATS KV bucket, the only bucket served (any other
 * `bucket` is refused with 403). kubemoot_agent_state (agent heartbeats with 300s TTL, keyed
 * `<namespace>.<agent>`). Entries are returned under their raw keys.
 *
 * Reads the current values through a watch that ends when the store says it delivered them
 * all, or at a short deadline (then `partial: true`); it never waits for a later heartbeat.
 *
 * GET /api/nats/kv?bucket=kubemoot_agent_state
 */
export const GET: RequestHandler = async ({ url }) => {
	const bucket = url.searchParams.get('bucket') || 'kubemoot_agent_state';
	const denied = await guardKvRead(bucket);
	if (denied) return denied;

	try {
		const nc = await getNatsConnection();
		const kv = await nc.jetstream().views.kv(bucket, { bindOnly: true });
		const { values, complete } = await readKvSnapshot(kv, parsedEntry);
		return Response.json({ agents: values, ...(complete ? {} : { partial: true }) });
	} catch (err) {
		const message = err instanceof Error ? err.message : 'Failed to read KV bucket';
		return Response.json({ agents: {}, error: message }, { status: 200 });
	}
};
