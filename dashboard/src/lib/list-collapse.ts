// Whether a viewer has collapsed the list column of a list-and-detail page (the Conversations page
// and the Moot), kept in this browser so the page opens the way they left it. With storage
// unavailable the list is shown.

import { readItem, writeItem } from '#lib/safe-storage.js';

const key = (route: string) => `kubemoot-dashboard-list-collapsed-${route.replace(/^\//, '')}`;

/** True only when the viewer collapsed this page's list and the choice could be read. */
export function loadCollapsed(route: string): boolean {
	return readItem(key(route)) === 'true';
}

/** Remembers the choice; does nothing when storage is unavailable or full. */
export function saveCollapsed(route: string, collapsed: boolean): void {
	writeItem(key(route), String(collapsed));
}
