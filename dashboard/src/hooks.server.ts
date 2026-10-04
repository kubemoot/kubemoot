import type { Handle } from '@sveltejs/kit/hooks';
import { isReadOnly, SAFE_METHODS } from '#lib/server/mode.js';

/**
 * Read-only mode (KUBEMOOT_DASHBOARD_READ_ONLY=true): refuse every request whose
 * method is not GET, HEAD or OPTIONS before any route or SvelteKit form action runs.
 * The rule is on the method alone, so a write route added later is refused with no
 * change here.
 */
export const handle: Handle = async ({ event, resolve }) => {
	if (isReadOnly() && !SAFE_METHODS.has(event.request.method.toUpperCase())) {
		return Response.json(
			{
				error: 'This dashboard is read-only: requests that change state are refused.',
				readOnly: true,
				method: event.request.method
			},
			{ status: 403, headers: { Allow: 'GET, HEAD, OPTIONS' } }
		);
	}
	return resolve(event);
};
