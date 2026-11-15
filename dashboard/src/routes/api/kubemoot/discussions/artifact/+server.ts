import type { RequestHandler } from './$types';

/**
 * GET /api/kubemoot/discussions/artifact?key=...
 *
 * The older name of /api/kubemoot/discussions/workspace. It answers with a 308 to the new route
 * for one release, so a link or script that still uses it keeps working, and says so in a
 * Deprecation header. The new route does the key check and the namespace guard.
 */
let warned = false;

export const GET: RequestHandler = ({ url }) => {
	if (!warned) {
		warned = true;
		console.warn(
			'GET /api/kubemoot/discussions/artifact is deprecated and is removed in the next release; use /api/kubemoot/discussions/workspace'
		);
	}
	const target = new URL(url);
	target.pathname = target.pathname.replace(/\/artifact$/, '/workspace');
	return new Response(null, {
		status: 308,
		headers: {
			Location: `${target.pathname}${target.search}`,
			Deprecation: 'true',
			'Cache-Control': 'no-store',
			Link: `<${target.pathname}>; rel="successor-version"`
		}
	});
};
