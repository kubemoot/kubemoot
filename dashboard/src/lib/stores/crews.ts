import { writable } from 'svelte/store';
import type { CrewEntry } from '#lib/crew-display-name.js';

/**
 * The crews deployed in the cluster (namespace, technical name, display name),
 * loaded once by the root layout. The top-bar selector and the pages read it to
 * scope by crew and to show display names beside crew references.
 */
export const crewDirectory = writable<CrewEntry[]>([]);

/**
 * Fills crewDirectory from the crew-namespaces endpoint (`{ crews: CrewEntry[] }`).
 * Any failure or unexpected body leaves the directory empty. An answer that arrives after
 * `current()` has turned false (the person signed out or in again meanwhile) is dropped.
 */
export async function loadCrewDirectory(fetchFn: typeof fetch, url: string, current: () => boolean = () => true): Promise<void> {
	let crews: CrewEntry[];
	try {
		const res = await fetchFn(url);
		const body = ((await res.json()) as { crews?: unknown } | null)?.crews;
		crews = Array.isArray(body) ? (body as CrewEntry[]) : [];
	} catch {
		crews = [];
	}
	if (current()) crewDirectory.set(crews);
}
