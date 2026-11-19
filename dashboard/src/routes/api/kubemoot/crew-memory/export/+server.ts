import type { RequestHandler } from './$types';
import { tryCrewScope } from '#lib/crewScope.js';
import { operatorReportBase } from '#lib/server/operator-report.js';
import { guardNamespace } from '#lib/server/scope.js';

/**
 * A crew's memory in the OMPI interchange format, built by the operator (the one
 * implementation of the format) and relayed here, so the viewer's namespace scope
 * applies before the operator is asked.
 *
 *   GET /api/kubemoot/crew-memory/export?namespace=&crew=[&allowUnscreened=true]
 *       -> a zip: the manifest and one memory object per fact
 *   GET /api/kubemoot/crew-memory/export?namespace=&crew=&report=true
 *       -> JSON selection report: how many facts and which entries were left out
 *          (by shape or opaque id, never their text)
 *
 * Facts that hold a credential are left out unless allowUnscreened=true.
 * Read-only: it changes nothing, so a read-only dashboard serves it too.
 */
function fail(message: string, status: number) {
	return Response.json({ error: message }, { status });
}

/** The flags the operator understands, forwarded only when exactly "true". */
function upstreamUrl(scope: { namespace: string; crew: string }, url: URL): string {
	const query = new URLSearchParams();
	for (const flag of ['allowUnscreened', 'report']) {
		if (url.searchParams.get(flag) === 'true') query.set(flag, 'true');
	}
	const path = `${encodeURIComponent(scope.namespace)}/${encodeURIComponent(scope.crew)}`;
	return `${operatorReportBase()}/memory-export/${path}?${query}`;
}

/** The operator's answer passed on as it is, or the error response for a failure. */
async function relay(upstream: Response): Promise<Response> {
	if (!upstream.ok) {
		const detail = (await upstream.text().catch(() => '')).trim();
		return fail(detail || `memory export failed (${upstream.status})`, upstream.status === 400 ? 400 : 502);
	}
	const headers: Record<string, string> = {
		'Content-Type': upstream.headers.get('Content-Type') ?? 'application/octet-stream',
		'Cache-Control': 'no-store'
	};
	const disposition = upstream.headers.get('Content-Disposition');
	if (disposition) headers['Content-Disposition'] = disposition;
	return new Response(Buffer.from(await upstream.arrayBuffer()), { status: 200, headers });
}

export const GET: RequestHandler = async ({ url }) => {
	const scope = tryCrewScope(url.searchParams.get('namespace'), url.searchParams.get('crew'));
	if (!scope) return fail('a valid namespace and crew are required', 400);
	const denied = await guardNamespace(scope.namespace);
	if (denied) return denied;
	try {
		return await relay(await fetch(upstreamUrl(scope, url)));
	} catch (e) {
		return fail(`memory export service unreachable: ${e instanceof Error ? e.message : 'fetch failed'}`, 502);
	}
};
