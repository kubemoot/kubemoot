import type { RequestHandler } from './$types';
import { getNatsConnection, sc } from '#lib/server/nats-client.js';
import {
	crewMemoryKey,
	crewMemoryPrefix,
	crewScope,
	isNamespace,
	kvPrefixSubject,
	parseCrewMemoryKey,
	type CrewMemoryKey,
	type CrewScope
} from '#lib/crewScope.js';
import { isReadOnly } from '#lib/server/mode.js';
import { guardNamespace, guardNamespaceOrAll, namespaceAllowedNow } from '#lib/server/scope.js';

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
	return s.trim().replaceAll(/[^A-Za-z0-9_=-]/g, '_');
}

function badRequest(message: string) {
	return Response.json({ error: message }, { status: 400 });
}

// The validated scope from request parameters, or an error message.
function scopeFrom(namespace: string | null | undefined, crew: string | null | undefined): CrewScope | string {
	if (!crew) return 'crew is required';
	if (!isNamespace(namespace)) return 'a valid namespace is required';
	return crewScope(namespace, sanitize(crew));
}

// A read-only dashboard binds to the bucket and never creates it.
async function openKV() {
	const nc = await getNatsConnection();
	const js = nc.jetstream();
	return js.views.kv(BUCKET, { bindOnly: isReadOnly() });
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

// True when a parsed key belongs to the requested crew (and namespace, when given)
// and the namespace is one this dashboard may show.
function matches(parsed: CrewMemoryKey | null, crew: string, namespace: string | null): parsed is CrewMemoryKey {
	return (
		!!parsed &&
		parsed.crew === crew &&
		(namespace === null || parsed.namespace === namespace) &&
		namespaceAllowedNow(parsed.namespace)
	);
}

type CrewMemoryKV = Awaited<ReturnType<typeof openKV>>;

// The fact stored under a key, or null when the entry is gone or unreadable.
async function readFact(kv: CrewMemoryKV, k: string, parsed: CrewMemoryKey) {
	const entry = await kv.get(k);
	if (!entry) return null;
	try {
		return toFact(parsed, JSON.parse(sc.decode(entry.value)));
	} catch {
		return null;
	}
}

// The crew and optional namespace of a list request, or the 400 that rejects them.
function listParams(url: URL): { crew: string; namespace: string | null } | Response {
	const crewParam = url.searchParams.get('crew');
	const namespace = url.searchParams.get('namespace') || null;
	if (!crewParam) return badRequest('crew is required');
	if (namespace !== null && !isNamespace(namespace)) return badRequest('invalid namespace');
	return { crew: sanitize(crewParam), namespace };
}

export const GET: RequestHandler = async ({ url }) => {
	const params = listParams(url);
	if (params instanceof Response) return params;
	const { crew, namespace } = params;
	const denied = await guardNamespaceOrAll(namespace ?? '');
	if (denied) return denied;
	try {
		const kv = await openKV();
		const facts: unknown[] = [];
		const iter = await kv.keys();
		for await (const k of iter) {
			const parsed = parseCrewMemoryKey(k);
			if (!matches(parsed, crew, namespace)) continue;
			const fact = await readFact(kv, k, parsed);
			if (fact) facts.push(fact);
		}
		return Response.json({ namespace, crew, facts });
	} catch (err) {
		const message = err instanceof Error ? err.message : 'Failed to read crew memory';
		return Response.json({ error: message }, { status: 500 });
	}
};

// When the fact was first learned: the stored time for an existing key, otherwise now.
async function firstLearnedAt(kv: CrewMemoryKV, key: string, now: string): Promise<string> {
	const existing = await kv.get(key);
	if (!existing) return now;
	try {
		return JSON.parse(sc.decode(existing.value)).learnedAt ?? now;
	} catch {
		return now;
	}
}

export const POST: RequestHandler = async ({ request }) => {
	try {
		const { namespace, crew, topic, key, value } = await request.json();
		if (!topic || !key || value === undefined) {
			return badRequest('topic, key, value are required');
		}
		const scope = scopeFrom(namespace, crew);
		if (typeof scope === 'string') return badRequest(scope);
		const denied = await guardNamespace(scope.namespace);
		if (denied) return denied;
		const kv = await openKV();
		const nk = crewMemoryKey(scope, sanitize(topic), sanitize(key));
		const now = new Date().toISOString();
		const learnedAt = await firstLearnedAt(kv, nk, now);
		const payload = { value: String(value), learnedBy: 'dashboard', learnedAt, usedAt: now };
		await kv.put(nk, sc.encode(JSON.stringify(payload)));
		return Response.json({ success: true, key: nk });
	} catch (err) {
		const message = err instanceof Error ? err.message : 'Failed to write crew memory';
		return Response.json({ error: message }, { status: 500 });
	}
};

async function deleteFact(scope: CrewScope, topic: string, key: string) {
	const kv = await openKV();
	await kv.delete(crewMemoryKey(scope, topic, key));
	return Response.json({ success: true, deleted: 1 });
}

// Clear the whole crew's memory via a server-side JetStream stream purge.
// kv.keys() returns only a partial page in a short-lived request context, so a
// per-key delete loop under-deletes. Purging the KV-backing stream by subject
// filter is atomic and complete. The filter carries the namespace and the crew
// as whole tokens, so neither a same-named crew in another namespace nor a
// crew whose name extends this one ("homelab-pilot-prose") is touched.
async function clearCrew(scope: CrewScope) {
	const nc = await getNatsConnection();
	const jsm = await nc.jetstreamManager();
	const subject = kvPrefixSubject(BUCKET, crewMemoryPrefix(scope));
	const result = await jsm.streams.purge(`KV_${BUCKET}`, { filter: subject });
	return Response.json({ success: true, deleted: result.purged });
}

export const DELETE: RequestHandler = async ({ url }) => {
	const scope = scopeFrom(url.searchParams.get('namespace'), url.searchParams.get('crew'));
	if (typeof scope === 'string') return badRequest(scope);
	const denied = await guardNamespace(scope.namespace);
	if (denied) return denied;
	const topic = url.searchParams.get('topic');
	const key = url.searchParams.get('key');
	try {
		if (topic && key) return await deleteFact(scope, sanitize(topic), sanitize(key));
		return await clearCrew(scope);
	} catch (err) {
		const message = err instanceof Error ? err.message : 'Failed to delete crew memory';
		return Response.json({ error: message }, { status: 500 });
	}
};
