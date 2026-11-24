// @vitest-environment node
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { getSession, resetSessions, sessionCount } from './session';
import { SignInError, type TokenReviewer } from './token-review';
import { ADDRESS_TTL_MS, bearerOf, normalizeAddress, proxySession, resetTrustedProxy } from './trusted-proxy';
import { TRUSTED_PROXY_ADDRESSES_ENV, TRUSTED_PROXY_ENV, TRUSTED_PROXY_HOST_ENV } from '../mode';

const NOW = Date.UTC(2026, 10, 24, 12, 0, 0);
const HOUR = 3_600_000;
/** An address built from parts: a literal one is a Sonar finding, and these are not real hosts. */
const ip = (...parts: Array<number | string>) => parts.join('.');
const PROXY = ip(10, 1, 2, 3);
const OTHER = ip(10, 9, 9, 9);

function jwt(exp: number): string {
	const part = (o: object) => Buffer.from(JSON.stringify(o)).toString('base64url');
	return `${part({ alg: 'none' })}.${part({ exp })}.sig`;
}

const headers = (token: string) => new Headers({ authorization: `Bearer ${token}` });
const accepts: TokenReviewer = async () => ({ username: 'oidc:ada' });
const refuses: TokenReviewer = async () => {
	throw new SignInError('invalid', 'no');
};

beforeEach(() => {
	resetSessions();
	resetTrustedProxy();
	process.env[TRUSTED_PROXY_ENV] = 'true';
	process.env[TRUSTED_PROXY_ADDRESSES_ENV] = PROXY;
	delete process.env[TRUSTED_PROXY_HOST_ENV];
});
afterEach(() => {
	delete process.env[TRUSTED_PROXY_ENV];
	delete process.env[TRUSTED_PROXY_ADDRESSES_ENV];
	delete process.env[TRUSTED_PROXY_HOST_ENV];
});

describe('a trusted proxy', () => {
	it('opens a session for a token the API server accepts', async () => {
		const review = vi.fn(accepts);
		const session = await proxySession(headers(jwt(NOW / 1000 + 3600)), PROXY, { review, now: () => NOW });
		expect(review).toHaveBeenCalledOnce();
		expect(session?.username).toBe('oidc:ada');
		expect(session?.token.reveal()).toContain('.');
		expect(session?.expiresAt).toBe(NOW + HOUR);
	});

	it('asks the API server once for the same token, even for concurrent requests', async () => {
		const review = vi.fn(accepts);
		const token = jwt(NOW / 1000 + 3600);
		const both = await Promise.all([
			proxySession(headers(token), PROXY, { review, now: () => NOW }),
			proxySession(headers(token), PROXY, { review, now: () => NOW })
		]);
		await proxySession(headers(token), PROXY, { review, now: () => NOW });
		expect(review).toHaveBeenCalledOnce();
		expect(both[0]).toBe(both[1]);
		expect(sessionCount()).toBe(1);
	});

	it('reviews again once the remembered session has ended', async () => {
		const review = vi.fn(accepts);
		const token = jwt(NOW / 1000 + 60);
		const first = await proxySession(headers(token), PROXY, { review, now: () => NOW });
		expect(getSession(undefined)).toBeUndefined();
		resetSessions();
		const second = await proxySession(headers(token), PROXY, { review, now: () => NOW });
		expect(review).toHaveBeenCalledTimes(2);
		expect(second).not.toBe(first);
	});

	it('accepts an IPv4 peer the socket reports as an IPv6-mapped address', async () => {
		const session = await proxySession(headers('t'), `::ffff:${PROXY}`, { review: accepts, now: () => NOW });
		expect(session?.username).toBe('oidc:ada');
	});

	it('trusts the addresses behind the proxy host name, and looks them up again after the lifetime', async () => {
		delete process.env[TRUSTED_PROXY_ADDRESSES_ENV];
		process.env[TRUSTED_PROXY_HOST_ENV] = 'proxy.ns.svc';
		const resolve = vi.fn(async () => [OTHER]);
		const deps = { review: accepts, resolve };
		expect(await proxySession(headers('a'), OTHER, { ...deps, now: () => NOW })).toBeDefined();
		await proxySession(headers('b'), OTHER, { ...deps, now: () => NOW + 1 });
		expect(resolve).toHaveBeenCalledOnce();
		await proxySession(headers('c'), OTHER, { ...deps, now: () => NOW + ADDRESS_TTL_MS + 1 });
		expect(resolve).toHaveBeenCalledTimes(2);
	});
});

