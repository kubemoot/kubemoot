import type { Handle } from '@sveltejs/kit/hooks';
import { SAFE_METHODS } from '#lib/server/mode.js';

/**
 * The dashboard is read-only: refuse every request whose method is not GET, HEAD or
 * OPTIONS before any route or SvelteKit form action runs. The rule is on the method
 * alone, so a write route added later is refused with no change here.
 */
export const handle: Handle = async ({ event, resolve }) => {
	if (!SAFE_METHODS.has(event.request.method.toUpperCase())) {
		return Response.json(
			{
				error: 'This dashboard is read-only: use kmctl, CrewForge or kubectl to make changes.',
				readOnly: true,
				method: event.request.method
			},
			{ status: 405, headers: { Allow: 'GET, HEAD, OPTIONS' } }
		);
	}
	return resolve(event);
};
