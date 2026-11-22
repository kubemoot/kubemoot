import { redirect } from '@sveltejs/kit';
import { resolve } from '$app/paths';
import type { PageServerLoad } from './$types';

/**
 * The Conversations page was called Messages. The old address stays as a permanent
 * redirect, so bookmarks and links in docs keep working; the query string carries over.
 */
export const load: PageServerLoad = ({ url }) => {
	redirect(308, resolve('/conversations') + url.search);
};
