import { resolve } from '$app/paths';

/** The path the dashboard is served under ("/dashboard", or "" at the root), without a trailing slash. */
export function appBase(): string {
	const root = resolve('/');
	return root.endsWith('/') ? root.slice(0, -1) : root;
}