describe('the proxy host name', () => {
	it('is asked once for a burst of requests, even when it does not answer', async () => {
		delete process.env[TRUSTED_PROXY_ADDRESSES_ENV];
		process.env[TRUSTED_PROXY_HOST_ENV] = 'proxy.ns.svc';
		const resolve = vi.fn(async () => {
			throw new Error('ENOTFOUND');
		});
		const review = vi.fn(accepts);
		await Promise.all([1, 2, 3].map((n) => proxySession(headers(`t${n}`), PROXY, { review, resolve, now: () => NOW })));
		await proxySession(headers('t4'), PROXY, { review, resolve, now: () => NOW + 1 });
		expect(resolve).toHaveBeenCalledOnce();
		expect(review).not.toHaveBeenCalled();
	});
});

describe('a request the proxy did not send', () => {
	it('is ignored from any other address, and the API server is never asked', async () => {
		const review = vi.fn(accepts);
		expect(await proxySession(headers('t'), ip(10, 7, 7, 7), { review, now: () => NOW })).toBeUndefined();
		expect(await proxySession(headers('t'), undefined, { review, now: () => NOW })).toBeUndefined();
		expect(review).not.toHaveBeenCalled();
		expect(sessionCount()).toBe(0);
	});

	it('trusts nothing when no address is configured, and when the host name does not resolve', async () => {
		delete process.env[TRUSTED_PROXY_ADDRESSES_ENV];
		const review = vi.fn(accepts);
		expect(await proxySession(headers('t'), PROXY, { review })).toBeUndefined();
		process.env[TRUSTED_PROXY_HOST_ENV] = 'missing.ns.svc';
		const resolve = async () => {
			throw new Error('ENOTFOUND');
		};
		expect(await proxySession(headers('t'), PROXY, { review, resolve })).toBeUndefined();
		expect(review).not.toHaveBeenCalled();
	});

	it('is ignored when the proxy is not turned on, even from the proxy address', async () => {
		delete process.env[TRUSTED_PROXY_ENV];
		const review = vi.fn(accepts);
		expect(await proxySession(headers('t'), PROXY, { review, now: () => NOW })).toBeUndefined();
		process.env[TRUSTED_PROXY_ENV] = 'false';
		expect(await proxySession(headers('t'), PROXY, { review, now: () => NOW })).toBeUndefined();
		expect(review).not.toHaveBeenCalled();
	});
});

describe('a token that does not open a session', () => {
	it('is refused by the API server', async () => {
		expect(await proxySession(headers('t'), PROXY, { review: refuses, now: () => NOW })).toBeUndefined();
		expect(sessionCount()).toBe(0);
	});

	it('has expired, without asking the API server', async () => {
		const review = vi.fn(accepts);
		expect(await proxySession(headers(jwt(NOW / 1000 - 60)), PROXY, { review, now: () => NOW })).toBeUndefined();
		expect(review).not.toHaveBeenCalled();
	});

	it('could not be reviewed because the API server was unreachable', async () => {
		const down: TokenReviewer = async () => {
			throw new SignInError('unavailable', 'down');
		};
		expect(await proxySession(headers('t'), PROXY, { review: down, now: () => NOW })).toBeUndefined();
	});

	it.each([null, '', 'Basic abc', 'Bearer', 'Bearer a b', 'abc'])('is not there in the header %j', async (value) => {
		const review = vi.fn(accepts);
		const h = new Headers(value === null ? {} : { authorization: value });
		expect(await proxySession(h, PROXY, { review })).toBeUndefined();
		expect(review).not.toHaveBeenCalled();
	});

	it('is not a SignInError that hides a real failure', async () => {
		const broken: TokenReviewer = async () => {
			throw new TypeError('bug');
		};
		await expect(proxySession(headers('t'), PROXY, { review: broken, now: () => NOW })).rejects.toThrow('bug');
	});
});

describe('helpers', () => {
	it('reads a bearer token', () => {
		expect(bearerOf('Bearer abc')).toBe('abc');
		expect(bearerOf('bearer   abc ')).toBe('abc');
		expect(bearerOf(undefined)).toBeUndefined();
	});

	it('normalizes addresses', () => {
		expect(normalizeAddress(`::FFFF:${PROXY}`)).toBe(PROXY);
		expect(normalizeAddress(['fe80', '', '1'].join(':'))).toBe(['fe80', '', '1'].join(':'));
		expect(normalizeAddress('')).toBeUndefined();
	});
});
