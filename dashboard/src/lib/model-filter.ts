/** Query parameters the models page filters on. */
const MODEL_FILTER_PARAMS = ['model', 'provider'] as const;

/**
 * The path and query of `url` with the models page filter removed. Takes any URL-like
 * value with an href, because SvelteKit's page.url is read-only and is copied first.
 */
export function withoutModelFilter(url: { href: string }): string {
	const target = new URL(url.href);
	for (const param of MODEL_FILTER_PARAMS) target.searchParams.delete(param);
	return target.pathname + target.search;
}
