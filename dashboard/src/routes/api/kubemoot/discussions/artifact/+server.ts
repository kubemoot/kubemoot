import type { RequestHandler } from './$types';

/**
 * GET /api/kubemoot/discussions/artifact?key=...
 *
 * The older name of /api/kubemoot/discussions/workspace. It answers with a 308 to the new route
 * for one release, so a link or script that still uses it keeps working, and says so in a
 * Deprecation header. The new route does the key check and the namespace guard.
 */
export const GET: RequestHandler = ({ url }) => {
	const target = new URL(url);
	target.pathname = target.pathname.replace(/\/artifact$/, '/workspace');
	return new Response(null, {
		status: 308,
		headers: {
			Location: `${target.pathname}${target.search}`,
			Deprecation: 'true',
			Link: `<${target.pathname}>; rel="successor-version"`
		}
	});
};
