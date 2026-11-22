// The three views every Kubemoot resource page offers, how the choice lives in the URL, and the
// text the YAML and JSON views show.

import { displayResource, toYaml, type YamlResource } from '#lib/yaml.js';
import { highlightJson } from '#lib/json-highlight.js';
import { highlightYaml, type YamlToken } from '#lib/yaml-highlight.js';

export type ResourceView = 'description' | 'yaml' | 'json';

export const VIEWS: readonly { id: ResourceView; label: string }[] = [
	{ id: 'description', label: 'Description' },
	{ id: 'yaml', label: 'YAML' },
	{ id: 'json', label: 'JSON' }
];

/** What a page URL offers: SvelteKit's read-only page.url fits, and so does a URL. */
export interface UrlParts {
	pathname: string;
	search: string;
	hash: string;
	searchParams: { get(name: string): string | null };
}

/** The view named by ?view=; anything else is the Description. A link with the older ?tab=yaml still opens the YAML. */
export function viewFromUrl(url: UrlParts): ResourceView {
	const named = url.searchParams.get('view') ?? (url.searchParams.get('tab') === 'yaml' ? 'yaml' : null);
	return named === 'yaml' || named === 'json' ? named : 'description';
}

/** The page URL with the view chosen; the Description is the default and leaves no parameter. */
export function viewUrl(url: UrlParts, view: ResourceView): string {
	const next = new URL(url.pathname + url.search + url.hash, 'https://page.invalid');
	next.searchParams.delete('tab');
	if (view === 'description') next.searchParams.delete('view');
	else next.searchParams.set('view', view);
	return next.pathname + next.search + next.hash;
}

/** The resource as text in the format: YAML, or two-space JSON, each ending in a newline. */
export function resourceText(resource: YamlResource, format: 'yaml' | 'json', showManagedFields: boolean): string {
	const shown = displayResource(resource, showManagedFields);
	return format === 'yaml' ? toYaml(shown) : JSON.stringify(shown, null, 2) + '\n';
}

/** The text as coloured pieces, line by line. */
export function resourceLines(text: string, format: 'yaml' | 'json'): YamlToken[][] {
	return format === 'yaml' ? highlightYaml(text) : highlightJson(text);
}
