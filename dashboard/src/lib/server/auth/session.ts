import { createHash, randomBytes } from 'node:crypto';
import { BearerToken } from './bearer-token.js';

/** The name of the session cookie. It holds only an opaque random id, never a token. */
export const SESSION_COOKIE = 'kubemoot_session';

/** The most sessions held at once, and the most one user may hold; the oldest is dropped to make room. */
export const MAX_SESSIONS = 1000;
export const MAX_SESSIONS_PER_USER = 20;

export interface Session {
	username: string;
	token: BearerToken;
	/** Epoch milliseconds; never later than the token's own expiry. */
	expiresAt: number;
	/** Aborts when the session ends, so open streams made with its token stop. */
	ended: AbortSignal;
}

interface Entry {
	session: Session;
	abort: AbortController;
}

// The sessions live in this process's memory only: nothing is written to disk, and a restart
// signs everyone out. They are keyed by a hash of the cookie value, so the keys of a memory
// dump cannot be replayed as cookies.
const sessions = new Map<string, Entry>();

function forget(key: string): void {
	sessions.get(key)?.abort.abort();
	sessions.delete(key);
}

function keyOf(id: string): string {
	return createHash('sha256').update(id).digest('hex');
}

function dropExpired(now: number): void {
	for (const [key, entry] of sessions) {
		if (entry.session.expiresAt <= now) forget(key);
	}
}

/** The key of the oldest session of the user who holds the most, or of one user when given. */
function evictionKey(username?: string): string | undefined {
	const counts = new Map<string, number>();
	for (const entry of sessions.values()) counts.set(entry.session.username, (counts.get(entry.session.username) ?? 0) + 1);
	const biggest = username ?? [...counts.entries()].sort((a, b) => b[1] - a[1])[0]?.[0];
	for (const [key, entry] of sessions) if (entry.session.username === biggest) return key;
	return undefined;
}

/** Stores a session and returns the opaque cookie value that finds it. */
export function createSession(username: string, token: string, expiresAt: number, now = Date.now()): string {
	dropExpired(now);
	// One user's sign-ins make room among their own first, so they cannot sign everyone else out.
	const own = [...sessions.values()].filter((e) => e.session.username === username).length;
	if (own >= MAX_SESSIONS_PER_USER) forget(evictionKey(username) ?? '');
	while (sessions.size >= MAX_SESSIONS) {
		const key = evictionKey();
		if (key === undefined) break;
		forget(key);
	}
	const id = randomBytes(32).toString('base64url');
	const abort = new AbortController();
	sessions.set(keyOf(id), {
		session: { username, token: new BearerToken(token), expiresAt, ended: abort.signal },
		abort
	});
	return id;
}

/** The live session for a cookie value; an expired one is removed and not returned. */
export function getSession(id: string | undefined, now = Date.now()): Session | undefined {
	if (!id) return undefined;
	const key = keyOf(id);
	const entry = sessions.get(key);
	if (!entry) return undefined;
	if (entry.session.expiresAt <= now) {
		forget(key);
		return undefined;
	}
	return entry.session;
}

/** Ends a session: its token is forgotten. */
export function destroySession(id: string | undefined): void {
	if (id) forget(keyOf(id));
}

/** Test helper: forget every session. */
export function resetSessions(): void {
	for (const key of [...sessions.keys()]) forget(key);
}

/** Test helper: how many sessions are held. */
export function sessionCount(): number {
	return sessions.size;
}
