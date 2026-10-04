export const base = '';
export const assets = '';

/** Test stand-in for $app/paths asset(): a static file's URL under the assets path. */
export function asset(file: string): string {
	return (assets || base) + '/' + file.replace(/^\//, '');
}

/**
 * Test stand-in for $app/paths resolve(): a route id (leading slash) has its [param]
 * segments filled from params; a pathname without the slash is placed under the base.
 */
export function resolve(route: string, params: Record<string, string> = {}): string {
	if (!route.startsWith('/')) return base + '/' + route;
	return base + route.replaceAll(/\[(\w+)\]/g, (segment, name: string) => params[name] ?? segment);
}
