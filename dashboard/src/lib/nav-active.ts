/**
 * Whether a sidebar link is the current page: the root link matches only the root,
 * any other link matches its own path and the pages beneath it. A path that merely
 * starts with the link's text (/modelservers against /models) is a different page.
 */
export function isActivePath(href: string, currentPath: string, root: string): boolean {
	if (href === root) {
		return currentPath === root || currentPath === root.slice(0, -1);
	}
	const path = currentPath.length > 1 ? currentPath.replace(/\/$/, '') : currentPath;
	return path === href || path.startsWith(href + '/');
}
