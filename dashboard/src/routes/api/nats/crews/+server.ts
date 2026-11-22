import type { RequestHandler } from './$types';
import { listCrews } from '#lib/server/k8s/index.js';
import { kubeFailure } from '#lib/server/k8s/errors.js';
import { scopeList } from '#lib/server/scope.js';
import { readCrewUsage } from '#lib/server/nats-status.js';
import { CREW_ENTRY_CAP } from '#lib/nats/crew-usage.js';
import { cachedRead } from '#lib/server/ttl-cache.js';

const usage = cachedRead(15_000, async () => {
	const crews = await scopeList('', listCrews);
	const refs = crews.items.map((c) => ({ namespace: c.metadata.namespace ?? '', crew: c.metadata.name }));
	const byRef = new Map(crews.items.map((c) => [`${c.metadata.namespace}/${c.metadata.name}`, c.status?.retention ?? null]));
	const perCrew = await readCrewUsage(refs);
	return perCrew.map((u) => ({ ...u, retention: byRef.get(`${u.namespace}/${u.crew}`) ?? null }));
});

/**
 * Each crew's share of the stores keyed by crew, and its last retention cleanup. Reads
 * subject-filtered stream info and bounded object listings, so a large bucket cannot
 * stall it: no more than `cap` entries count per crew and store.
 *
 * GET /api/nats/crews
 */
export const GET: RequestHandler = async () => {
	try {
		return Response.json(
			{ checkedAt: new Date().toISOString(), cap: CREW_ENTRY_CAP, crews: await usage() },
			{ headers: { 'Cache-Control': 'no-store' } }
		);
	} catch (e) {
		return kubeFailure(e, 'Failed to read crew usage', { crews: [], cap: CREW_ENTRY_CAP });
	}
};
