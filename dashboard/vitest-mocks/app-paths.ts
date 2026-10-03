export const base = '';
export const assets = '';

/** Test stand-in for $app/paths asset(): a static file's URL under the assets path. */
export function asset(file: string): string {
	return assets + file;
}

/**
 * Test stand-in for $app/paths resolve(): fills [param] segments of a route id from
 * params; with an empty base the pathname is otherwise unchanged.
 */
export function resolve(route: string, params: Record<string, string> = {}): string {
	return base + route.replaceAll(/\[(\w+)\]/g, (segment, name: string) => params[name] ?? segment);
}
