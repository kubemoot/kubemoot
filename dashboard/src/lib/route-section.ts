/**
 * The top-level section of a SvelteKit route id: '/agents/[name]' is 'agents', the
 * root route '/' is ''. Route groups such as '(app)' add no URL segment and are
 * skipped. Null when there is no matched route (an error page).
 */
export function routeSection(routeId: string | null): string | null {
	if (routeId === null) return null;
	return routeId.split('/').find((s) => s !== '' && !/^\(.*\)$/.test(s)) ?? '';
}
