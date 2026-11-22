import { redirect } from '@sveltejs/kit';
import { resolve } from '$app/paths';
import type { PageServerLoad } from './$types';

/**
 * The Moot page was called Discussions. The old address stays as a permanent redirect, so
 * bookmarks and links in docs keep working; the query string (such as ?thread=) carries over.
 */
export const load: PageServerLoad = ({ url }) => {
	redirect(308, resolve('/moot') + url.search);
};
