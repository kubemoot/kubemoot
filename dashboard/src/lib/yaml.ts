// A small YAML writer for showing a custom resource read-only. It covers what
// Kubernetes JSON holds (objects, arrays, strings, numbers, booleans, null).

const PLAIN_SAFE = /^[A-Za-z0-9_./][A-Za-z0-9_./ -]*$/;
const RESERVED_WORD = /^(true|false|null|yes|no|on|off|~|0x[0-9a-f]+)$/i;
const RESERVED_NUMBER = /^[-+]?[\d.]+(e[-+]?\d+)?$/i;

function isReserved(value: string): boolean {
	return RESERVED_WORD.test(value) || RESERVED_NUMBER.test(value);
}

function scalar(value: string | number | boolean | null): string {
	if (value === null) return 'null';
	if (typeof value !== 'string') return String(value);
	if (value !== '' && PLAIN_SAFE.test(value) && !value.endsWith(' ') && !isReserved(value)) return value;
	return JSON.stringify(value);
}

function isEmptyContainer(value: unknown): boolean {
	return Array.isArray(value) ? value.length === 0 : typeof value === 'object' && value !== null && Object.keys(value).length === 0;
}

function inline(value: unknown): string {
	if (Array.isArray(value)) return '[]';
	if (typeof value === 'object' && value !== null) return '{}';
	return scalar(value as string | number | boolean | null);
}

function isContainer(value: unknown): boolean {
	return typeof value === 'object' && value !== null && !isEmptyContainer(value);
}

function lines(value: unknown, indent: string): string[] {
	if (Array.isArray(value)) return value.flatMap((item) => arrayItem(item, indent));
	const entries = Object.entries(value as Record<string, unknown>).filter(([, v]) => v !== undefined);
	return entries.flatMap(([key, v]) => {
		const k = scalar(key);
		if (!isContainer(v)) return [`${indent}${k}: ${inline(v)}`];
		return [`${indent}${k}:`, ...lines(v, Array.isArray(v) ? indent : indent + '  ')];
	});
}

function arrayItem(item: unknown, indent: string): string[] {
	if (!isContainer(item)) return [`${indent}- ${inline(item)}`];
	const inner = lines(item, indent + '  ');
	inner[0] = `${indent}- ${inner[0].slice(indent.length + 2)}`;
	return inner;
}

/** The value as YAML text ending in a newline. */
export function toYaml(value: unknown): string {
	if (!isContainer(value)) return inline(value) + '\n';
	return lines(value, '').join('\n') + '\n';
}

/** Any Kubernetes object with optional metadata, whatever its typed metadata interface. */
export interface YamlResource {
	metadata?: object;
}

/** A resource as it should be shown: without the bookkeeping fields that bury the spec. */
export function displayResource<T extends YamlResource>(resource: T): T {
	if (!resource.metadata || !('managedFields' in resource.metadata)) return resource;
	const metadata: Record<string, unknown> = { ...resource.metadata };
	delete metadata.managedFields;
	return { ...resource, metadata };
}
