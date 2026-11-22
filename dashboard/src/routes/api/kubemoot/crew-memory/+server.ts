import type { RequestHandler } from './$types';
import { kubeFailure } from '#lib/server/k8s/errors.js';
import { getNatsConnection, sc } from '#lib/server/nats-client.js';
import { isNamespace, parseCrewMemoryKey, type CrewMemoryKey } from '#lib/crewScope.js';
import { callerNamespaces, guardNamespaceOrAll, natsNamespaceAllowedNow } from '#lib/server/scope.js';

/**
 * Reads crew working-memory facts (the kubemoot_crew_memory NATS KV bucket). Keys are
 * namespace- and crew-scoped: <namespace>.<crew>.<topic>.<key>. The dashboard only reads;
 * writes happen outside it (see the dashboard docs).
 *
 *   GET /api/kubemoot/crew-memory?crew=homelab-pilot[&namespace=ns]  -> list facts
 *       (without namespace: that crew name in every namespace)
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

// The bucket is only ever bound, never created: the dashboard holds no write path.
async function openKV() {
	const nc = await getNatsConnection();
	const js = nc.jetstream();
	return js.views.kv(BUCKET, { bindOnly: true });
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
		natsNamespaceAllowedNow(parsed.namespace)
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
	await callerNamespaces();
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
		return kubeFailure(err, 'Failed to read crew memory');
	}
};
