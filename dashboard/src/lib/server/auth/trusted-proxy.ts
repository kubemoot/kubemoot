import { createHash } from 'node:crypto';
import { lookup } from 'node:dns/promises';
import { getSession, type Session } from './session.js';
import { signIn } from './sign-in.js';
import { SignInError, reviewToken, type TokenReviewer } from './token-review.js';
import { trustedProxyAddresses, trustedProxyEnabled, trustedProxyHost } from '../mode.js';

/** Resolves a DNS name to its addresses; tests substitute their own. */
export type Resolver = (host: string) => Promise<string[]>;

export interface ProxyDeps {
	review?: TokenReviewer;
	resolve?: Resolver;
	now?: () => number;
}

/** How long the proxy's resolved addresses are reused; a pod that restarts is trusted again within this. */
export const ADDRESS_TTL_MS = 10_000;
const MAX_REMEMBERED = 1000;

const resolveAll: Resolver = async (host) => (await lookup(host, { all: true })).map((a) => a.address);

/** An address in the form the socket reports it, with an IPv4-mapped IPv6 address shown as the IPv4 one. */
export function normalizeAddress(address: string | undefined): string | undefined {
	const trimmed = address?.trim().toLowerCase();
	if (!trimmed) return undefined;
	return trimmed.startsWith('::ffff:') && trimmed.includes('.') ? trimmed.slice(7) : trimmed;
}

interface Resolved {
	host: string;
	at: number;
	addresses: string[];
}

let resolved: Resolved | undefined;
let resolving: Promise<string[]> | undefined;

async function lookupAddresses(host: string, resolve: Resolver, now: number): Promise<string[]> {
	let addresses: string[] = [];
	try {
		addresses = (await resolve(host)).map((a) => normalizeAddress(a)).filter((a): a is string => a !== undefined);
	} catch {
		// A failed lookup is remembered like a good one, so a name that does not answer is asked once per lifetime.
	}
	resolved = { host, at: now, addresses };
	return addresses;
}

/** The addresses behind the proxy's DNS name: reused for ADDRESS_TTL_MS, failures included, and one lookup at a time. */
async function hostAddresses(host: string, resolve: Resolver, now: number): Promise<string[]> {
	if (!host) return [];
	if (resolved?.host === host && now - resolved.at < ADDRESS_TTL_MS) return resolved.addresses;
	resolving ??= lookupAddresses(host, resolve, now).finally(() => {
		resolving = undefined;
	});
	return resolving;
}

/** True when the socket peer is one of the sign-in proxy's addresses. Fails closed: no address to trust, no trust. */
export async function isTrustedSource(peer: string | undefined, resolve: Resolver, now: number): Promise<boolean> {
	const who = normalizeAddress(peer);
	if (!who) return false;
	const direct = trustedProxyAddresses().map((a) => normalizeAddress(a));
	if (direct.includes(who)) return true;
	return (await hostAddresses(trustedProxyHost(), resolve, now)).includes(who);
}

/** The token of an "Authorization: Bearer <token>" header, or undefined for any other value. */
export function bearerOf(header: string | null | undefined): string | undefined {
	const match = /^bearer +(\S+)$/i.exec((header ?? '').trim());
	return match?.[1];
}

// A proxy sends the same token on every request. The session it opened is remembered by a hash
// of the token (never the token), so the API server is asked once, not once per request.
const remembered = new Map<string, string>();
const pending = new Map<string, Promise<string | undefined>>();

function keyOf(token: string): string {
	return createHash('sha256').update(token).digest('hex');
}

function liveSession(key: string, now: number): Session | undefined {
	const id = remembered.get(key);
	const session = id === undefined ? undefined : getSession(id, now);
	if (id !== undefined && !session) remembered.delete(key);
	return session;
}

function forgetStale(now: number): void {
	if (remembered.size < MAX_REMEMBERED) return;
	for (const key of remembered.keys()) liveSession(key, now);
}

async function openSession(token: string, key: string, review: TokenReviewer, now: () => number): Promise<string | undefined> {
	try {
		const { sessionId } = await signIn(token, review, now);
		forgetStale(now());
		remembered.set(key, sessionId);
		return sessionId;
	} catch (error) {
		// A refused, expired or unreviewable token opens no session; the request is then as signed out as before.
		if (error instanceof SignInError) return undefined;
		throw error;
	}
}

async function sessionForToken(token: string, review: TokenReviewer, clock: () => number): Promise<Session | undefined> {
	const now = clock();
	const key = keyOf(token);
	const known = liveSession(key, now);
	if (known) return known;
	let attempt = pending.get(key);
	if (!attempt) {
		attempt = openSession(token, key, review, clock).finally(() => pending.delete(key));
		pending.set(key, attempt);
	}
	const id = await attempt;
	return getSession(id, now);
}

/**
 * The session a request from the sign-in proxy carries, or undefined. Only when the proxy is
 * trusted (auth.trustedProxy.enabled), the request comes from one of its addresses, and its
 * bearer token is one the API server accepts (the same TokenReview as a pasted token, so there
 * is no token parsing of our own beyond the expiry the session already reads). In every other
 * case the Authorization header is ignored.
 */
export async function proxySession(headers: Headers, peer: string | undefined, deps: ProxyDeps = {}): Promise<Session | undefined> {
	if (!trustedProxyEnabled()) return undefined;
	const token = bearerOf(headers.get('authorization'));
	if (!token) return undefined;
	const clock = deps.now ?? Date.now;
	if (!(await isTrustedSource(peer, deps.resolve ?? resolveAll, clock()))) return undefined;
	return sessionForToken(token, deps.review ?? reviewToken, clock);
}

/** Test helper: forget the remembered sessions and resolved addresses. */
export function resetTrustedProxy(): void {
	remembered.clear();
	pending.clear();
	resolved = undefined;
	resolving = undefined;
}
