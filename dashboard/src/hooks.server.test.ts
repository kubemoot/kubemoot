// @vitest-environment node
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { callerToken } from '#lib/server/auth/identity.js';
import { SESSION_COOKIE, createSession, resetSessions } from '#lib/server/auth/session.js';
import { ANONYMOUS_READ_ONLY_ENV, TRUSTED_PROXY_ADDRESSES_ENV, TRUSTED_PROXY_ENV } from '#lib/server/mode.js';
import { resetTrustedProxy } from '#lib/server/auth/trusted-proxy.js';
import * as tokenReview from '#lib/server/auth/token-review.js';
import { handle } from './hooks.server';

const ORIGIN = 'https://dash.example.test';

interface EventOptions {
	cookie?: string;
	origin?: string | null;
	/** The request URL's origin when it is not ORIGIN (adapter-node guesses https without a configured origin). */
	urlOrigin?: string;
	/** Extra request headers, such as X-Forwarded-Proto. */
	headers?: Record<string, string>;
	/** Whether the socket is TLS; omitted means the server cannot tell (no platform). */
	encrypted?: boolean;
	/** The socket's peer address; with it the platform exists even when encrypted is omitted. */
	peer?: string;
}

function event(method: string, path: string, opts: EventOptions = {}) {
	const urlOrigin = opts.urlOrigin ?? ORIGIN;
	const headers: Record<string, string> = { ...opts.headers };
	if (opts.origin !== null && (opts.origin ?? method !== 'GET')) headers.origin = opts.origin ?? ORIGIN;
	const jar = new Map<string, string>(opts.cookie ? [[SESSION_COOKIE, opts.cookie]] : []);
	const deleted: string[] = [];
	return {
		deleted,
		event: {
			request: new Request(`${urlOrigin}${path}`, { method, headers }),
			url: new URL(`${urlOrigin}${path}`),
			platform:
				opts.encrypted === undefined && opts.peer === undefined
					? undefined
					: { req: { socket: { encrypted: opts.encrypted, remoteAddress: opts.peer } } },
			cookies: { get: (n: string) => jar.get(n), delete: (n: string) => deleted.push(n) },
			locals: {} as Record<string, unknown>
		} as unknown as Parameters<typeof handle>[0]['event']
	};
}

async function run(method: string, path: string, opts: EventOptions = {}) {
	let seenToken: string | undefined;
	const resolve = vi.fn(async () => {
		seenToken = callerToken()?.reveal();
		return new Response('route ran');
	});
	const { event: e, deleted } = event(method, path, opts);
	const response = await handle({ event: e, resolve });
	return { response, resolve, seenToken: () => seenToken, locals: e.locals, deleted };
}

beforeEach(() => {
	resetSessions();
	delete process.env[ANONYMOUS_READ_ONLY_ENV];
	delete process.env.ORIGIN;
	delete process.env.PROTOCOL_HEADER;
	delete process.env[TRUSTED_PROXY_ENV];
	delete process.env[TRUSTED_PROXY_ADDRESSES_ENV];
	resetTrustedProxy();
	vi.restoreAllMocks();
});

describe('without a session', () => {
	it('redirects a page to the sign-in page and never runs the route', async () => {
		const { response, resolve } = await run('GET', '/crews');
		expect(response.status).toBe(303);
		expect(response.headers.get('Location')).toBe('/signin');
		expect(resolve).not.toHaveBeenCalled();
	});

	it('answers the API with 401', async () => {
		const { response } = await run('GET', '/api/kubemoot/crews');
		expect(response.status).toBe(401);
	});

	it('runs the health probe', async () => {
		expect((await run('GET', '/api/health')).response.status).toBe(200);
	});
});

describe('with a session', () => {
	it('runs the route as the caller and exposes the username, never the token', async () => {
		const id = createSession('jonathan', 'callers-token', Date.now() + 60_000);
		const { response, seenToken, locals } = await run('GET', '/crews', { cookie: id });
		expect(response.status).toBe(200);
		expect(seenToken()).toBe('callers-token');
		expect(locals.user).toEqual({ username: 'jonathan' });
		expect(JSON.stringify(locals)).not.toContain('callers-token');
	});

	it('removes a cookie whose session is gone', async () => {
		const { deleted, response } = await run('GET', '/crews', { cookie: 'stale' });
		expect(deleted).toContain(SESSION_COOKIE);
		expect(response.status).toBe(303);
	});

	it('serves asking and 405s other writes', async () => {
		const id = createSession('u', 't', Date.now() + 60_000);
		expect((await run('POST', '/api/gateway/ns/c/ask', { cookie: id })).response.status).toBe(200);
		const refused = await run('POST', '/api/nats/publish', { cookie: id });
		expect(refused.response.status).toBe(405);
		expect(refused.response.headers.get('Allow')).toBe('GET, HEAD, OPTIONS');
	});
});

describe('cross-site requests', () => {
	it.each(['https://evil.example.test', null])('are refused on a write (origin %s)', async (origin) => {
		const id = createSession('u', 't', Date.now() + 60_000);
		const { response, resolve } = await run('POST', '/api/gateway/ns/c/ask', { cookie: id, origin });
		expect(response.status).toBe(403);
		expect(resolve).not.toHaveBeenCalled();
	});

	it('are refused on sign-out too', async () => {
		const { response } = await run('POST', '/signout', { origin: 'https://evil.example.test' });
		expect(response.status).toBe(403);
	});
});

