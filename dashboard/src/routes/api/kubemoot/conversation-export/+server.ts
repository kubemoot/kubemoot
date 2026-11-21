import type { RequestHandler } from './$types';
import { tryCrewScope } from '#lib/crewScope.js';
import { cleanTimeZone, exportQuery, isConversationId, MAX_EXPORT_IDS } from '#lib/conversation-export.js';
import { operatorAuthHeaders, operatorReportBase } from '#lib/server/operator-report.js';
import { guardNamespace } from '#lib/server/scope.js';

/**
 * Conversations of a crew as a zip, built by the operator and relayed here, so the
 * viewer's namespace scope applies before the operator is asked.
 *
 *   GET /api/kubemoot/conversation-export?namespace=&crew=&id=ID
 *       -> one conversation as a zip
 *   GET /api/kubemoot/conversation-export?namespace=&crew=&id=A&id=B
 *       -> a zip of zips with a manifest of what is included, refused, expired or failed
 *   ...&report=true -> the manifest as JSON, no zip
 *
 * The file name comes from the operator. Only the ids, the report flag and a time zone
 * are forwarded, and an allowUnscreened parameter is refused as in the memory export.
 * The operator is called with the dashboard's service account token, which it checks with
 * a TokenReview and a SubjectAccessReview (`get` on the Crew). Read-only: it changes nothing.
 */
function fail(message: string, status: number) {
	return Response.json({ error: message }, { status });
}

/** What the operator's own refusals mean for the person; anything else is a failed upstream. */
const PASSED_ON = new Set([400, 403, 404, 503]);

type Scope = { namespace: string; crew: string };

/** The operator URL, from the validated scope and ids; only `report` (exactly "true") and a clean zone follow. */
function upstreamUrl(scope: Scope, ids: string[], url: URL): string {
	const query = new URLSearchParams(exportQuery(scope, ids, cleanTimeZone(url.searchParams.get('timeZone'))));
	query.delete('namespace');
	query.delete('crew');
	if (url.searchParams.get('report') === 'true') query.set('report', 'true');
	const path = `${encodeURIComponent(scope.namespace)}/${encodeURIComponent(scope.crew)}`;
	return `${operatorReportBase()}/conversation-export/${path}?${query}`;
}

/** The operator's answer passed on as it is, or the error response for a failure. */
async function relay(upstream: Response): Promise<Response> {
	if (!upstream.ok) {
		const detail = (await upstream.text().catch(() => '')).trim();
		const status = PASSED_ON.has(upstream.status) ? upstream.status : 502;
		return fail(detail || `conversation export failed (${upstream.status})`, status);
	}
	const headers: Record<string, string> = {
		'Content-Type': upstream.headers.get('Content-Type') ?? 'application/octet-stream',
		'Cache-Control': 'no-store'
	};
	const disposition = upstream.headers.get('Content-Disposition');
	if (disposition) headers['Content-Disposition'] = disposition;
	return new Response(Buffer.from(await upstream.arrayBuffer()), { status: 200, headers });
}

/** The request's ids when they are all valid and few enough, else null. */
function requestedIds(url: URL): string[] | null {
	const ids = url.searchParams.getAll('id');
	return ids.length > 0 && ids.length <= MAX_EXPORT_IDS && ids.every((id) => isConversationId(id)) ? ids : null;
}

export const GET: RequestHandler = async ({ url }) => {
	const scope = tryCrewScope(url.searchParams.get('namespace'), url.searchParams.get('crew'));
	if (!scope) return fail('a valid namespace and crew are required', 400);
	if (url.searchParams.has('allowUnscreened')) return fail('an unscreened export is not available here; use kmctl', 400);
	const ids = requestedIds(url);
	if (!ids) return fail(`id is required: one to ${MAX_EXPORT_IDS} conversation ids of letters, digits, - or _`, 400);
	const denied = await guardNamespace(scope.namespace);
	if (denied) return denied;
	try {
		return await relay(await fetch(upstreamUrl(scope, ids, url), { headers: operatorAuthHeaders() }));
	} catch (e) {
		return fail(`conversation export service unreachable: ${e instanceof Error ? e.message : 'fetch failed'}`, 502);
	}
};
