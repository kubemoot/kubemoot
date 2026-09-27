import { json } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { getNatsConnection, sc } from '$lib/server/nats-client';
import {
	crewMemoryKey,
	crewMemoryPrefix,
	crewScope,
	isNamespace,
	kvPrefixSubject,
	parseCrewMemoryKey,
	type CrewMemoryKey,
	type CrewScope
} from '$lib/crewScope';

/**
 * CRUD for crew working-memory facts (the kubemoot_crew_memory NATS KV bucket).
 * Keys are namespace- and crew-scoped: <namespace>.<crew>.<topic>.<key>. Admins
 * can view, create/update, delete a fact, or clear one crew's memory.
 *
 *   GET    /api/kubemoot/crew-memory?crew=homelab-pilot[&namespace=ns]  -> list facts
 *          (without namespace: that crew name in every namespace)
 *   POST   /api/kubemoot/crew-memory  {namespace,crew,topic,key,value} -> create/update
 *   DELETE /api/kubemoot/crew-memory?namespace=&crew=&topic=&key=      -> delete one fact
 *   DELETE /api/kubemoot/crew-memory?namespace=&crew=                  -> clear the crew
 */
const BUCKET = 'kubemoot_crew_memory';

// Mirror the agent/operator key sanitisation: keep [A-Za-z0-9_=-], replace rest.
function sanitize(s: string): string {
	if (!s) return '_';
	return s.trim().replace(/[^A-Za-z0-9_=-]/g, '_');
}

function badRequest(message: string) {
	return json({ error: message }, { status: 400 });
}

// The validated scope from request parameters, or an error message.
function scopeFrom(namespace: string | null | undefined, crew: string | null | undefined): CrewScope | string {
	if (!crew) return 'crew is required';
	if (!isNamespace(namespace)) return 'a valid namespace is required';
	return crewScope(namespace, sanitize(crew));
}

async function openKV() {
	const nc = await getNatsConnection();
	const js = nc.jetstream();
	return js.views.kv(BUCKET);
}

function toFact(parsed: CrewMemoryKey, v: Record<string, unknown>) {
	return {
		namespace: parsed.namespace,
		crew: parsed.crew,
		topic: parsed.topic,
		key: parsed.key,
		value: v.value ?? '',
		learnedBy: v.learnedBy ?? '',
		learnedAt: v.learnedAt ?? '',
		usedAt: v.usedAt ?? v.learnedAt ?? ''
	};
}

// True when a parsed key belongs to the requested crew (and namespace, when given).
function matches(parsed: CrewMemoryKey | null, crew: string, namespace: string | null): parsed is CrewMemoryKey {
	return !!parsed && parsed.crew === crew && (namespace === null || parsed.namespace === namespace);
}

export const GET: RequestHandler = async ({ url }) => {
	const crewParam = url.searchParams.get('crew');
	const namespace = url.searchParams.get('namespace') || null;
	if (!crewParam) return badRequest('crew is required');
	if (namespace !== null && !isNamespace(namespace)) return badRequest('invalid namespace');
	const crew = sanitize(crewParam);
	try {
		const kv = await openKV();
		const facts: unknown[] = [];
		const iter = await kv.keys();
		for await (const k of iter) {
			const parsed = parseCrewMemoryKey(k);
			if (!matches(parsed, crew, namespace)) continue;
			const entry = await kv.get(k);
			if (!entry) continue;
			try {
				facts.push(toFact(parsed, JSON.parse(sc.decode(entry.value))));
			} catch {
				/* skip unreadable */
			}
		}
		return json({ namespace, crew, facts });
	} catch (err) {
		const message = err instanceof Error ? err.message : 'Failed to read crew memory';
		return json({ error: message }, { status: 500 });
	}
};

export const POST: RequestHandler = async ({ request }) => {
	try {
		const { namespace, crew, topic, key, value } = await request.json();
		if (!topic || !key || value === undefined) {
			return badRequest('topic, key, value are required');
		}
		const scope = scopeFrom(namespace, crew);
		if (typeof scope === 'string') return badRequest(scope);
		const kv = await openKV();
		const nk = crewMemoryKey(scope, sanitize(topic), sanitize(key));
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
	const scope = scopeFrom(url.searchParams.get('namespace'), url.searchParams.get('crew'));
	if (typeof scope === 'string') return badRequest(scope);
	const topic = url.searchParams.get('topic');
	const key = url.searchParams.get('key');
	try {
		if (topic && key) {
			const kv = await openKV();
			await kv.delete(crewMemoryKey(scope, sanitize(topic), sanitize(key)));
			return json({ success: true, deleted: 1 });
		}
		// Clear the whole crew's memory via a server-side JetStream stream purge.
		// kv.keys() returns only a partial page in a short-lived request context, so a
		// per-key delete loop under-deletes. Purging the KV-backing stream by subject
		// filter is atomic and complete. The filter carries the namespace and the crew
		// as whole tokens, so neither a same-named crew in another namespace nor a
		// crew whose name extends this one ("homelab-pilot-prose") is touched.
		const nc = await getNatsConnection();
		const jsm = await nc.jetstreamManager();
		const subject = kvPrefixSubject(BUCKET, crewMemoryPrefix(scope));
		const result = await jsm.streams.purge(`KV_${BUCKET}`, { filter: subject });
		return json({ success: true, deleted: result.purged });
	} catch (err) {
		const message = err instanceof Error ? err.message : 'Failed to delete crew memory';
		return json({ error: message }, { status: 500 });
	}
};
