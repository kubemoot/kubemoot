// The model catalog a ModelServer reports in status.models. Crews ship no Model
// resources; they select models from these catalogs by label, so the Models page
// lists the catalog entries next to any Model resources.

import type { CatalogModel, ModelServer } from '#lib/types/kubemoot.js';

/** A catalog model with the server that offers it. */
export interface CatalogEntry {
	server: string;
	namespace: string;
	model: CatalogModel;
}

/** Every catalog entry across the servers, sorted by model then server. */
export function catalogEntries(servers: ModelServer[]): CatalogEntry[] {
	const entries = servers.flatMap((s) =>
		(s.status?.models ?? []).map((model) => ({
			server: s.metadata.name,
			namespace: s.metadata.namespace ?? 'kubemoot',
			model
		}))
	);
	return entries.sort((a, b) => a.model.name.localeCompare(b.model.name) || a.server.localeCompare(b.server));
}

/** The entries matching the Models page's model and server filters; an empty filter matches all. */
export function filterCatalog(entries: CatalogEntry[], modelFilter: string, serverFilter: string): CatalogEntry[] {
	return entries.filter(
		(e) => (!modelFilter || e.model.name === modelFilter) && (!serverFilter || e.server === serverFilter)
	);
}

/** A context window as "32k tokens", or an empty string when unknown. */
export function contextLabel(tokens: number | undefined): string {
	if (!tokens || tokens <= 0) return '';
	return tokens >= 1000 ? `${Math.round(tokens / 1000)}k tokens` : `${tokens} tokens`;
}

/** A parameter count as "32B", or an empty string when unknown. */
export function parameterLabel(count: number | undefined): string {
	if (!count || count <= 0) return '';
	if (count >= 1e9) return `${+(count / 1e9).toFixed(1)}B`;
	return `${Math.round(count / 1e6)}M`;
}
