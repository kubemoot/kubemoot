// Small string helpers shared by the server routes and the client stores.

/** The string without any run of trailing '/' characters, scanned in linear time. */
export function trimTrailingSlashes(value: string): string {
	let end = value.length;
	while (end > 0 && value[end - 1] === '/') end--;
	return value.slice(0, end);
}

/**
 * Orders strings by UTF-16 code unit, the order Array#sort uses without a compare
 * function. Use it where the order must stay byte-wise rather than locale-aware.
 */
export function compareCodeUnits(a: string, b: string): number {
	if (a < b) return -1;
	return a > b ? 1 : 0;
}

/** A PascalCase or camelCase word split into words: 'NotReady' becomes 'Not Ready'. */
export function splitCamelCase(value: string): string {
	return value.replaceAll(/([a-z])([A-Z])/g, '$1 $2');
}

/** A readable description of a thrown or reported value of unknown type. */
export function describeError(err: unknown): string {
	if (typeof err === 'string') return err;
	if (err instanceof Error) return String(err);
	return JSON.stringify(err) ?? String(err);
}
