import { error } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { operatorReportBase } from '$lib/server/operator-report';
import { guardNamespace } from '$lib/server/scope';

/**
 * GET /api/kubemoot/crewfitnesssuites/{namespace}/{name}/artifact
 *
 * Generates the fitness XLSX ON DEMAND by delegating to the operator's report
 * endpoint, which builds it fresh from the per-iteration transcripts using the
 * currently deployed generator. No pre-baked artifact is read - every download
 * reflects the latest report format/code (no staleness, no manual overwrites).
 *
 *   200 + spreadsheet bytes on success
 *   404 when the suite/transcripts aren't found
 *   502 when the operator report service is unreachable or errors
 */
export const GET: RequestHandler = async ({ params }) => {
	const { namespace, name } = params;
	const denied = await guardNamespace(namespace);
	if (denied) return denied;
	if (!namespace || !name) {
		throw error(400, 'namespace and name are required');
	}

	const url = `${operatorReportBase()}/report/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`;
	let upstream: Response;
	try {
		upstream = await fetch(url);
	} catch (e) {
		const msg = e instanceof Error ? e.message : 'fetch failed';
		throw error(502, `report service unreachable: ${msg}`);
	}

	if (!upstream.ok) {
		const body = await upstream.text().catch(() => '');
		throw error(upstream.status === 404 ? 404 : 502, `report generation failed: ${body || upstream.status}`);
	}

	const buf = Buffer.from(await upstream.arrayBuffer());
	return new Response(buf, {
		status: 200,
		headers: {
			'Content-Type': 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
			'Content-Disposition': `attachment; filename="${name}.xlsx"`,
			'Content-Length': String(buf.byteLength),
			'Cache-Control': 'no-store'
		}
	});
};
