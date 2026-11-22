import { redirect } from '@sveltejs/kit';
import { appBase } from '#lib/server/auth/base-path.js';
import { SESSION_COOKIE, destroySession } from '#lib/server/auth/session.js';
import type { RequestHandler } from './$types';

/** Signs out: the session and the token it held are forgotten, and the cookie is removed. */
export const POST: RequestHandler = ({ cookies }) => {
	destroySession(cookies.get(SESSION_COOKIE));
	cookies.delete(SESSION_COOKIE, { path: appBase() || '/' });
	redirect(303, `${appBase()}/signin`);
};
