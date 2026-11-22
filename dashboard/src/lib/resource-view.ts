// The views every Kubemoot resource page offers (Description, YAML, JSON and KYAML), how the choice
// lives in the URL, and the text the text views show.

import { displayResource, toYaml, type YamlResource } from '#lib/yaml.js';
import { highlightJson } from '#lib/json-highlight.js';
import { toKyaml } from '#lib/kyaml.js';
import { highlightKyaml } from '#lib/kyaml-highlight.js';
import { highlightYaml, type YamlToken } from '#lib/yaml-highlight.js';

/** The formats the text views write. */
export type TextFormat = 'yaml' | 'json' | 'kyaml';

export type ResourceView = 'description' | TextFormat;

export const VIEWS: readonly { id: ResourceView; label: string }[] = [
	{ id: 'description', label: 'Description' },
	{ id: 'yaml', label: 'YAML' },
	{ id: 'json', label: 'JSON' },
	{ id: 'kyaml', label: 'KYAML' }
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
	return VIEWS.some((v) => v.id === named && v.id !== 'description') ? (named as TextFormat) : 'description';
}

/** The page URL with the view chosen; the Description is the default and leaves no parameter. */
export function viewUrl(url: UrlParts, view: ResourceView): string {
	const next = new URL(url.pathname + url.search + url.hash, 'https://page.invalid');
	next.searchParams.delete('tab');
	if (view === 'description') next.searchParams.delete('view');
	else next.searchParams.set('view', view);
	return next.pathname + next.search + next.hash;
}

const WRITERS: Record<TextFormat, (shown: YamlResource) => string> = {
	yaml: toYaml,
	json: (shown) => JSON.stringify(shown, null, 2) + '\n',
	kyaml: toKyaml
};

const HIGHLIGHTERS: Record<TextFormat, (text: string) => YamlToken[][]> = {
	yaml: highlightYaml,
	json: highlightJson,
	kyaml: highlightKyaml
};

/** The resource as text in the format: YAML, two-space JSON, or KYAML (as `kubectl get -o kyaml` writes it), each ending in a newline. */
export function resourceText(resource: YamlResource, format: TextFormat, showManagedFields: boolean): string {
	return WRITERS[format](displayResource(resource, showManagedFields));
}

/** The text as coloured pieces, line by line. */
export function resourceLines(text: string, format: TextFormat): YamlToken[][] {
	return HIGHLIGHTERS[format](text);
}
