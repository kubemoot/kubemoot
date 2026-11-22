import type { Handle } from '@sveltejs/kit/hooks';
import { appBase } from '#lib/server/auth/base-path.js';
import { jsonError } from '#lib/server/json-error.js';
import { anonymousReadOnly } from '#lib/server/mode.js';
import { decideAccess, isWrite, pathInApp, type Decision } from '#lib/server/auth/access.js';
import { runWithIdentity, type RequestIdentity } from '#lib/server/auth/identity.js';
import { sameOrigin } from '#lib/server/auth/same-origin.js';
import { SESSION_COOKIE, getSession, type Session } from '#lib/server/auth/session.js';

function refuseOrRedirect(decision: Exclude<Decision, { action: 'allow' }>): Response {
	if (decision.action === 'redirect') return new Response(null, { status: 303, headers: { Location: appBase() + decision.to } });
	const headers = decision.status === 405 ? { Allow: 'GET, HEAD, OPTIONS' } : undefined;
	return jsonError(decision.status, decision.error, decision.extra, headers);
}

/** The live session of the request's cookie; a cookie whose session is gone or expired is removed. */
function sessionOf(event: Parameters<Handle>[0]['event']): Session | undefined {
	const cookie = event.cookies.get(SESSION_COOKIE);
	const session = getSession(cookie);
	if (cookie && !session) event.cookies.delete(SESSION_COOKIE, { path: appBase() || '/' });
	event.locals.user = session ? { username: session.username } : null;
	event.locals.sessionId = session ? cookie : undefined;
	return session;
}

/**
 * The one gate in front of every route.
 *
 * 1. A request that could change something must come from the dashboard's own origin (CSRF).
 * 2. The session cookie, an opaque id, finds the signed-in person's session in server memory.
 * 3. A visitor with no session sees the sign-in page for every page, or a 401 for the API,
 *    unless anonymous read-only mode is on, which allows reads only.
 * 4. Of the writes, only sign-in, sign-out and asking a crew are served; every other method
 *    but GET, HEAD and OPTIONS is a 405.
 * 5. The route runs as the person: every Kubernetes call it makes uses their token.
 */
export const handle: Handle = async ({ event, resolve }) => {
	const method = event.request.method.toUpperCase();
	if (isWrite(method) && !sameOrigin(event.request, event.url)) {
		return jsonError(403, `Cross-origin request refused: expected an Origin of ${event.url.origin}.`, { expectedOrigin: event.url.origin });
	}
	const session = sessionOf(event);
	const decision = decideAccess({
		method,
		path: pathInApp(event.url.pathname, appBase()),
		signedIn: session !== undefined,
		anonymousReadOnly: anonymousReadOnly()
	});
	if (decision.action !== 'allow') return refuseOrRedirect(decision);

	const identity: RequestIdentity = session
		? { kind: 'caller', username: session.username, token: session.token, ended: session.ended }
		: { kind: 'anonymous' };
	return runWithIdentity(identity, () => resolve(event));
};
