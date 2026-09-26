import { json, error } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { env } from '$env/dynamic/private';

/**
 * GET /api/kubemoot/componentstatuses
 *
 * Proxies the operator's control-plane health endpoint (operator self, NATS,
 * model providers). The operator is the source of truth for its own topology —
 * the dashboard does not scrape pods or poke ports. Returns the operator's JSON
 * array of { name, healthy, message } verbatim.
 *
 *   200 + statuses on success
 *   502 when the operator report service is unreachable or errors (the Overview
 *       pane treats any non-200 as "component status unavailable")
 */
const reportBase = () =>
	env.OPERATOR_REPORT_URL || 'http://kubemoot-operator-report.kubemoot:8082';

export const GET: RequestHandler = async () => {
	let upstream: Response;
	try {
		upstream = await fetch(`${reportBase()}/componentstatuses`);
	} catch (e) {
		const msg = e instanceof Error ? e.message : 'fetch failed';
		throw error(502, `component status service unreachable: ${msg}`);
	}

	if (!upstream.ok) {
		const body = await upstream.text().catch(() => '');
		throw error(502, `component status lookup failed: ${body || upstream.status}`);
	}

	return json(await upstream.json(), { headers: { 'Cache-Control': 'no-store' } });
};
