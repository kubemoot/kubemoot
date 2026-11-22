// @vitest-environment node
import { inspect } from 'node:util';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { BearerToken } from './bearer-token';
import { callerToken, runWithIdentity } from './identity';
import { MAX_SESSIONS, createSession, destroySession, getSession, resetSessions, sessionCount } from './session';
import { cleanToken, sessionExpiry, signIn, tokenExpiry } from './sign-in';
import { SignInError, type TokenReviewer } from './token-review';
import { sessionCookieOptions } from './cookie';
import { sameOrigin } from './same-origin';

const NOW = Date.UTC(2026, 10, 21, 12, 0, 0);
const HOUR = 3_600_000;

function jwt(exp?: number): string {
	const part = (o: object) => Buffer.from(JSON.stringify(o)).toString('base64url');
	return `${part({ alg: 'none' })}.${part(exp === undefined ? {} : { exp })}.sig`;
}

const accepts: TokenReviewer = async () => ({ username: 'jonathan' });
const refuses: TokenReviewer = async () => {
	throw new SignInError('invalid', 'no');
};

beforeEach(() => resetSessions());
afterEach(() => vi.restoreAllMocks());

describe('signIn', () => {
	it('starts a session for a token the cluster accepts', async () => {
		const review = vi.fn(accepts);
		const result = await signIn('  Bearer abc.def  ', review, () => NOW);
		expect(review).toHaveBeenCalledWith('abc.def');
		expect(result.username).toBe('jonathan');
		expect(getSession(result.sessionId, NOW)?.username).toBe('jonathan');
	});

	it('refuses a token the cluster rejects and starts no session', async () => {
		await expect(signIn('abc', refuses, () => NOW)).rejects.toMatchObject({ failure: 'invalid' });
		expect(sessionCount()).toBe(0);
	});

	it('refuses a token that has already expired without asking the cluster', async () => {
		const review = vi.fn(accepts);
		await expect(signIn(jwt((NOW - HOUR) / 1000), review, () => NOW)).rejects.toMatchObject({ failure: 'expired' });
		expect(review).not.toHaveBeenCalled();
	});

	it.each([
		['', 'empty'],
		['   ', 'empty'],
		['two words', 'malformed'],
		['line\nbreak', 'malformed'],
		['x'.repeat(17_000), 'malformed']
	])('refuses %j before asking the cluster', async (input, failure) => {
		const review = vi.fn(accepts);
		await expect(signIn(input, review, () => NOW)).rejects.toMatchObject({ failure });
		expect(review).not.toHaveBeenCalled();
	});

	it('reports a review that could not be made as unavailable, not as a bad token', async () => {
		const down: TokenReviewer = async () => {
			throw new SignInError('unavailable', 'down');
		};
		await expect(signIn('abc', down, () => NOW)).rejects.toMatchObject({ failure: 'unavailable' });
	});
});

describe('session expiry', () => {
	it('ends no later than the token', () => {
		expect(sessionExpiry(jwt((NOW + HOUR) / 1000), NOW, 8 * HOUR)).toBe(NOW + HOUR);
	});

	it('ends at the configured maximum when the token lasts longer or has no expiry', () => {
		expect(sessionExpiry(jwt((NOW + 20 * HOUR) / 1000), NOW, 8 * HOUR)).toBe(NOW + 8 * HOUR);
		expect(sessionExpiry('opaque-token', NOW, 8 * HOUR)).toBe(NOW + 8 * HOUR);
	});

	it('reads the expiry of a JWT only', () => {
		expect(tokenExpiry(jwt(100))).toBe(100_000);
		expect(tokenExpiry(jwt())).toBeUndefined();
		expect(tokenExpiry('a.b')).toBeUndefined();
		expect(tokenExpiry('a.!!.c')).toBeUndefined();
	});

	it('forgets an expired session', () => {
		const id = createSession('u', 't', NOW + HOUR, NOW);
		expect(getSession(id, NOW + 2 * HOUR)).toBeUndefined();
		expect(sessionCount()).toBe(0);
	});

	it('forgets a session on sign-out', () => {
		const id = createSession('u', 't', NOW + HOUR, NOW);
		destroySession(id);
		expect(getSession(id, NOW)).toBeUndefined();
	});

	it('holds a bounded number of sessions', () => {
		for (let i = 0; i < MAX_SESSIONS + 5; i++) createSession(`u${i}`, 't', NOW + HOUR, NOW);
		expect(sessionCount()).toBe(MAX_SESSIONS);
	});
});