describe('the origin when none is configured', () => {
	// adapter-node builds an https:// URL for a request with no ORIGIN, whatever the socket is.
	const guessed = 'https://localhost:18090';

	it('accepts a sign-in made through a plain-HTTP port-forward that names its own http origin', async () => {
		const { response } = await run('POST', '/signin', { urlOrigin: guessed, origin: 'http://localhost:18090', encrypted: false });
		expect(response.status).toBe(200);
	});

	it('refuses an Origin that does not match the connection, even for the same host', async () => {
		const { response, resolve } = await run('POST', '/signin', { urlOrigin: guessed, origin: 'https://localhost:18090', encrypted: false });
		expect(response.status).toBe(403);
		expect(await response.json()).toMatchObject({ expectedOrigin: 'http://localhost:18090' });
		expect(resolve).not.toHaveBeenCalled();
	});

	it('refuses another site and a missing Origin', async () => {
		for (const origin of ['http://evil.example.test', null]) {
			const { response } = await run('POST', '/signin', { urlOrigin: guessed, origin, encrypted: false });
			expect(response.status).toBe(403);
		}
	});

	it('prefers a configured origin over the connection', async () => {
		process.env.ORIGIN = 'https://dash.example.test';
		const url = 'https://dash.example.test';
		expect((await run('POST', '/signin', { urlOrigin: url, origin: url, encrypted: false })).response.status).toBe(200);
		expect((await run('POST', '/signin', { urlOrigin: url, origin: 'http://localhost:18090', encrypted: false })).response.status).toBe(403);
	});

	it('uses the forwarded scheme behind a TLS-ending proxy when forwarded headers are trusted', async () => {
		process.env.PROTOCOL_HEADER = 'x-forwarded-proto';
		const run1 = await run('POST', '/signin', { urlOrigin: 'https://dash.example.test', origin: 'https://dash.example.test', encrypted: false, headers: { 'x-forwarded-proto': 'https' } });
		expect(run1.response.status).toBe(200);
		const plain = await run('POST', '/signin', { urlOrigin: 'https://dash.example.test', origin: 'http://dash.example.test', encrypted: false, headers: { 'x-forwarded-proto': 'https' } });
		expect(plain.response.status).toBe(403);
	});

	it('does not let an untrusted client choose the scheme with a forwarded header', async () => {
		const { response } = await run('POST', '/signin', { urlOrigin: guessed, origin: 'https://localhost:18090', encrypted: false, headers: { 'x-forwarded-proto': 'https' } });
		expect(response.status).toBe(403);
	});
});

describe('anonymous read-only mode', () => {
	beforeEach(() => {
		process.env[ANONYMOUS_READ_ONLY_ENV] = 'true';
	});

	it('serves read views with no caller token', async () => {
		const { response, seenToken } = await run('GET', '/api/kubemoot/crews');
		expect(response.status).toBe(200);
		expect(seenToken()).toBeUndefined();
	});

	it.each([
		['POST', '/api/gateway/ns/c/ask'],
		['DELETE', '/api/kubemoot/crews/x'],
		['POST', '/api/nats/publish'],
		['PATCH', '/api/kubemoot/crewfitnesssuites/ns/x'],
		['GET', '/api/gateway/ns/c/conversations']
	])('cannot reach %s %s', async (method, path) => {
		const { response, resolve } = await run(method, path);
		expect([401, 405]).toContain(response.status);
		expect(resolve).not.toHaveBeenCalled();
	});
});

describe('behind the sign-in proxy', () => {
	const PROXY = [10, 1, 2, 3].join('.');
	const bearer = { authorization: 'Bearer id-token' };
	const accept = () => vi.spyOn(tokenReview, 'reviewToken').mockResolvedValue({ username: 'oidc:ada' });

	beforeEach(() => {
		process.env[TRUSTED_PROXY_ENV] = 'true';
		process.env[TRUSTED_PROXY_ADDRESSES_ENV] = PROXY;
	});

	it('runs the route as the person the proxy vouched for, with their token, and sets no cookie', async () => {
		accept();
		const { response, seenToken, locals } = await run('GET', '/crews', { headers: bearer, peer: PROXY });
		expect(response.status).toBe(200);
		expect(seenToken()).toBe('id-token');
		expect(locals.user).toEqual({ username: 'oidc:ada' });
		expect(locals.sessionId).toBeUndefined();
	});

	it('ignores the header from any other address', async () => {
		const review = accept();
		const { response, resolve } = await run('GET', '/crews', { headers: bearer, peer: [10, 9, 9, 9].join('.') });
		expect(response.status).toBe(303);
		expect(resolve).not.toHaveBeenCalled();
		expect(review).not.toHaveBeenCalled();
	});

	it('ignores the header when the proxy is not turned on', async () => {
		delete process.env[TRUSTED_PROXY_ENV];
		const review = accept();
		const { response } = await run('GET', '/api/kubemoot/crews', { headers: bearer, peer: PROXY });
		expect(response.status).toBe(401);
		expect(review).not.toHaveBeenCalled();
	});

	it('treats a token the API server refuses as signed out', async () => {
		vi.spyOn(tokenReview, 'reviewToken').mockRejectedValue(new tokenReview.SignInError('invalid', 'no'));
		const { response } = await run('GET', '/api/kubemoot/crews', { headers: bearer, peer: PROXY });
		expect(response.status).toBe(401);
	});

	it('lets the proxy token win over a cookie of another person', async () => {
		accept();
		const id = createSession('someone-else', 'cookie-token', Date.now() + 60_000);
		const { seenToken, locals } = await run('GET', '/crews', { headers: bearer, peer: PROXY, cookie: id });
		expect(seenToken()).toBe('id-token');
		expect(locals.user).toEqual({ username: 'oidc:ada' });
	});
});
