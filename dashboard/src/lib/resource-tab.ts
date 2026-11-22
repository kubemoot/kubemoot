// The two views every Kubemoot resource page offers, and how the choice lives in the URL.

export type ResourceTab = 'overview' | 'yaml';

export const TABS: readonly { id: ResourceTab; label: string }[] = [
	{ id: 'overview', label: 'Overview' },
	{ id: 'yaml', label: 'YAML' }
];

/** What a page URL offers: SvelteKit's read-only page.url fits, and so does a URL. */
export interface UrlParts {
	pathname: string;
	search: string;
	hash: string;
	searchParams: { get(name: string): string | null };
}

/** The tab named by ?tab=; anything else is the Overview. */
export function tabFromUrl(url: UrlParts): ResourceTab {
	return url.searchParams.get('tab') === 'yaml' ? 'yaml' : 'overview';
}

/** The page URL with the tab chosen; the Overview is the default and leaves no parameter. */
export function tabUrl(url: UrlParts, tab: ResourceTab): string {
	const next = new URL(url.pathname + url.search + url.hash, 'https://page.invalid');
	if (tab === 'overview') next.searchParams.delete('tab');
	else next.searchParams.set('tab', tab);
	return next.pathname + next.search + next.hash;
}

/** The tab an arrow, Home or End key moves to, or null for any other key. */
export function nextTab(current: ResourceTab, key: string): ResourceTab | null {
	const ids = TABS.map((t) => t.id);
	const at = ids.indexOf(current);
	if (key === 'ArrowRight') return ids[(at + 1) % ids.length];
	if (key === 'ArrowLeft') return ids[(at - 1 + ids.length) % ids.length];
	if (key === 'Home') return ids[0];
	if (key === 'End') return ids[ids.length - 1];
	return null;
}
