import { json } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { getNatsConnection, sc } from '$lib/server/nats-client';

/**
 * CRUD for crew working-memory facts (the kubemoot_crew_memory NATS KV bucket).
 * Keys are crew-scoped: <crew>.<topic>.<key>. Admins can view, create/update,
 * delete a fact, or clear a whole crew's memory from the dashboard.
 *
 *   GET    /api/kubemoot/crew-memory?crew=homelab-pilot      → list facts
 *   POST   /api/kubemoot/crew-memory  {crew,topic,key,value} → create/update
 *   DELETE /api/kubemoot/crew-memory?crew=&topic=&key=       → delete one fact
 *   DELETE /api/kubemoot/crew-memory?crew=                   → clear the crew
 */
const BUCKET = 'kubemoot_crew_memory';

// Mirror the agent/operator key sanitisation: keep [A-Za-z0-9_=-], replace rest.
function sanitize(s: string): string {
	if (!s) return '_';
	return s.trim().replace(/[^A-Za-z0-9_=-]/g, '_');
}

async function openKV() {
	const nc = await getNatsConnection();
	const js = nc.jetstream();
	return js.views.kv(BUCKET);
}

export const GET: RequestHandler = async ({ url }) => {
	const crew = url.searchParams.get('crew');
	if (!crew) return json({ error: 'crew is required' }, { status: 400 });
	try {
		const kv = await openKV();
		const prefix = sanitize(crew) + '.';
		const facts: unknown[] = [];
		const iter = await kv.keys();
		for await (const k of iter) {
			if (!k.startsWith(prefix)) continue;
			const entry = await kv.get(k);
			if (!entry) continue;
			let v: Record<string, unknown> = {};
			try {
				v = JSON.parse(sc.decode(entry.value));
			} catch {
				/* skip unreadable */
				continue;
			}
			const rest = k.substring(prefix.length);
			const dot = rest.indexOf('.');
			facts.push({
				topic: dot >= 0 ? rest.substring(0, dot) : rest,
				key: dot >= 0 ? rest.substring(dot + 1) : '',
				value: v.value ?? '',
				learnedBy: v.learnedBy ?? '',
				learnedAt: v.learnedAt ?? '',
				usedAt: v.usedAt ?? v.learnedAt ?? ''
			});
		}
		return json({ crew, facts });
	} catch (err) {
		const message = err instanceof Error ? err.message : 'Failed to read crew memory';
		return json({ error: message }, { status: 500 });
	}
};

export const POST: RequestHandler = async ({ request }) => {
	try {
		const { crew, topic, key, value } = await request.json();
		if (!crew || !topic || !key || value === undefined) {
			return json({ error: 'crew, topic, key, value are required' }, { status: 400 });
		}
		const kv = await openKV();
		const nk = `${sanitize(crew)}.${sanitize(topic)}.${sanitize(key)}`;
		const now = new Date().toISOString();
		const existing = await kv.get(nk);
		let learnedAt = now;
		if (existing) {
			try {
				learnedAt = JSON.parse(sc.decode(existing.value)).learnedAt ?? now;
			} catch {
				/* keep now */
			}
		}
		const payload = { value: String(value), learnedBy: 'dashboard', learnedAt, usedAt: now };
		await kv.put(nk, sc.encode(JSON.stringify(payload)));
		return json({ success: true, key: nk });
	} catch (err) {
		const message = err instanceof Error ? err.message : 'Failed to write crew memory';
		return json({ error: message }, { status: 500 });
	}
};

export const DELETE: RequestHandler = async ({ url }) => {
	const crew = url.searchParams.get('crew');
	const topic = url.searchParams.get('topic');
	const key = url.searchParams.get('key');
	if (!crew) return json({ error: 'crew is required' }, { status: 400 });
	try {
		const kv = await openKV();
		if (topic && key) {
			// Delete one fact.
			await kv.delete(`${sanitize(crew)}.${sanitize(topic)}.${sanitize(key)}`);
			return json({ success: true, deleted: 1 });
		}
		// Clear the whole crew's memory via a server-side JetStream stream purge.
		// kv.keys() returns only a partial page in a short-lived request context, so
		// the previous per-key delete loop silently under-deleted (one key per call).
		// Purging the KV-backing stream by subject filter is atomic and complete.
		// Crew tokens don't overlap ("homelab-pilot" vs "homelab-pilot-prose" are
		// distinct subject tokens), so the filter is crew-scoped.
		const nc = await getNatsConnection();
		const jsm = await nc.jetstreamManager();
		const subject = `$KV.${BUCKET}.${sanitize(crew)}.>`;
		const result = await jsm.streams.purge(`KV_${BUCKET}`, { filter: subject });
		return json({ success: true, deleted: result.purged });
	} catch (err) {
		const message = err instanceof Error ? err.message : 'Failed to delete crew memory';
		return json({ error: message }, { status: 500 });
	}
};
