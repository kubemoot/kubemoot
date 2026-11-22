import type { RequestHandler } from './$types';
import { readOperatorDetail } from '#lib/server/k8s/operator.js';

/**
 * GET /api/kubemoot/operator
 *
 * The operator's version, uptime, leader, Deployment conditions and the Kubemoot CRDs, each read
 * with the caller's token. A part the caller's RBAC refuses (or that cannot be read) says why in
 * its own reading, so a page never shows a zero for a refusal. The answer is always 200.
 */
export const GET: RequestHandler = async () => Response.json(await readOperatorDetail(), { headers: { 'Cache-Control': 'no-store' } });
