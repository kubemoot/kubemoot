// What the Conversations list and the Moot list work out the same way: the address a filter
// change goes to, and the crews whose conversations "Mine" asks each crew's gateway for.

import { resolve } from '$app/paths';
import type { CrewEntry } from '#lib/crew-display-name.js';
import { tryCrewScope, type CrewScope } from '#lib/crewScope.js';
import { withFilter, type ListFilter } from '#lib/conversation-filter.js';

/** The address of a page with the filter written into its query; the other parameters stay as they are. */
export function listAddress(route: '/conversations' | '/moot', search: string, next: ListFilter): string {
	const query = withFilter(new URLSearchParams(search), next).toString();
	return resolve(route) + (query ? `?${query}` : '');
}

/** The selected crew, or with none selected every crew that has a usable scope. */
export function mineScopesFor(selected: CrewScope | null, directory: readonly CrewEntry[]): CrewScope[] {
	if (selected) return [selected];
	return directory.flatMap((entry) => {
		const scope = tryCrewScope(entry.namespace, entry.crew);
		return scope ? [scope] : [];
	});
}
