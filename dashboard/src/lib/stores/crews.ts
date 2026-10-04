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
 * Any failure or unexpected body leaves the directory empty.
 */
export async function loadCrewDirectory(fetchFn: typeof fetch, url: string): Promise<void> {
	try {
		const res = await fetchFn(url);
		const data: unknown = await res.json();
		const crews = (data as { crews?: unknown } | null)?.crews;
		crewDirectory.set(Array.isArray(crews) ? (crews as CrewEntry[]) : []);
	} catch {
		crewDirectory.set([]);
	}
}
