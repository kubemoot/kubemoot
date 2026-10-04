import type { RequestHandler } from './$types';

/**
 * Returns NATS configuration for the dashboard.
 * Note: Browser clients no longer connect directly to NATS.
 * Instead, they use the SSE proxy at /api/nats/subscribe.
 */
export const GET: RequestHandler = async () => {
	return Response.json({
		available: !!process.env.NATS_URL,
		// Internal URL used by server-side proxy only (not exposed to browser)
		proxyEndpoint: '/api/nats/subscribe'
	});
};
