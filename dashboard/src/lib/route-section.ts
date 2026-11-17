/**
 * The top-level section of a SvelteKit route id: '/agents/[name]' is 'agents', the
 * root route '/' is ''. Route groups such as '(app)' add no URL segment and are
 * skipped. Null when there is no matched route (an error page).
 */
export function routeSection(routeId: string | null): string | null {
	if (routeId === null) return null;
	return routeId.split('/').find((s) => s !== '' && !/^\(.*\)$/.test(s)) ?? '';
}

/** The sections whose pages are not scoped to a crew, so the top bar hides the Crew selector. */
const SECTIONS_WITHOUT_CREW = new Set(['nodes', 'messages', 'verify', 'config']);

/**
 * Whether the top bar shows the Crew selector on a page. The Overview and Discussions
 * are crew-scoped, so they show it; an error page (no matched route) shows it too.
 */
export function showsCrewSelector(routeId: string | null): boolean {
	const section = routeSection(routeId);
	return section === null || !SECTIONS_WITHOUT_CREW.has(section);
}
