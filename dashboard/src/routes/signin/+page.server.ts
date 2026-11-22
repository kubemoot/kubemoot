import { fail, redirect } from '@sveltejs/kit';
import { appBase } from '#lib/server/auth/base-path.js';
import { signIn } from '#lib/server/auth/sign-in.js';
import { SignInError, type SignInFailure } from '#lib/server/auth/token-review.js';
import { SESSION_COOKIE, destroySession } from '#lib/server/auth/session.js';
import { allowSignInAttempt } from '#lib/server/auth/rate-limit.js';
import { sessionCookieOptions } from '#lib/server/auth/cookie.js';
import type { Actions } from './$types';

const STATUS: Record<SignInFailure, number> = {
	empty: 400,
	malformed: 400,
	invalid: 401,
	expired: 401,
	unavailable: 503
};

/** The client's address, or a shared name when the platform cannot say (all such clients share one limit). */
function clientOf(getClientAddress: () => string): string {
	try {
		return getClientAddress();
	} catch {
		return 'unknown';
	}
}

export const actions: Actions = {
	/** Checks the pasted token with the API server and starts a session; the token is never sent back to the page. */
	default: async ({ request, cookies, url, getClientAddress }) => {
		if (!allowSignInAttempt(clientOf(getClientAddress))) {
			return fail(429, { message: 'Too many sign-in attempts. Wait a minute and try again.' });
		}
		const form = await request.formData();
		const pasted = form.get('token');
		try {
			const signedIn = await signIn(typeof pasted === 'string' ? pasted : '');
			// Signing in again replaces the session this browser had; the old token is forgotten.
			destroySession(cookies.get(SESSION_COOKIE));
			cookies.set(SESSION_COOKIE, signedIn.sessionId, sessionCookieOptions(appBase(), signedIn.expiresAt, url));
		} catch (err) {
			if (err instanceof SignInError) return fail(STATUS[err.failure], { message: err.message });
			throw err;
		}
		redirect(303, `${appBase()}/`);
	}
};
