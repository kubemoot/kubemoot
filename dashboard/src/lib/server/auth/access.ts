// Who may reach what, decided from the method, the path and whether the visitor is signed in.
// Kept free of SvelteKit so every rule is a plain function a test can call.

import { SAFE_METHODS } from '../mode.js';

export type Decision =
	| { action: 'allow' }
	| { action: 'redirect'; to: string }
	| { action: 'deny'; status: number; error: string; extra?: Record<string, unknown> };

export interface AccessInput {
	method: string;
	/** The request path with the app's base path removed, starting with "/". */
	path: string;
	signedIn: boolean;
	anonymousReadOnly: boolean;
}

/** Reachable without a session: the sign-in page, and the probes and static files that carry no data. */
const PUBLIC: readonly RegExp[] = [/^\/(signin|api\/health|_app)(\/|$)/, /^\/favicon/];

/** The writes the dashboard server accepts: signing in and out, asking a crew, and acting on a conversation. */
interface WriteRoute {
	method: string;
	pattern: RegExp;
}

const WRITE_ROUTES: readonly WriteRoute[] = [
	{ method: 'POST', pattern: /^\/signin\/?$/ },
	{ method: 'POST', pattern: /^\/signout\/?$/ },
	{ method: 'POST', pattern: /^\/api\/gateway\/[^/]+\/[^/]+\/ask\/?$/ },
	// stop, pause, resume and delete a conversation, as the signed-in person
	{ method: 'POST', pattern: /^\/api\/gateway\/[^/]+\/[^/]+\/[^/]+\/(stop|pause|resume)\/?$/ },
	{ method: 'DELETE', pattern: /^\/api\/gateway\/[^/]+\/[^/]+\/[^/]+\/?$/ }
];

function isWriteRoute(method: string, path: string): boolean {
	return WRITE_ROUTES.some((r) => r.method === method.toUpperCase() && r.pattern.test(path));
}

/** Routes that act as the signed-in person at a crew's gateway: no session, no access, even in anonymous read-only mode. */
const CALLER_ONLY: readonly RegExp[] = [/^\/api\/gateway\//];

export const SIGN_IN_PATH = '/signin';

function matches(path: string, patterns: readonly RegExp[]): boolean {
	return patterns.some((p) => p.test(path));
}

function isPublic(path: string): boolean {
	return matches(path, PUBLIC);
}

/** Whether the method is one that could change something. */
export function isWrite(method: string): boolean {
	return !SAFE_METHODS.has(method.toUpperCase());
}

/** The path inside the app: the base path removed. A path outside the base is returned as it is. */
export function pathInApp(pathname: string, base: string): string {
	if (base && (pathname === base || pathname.startsWith(`${base}/`))) return pathname.slice(base.length) || '/';
	return pathname;
}

function writeDecision(input: AccessInput): Decision {
	if (!isWriteRoute(input.method, input.path)) {
		return {
			action: 'deny',
			status: 405,
			error: 'This dashboard is read-only: use kmctl, CrewForge or kubectl to make changes.',
			extra: { readOnly: true, method: input.method }
		};
	}
	if (!input.signedIn && !isPublic(input.path)) return unauthorized(input.path);
	return { action: 'allow' };
}

function unauthorized(path: string): Decision {
	if (path.startsWith('/api/')) {
		return { action: 'deny', status: 401, error: 'Sign in to use the dashboard.', extra: { signIn: true } };
	}
	return { action: 'redirect', to: SIGN_IN_PATH };
}

/** Decides one request: allow it, send the visitor to the sign-in page, or refuse it. */
export function decideAccess(input: AccessInput): Decision {
	if (isWrite(input.method)) return writeDecision(input);
	if (input.signedIn) {
		return input.path === SIGN_IN_PATH ? { action: 'redirect', to: '/' } : { action: 'allow' };
	}
	if (isPublic(input.path)) return { action: 'allow' };
	if (input.anonymousReadOnly && !matches(input.path, CALLER_ONLY)) return { action: 'allow' };
	return unauthorized(input.path);
}
