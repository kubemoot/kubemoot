import type { Link } from './overview-model.js';

/** The address of a link, given the app's root address (resolve('/') adds the base path). */
export function linkHref(link: Link, root: string): string {
	return `${root.replace(/\/$/, '')}${link.path}${link.search ?? ''}`;
}