describe('the session cookie', () => {
	it('is HttpOnly, Secure, SameSite=Strict, scoped to the dashboard and ends with the session', () => {
		const options = sessionCookieOptions('/dashboard', NOW + HOUR);
		expect(options).toMatchObject({ httpOnly: true, secure: true, sameSite: 'strict', path: '/dashboard' });
		expect(options.expires.getTime()).toBe(NOW + HOUR);
	});

	it('holds an opaque id, never the token', () => {
		const token = 'eyJ.secret-payload.sig';
		const id = createSession('u', token, NOW + HOUR, NOW);
		expect(id).not.toContain('secret-payload');
		expect(id).toMatch(/^[A-Za-z0-9_-]{43}$/);
	});

	it('gives each session a different id', () => {
		expect(createSession('u', 't', NOW + HOUR, NOW)).not.toBe(createSession('u', 't', NOW + HOUR, NOW));
	});
});

describe('BearerToken', () => {
	it('never shows its value when printed, serialised, interpolated or inspected', () => {
		const t = new BearerToken('super-secret-token');
		const shown = [String(t), `${t}`, JSON.stringify({ t }), inspect(t), JSON.stringify(getSessionOf(t))];
		for (const text of shown) expect(text).not.toContain('super-secret-token');
		expect(t.reveal()).toBe('super-secret-token');
	});

	it('does not reach the console through a logged session', () => {
		const log = vi.spyOn(console, 'log').mockImplementation(() => undefined);
		const error = vi.spyOn(console, 'error').mockImplementation(() => undefined);
		const id = createSession('u', 'canary-token-value', NOW + HOUR, NOW);
		const session = getSession(id, NOW);
		console.log(session);
		console.error(new Error('failed', { cause: session }));
		const printed = [...log.mock.calls, ...error.mock.calls].map((c) => c.map((a) => inspect(a, { depth: 5 })).join(' ')).join('\n');
		expect(printed).not.toContain('canary-token-value');
	});
});

function getSessionOf(token: BearerToken) {
	return { username: 'u', token };
}

describe('request identity', () => {
	it('gives the caller token to everything the request awaits, and none outside it', async () => {
		const token = new BearerToken('t1');
		await runWithIdentity({ kind: 'caller', username: 'u', token, ended: new AbortController().signal }, async () => {
			await Promise.resolve();
			expect(callerToken()).toBe(token);
		});
		expect(callerToken()).toBeUndefined();
		await runWithIdentity({ kind: 'anonymous' }, async () => expect(callerToken()).toBeUndefined());
	});
});

describe('sameOrigin', () => {
	const url = new URL('https://dash.example.test/dashboard/signin');
	const req = (origin?: string) => new Request(url, { method: 'POST', headers: origin ? { origin } : {} });

	it('accepts the dashboard own origin only', () => {
		expect(sameOrigin(req('https://dash.example.test'), url)).toBe(true);
		expect(sameOrigin(req('https://evil.example.test'), url)).toBe(false);
		expect(sameOrigin(req('http://dash.example.test'), url)).toBe(false);
		expect(sameOrigin(req(), url)).toBe(false);
	});
});

describe('cleanToken', () => {
	it('removes space and a Bearer prefix', () => {
		expect(cleanToken(' bearer  abc \n')).toBe('abc');
		expect(cleanToken('abc')).toBe('abc');
	});
});

describe('session ending', () => {
	it('aborts the signal of a session that is signed out or expires, so its streams stop', () => {
		const id = createSession('u', 't', NOW + HOUR, NOW);
		const ended = getSession(id, NOW)?.ended;
		expect(ended?.aborted).toBe(false);
		destroySession(id);
		expect(ended?.aborted).toBe(true);

		const id2 = createSession('u', 't', NOW + HOUR, NOW);
		const ended2 = getSession(id2, NOW)?.ended;
		getSession(id2, NOW + 2 * HOUR);
		expect(ended2?.aborted).toBe(true);
	});

	it('lets one user make room among their own sessions, not sign others out', () => {
		const other = createSession('other', 't', NOW + HOUR, NOW);
		for (let i = 0; i < 40; i++) createSession('busy', 't', NOW + HOUR, NOW);
		expect(getSession(other, NOW)).toBeDefined();
		expect(sessionCount()).toBe(1 + 20);
	});
});

describe('the cookie on a plain-http localhost port-forward', () => {
	it('is not Secure there, and is Secure everywhere else', () => {
		expect(sessionCookieOptions('/dashboard', NOW, new URL('http://localhost:3000/')).secure).toBe(false);
		expect(sessionCookieOptions('/dashboard', NOW, new URL('https://dash.example.test/')).secure).toBe(true);
		expect(sessionCookieOptions('/dashboard', NOW, new URL('http://dash.example.test/')).secure).toBe(true);
		expect(sessionCookieOptions('/dashboard', NOW).secure).toBe(true);
	});
});
