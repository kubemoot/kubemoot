import { createSession } from './session.js';
import { SignInError, reviewToken, type TokenReviewer } from './token-review.js';
import { sessionMaxMs } from '../mode.js';

/** The longest token accepted; real tokens are a few kilobytes at most. */
export const MAX_TOKEN_LENGTH = 16_384;

/** What a successful sign-in gives the caller: the cookie value and who they are. */
export interface SignedIn {
	sessionId: string;
	username: string;
	expiresAt: number;
}

/** A pasted token as the bare token: surrounding space and a leading "Bearer " removed. */
export function cleanToken(raw: string): string {
	return raw.trim().replace(/^bearer\s+/i, '');
}

/** The "exp" of a JWT in epoch milliseconds, or undefined when the token is not a JWT or has none. */
export function tokenExpiry(token: string): number | undefined {
	const payload = token.split('.')[1];
	if (token.split('.').length !== 3 || !payload) return undefined;
	try {
		const claims: unknown = JSON.parse(Buffer.from(payload, 'base64url').toString('utf8'));
		const exp = (claims as { exp?: unknown } | null)?.exp;
		return typeof exp === 'number' && Number.isFinite(exp) ? exp * 1000 : undefined;
	} catch {
		return undefined;
	}
}

/** The session's end: the token's own expiry when it has one, and never later than the configured maximum. */
export function sessionExpiry(token: string, now: number, maxMs: number): number {
	const own = tokenExpiry(token);
	return own === undefined ? now + maxMs : Math.min(own, now + maxMs);
}

/** True when the text holds a space, a line break, or any other character that cannot be in a token. */
function hasSpaceOrControl(text: string): boolean {
	for (const char of text) {
		const code = char.codePointAt(0) ?? 0;
		if (code <= 0x20 || code === 0x7f) return true;
	}
	return false;
}

function checkShape(token: string): void {
	if (token === '') throw new SignInError('empty', 'Paste a token to sign in.');
	if (token.length > MAX_TOKEN_LENGTH || hasSpaceOrControl(token)) {
		throw new SignInError('malformed', 'This is not a bearer token: it holds spaces, line breaks or is too long.');
	}
}

/**
 * Signs a person in with a pasted Kubernetes bearer token. The API server checks the token
 * (a TokenReview); only a token it accepts starts a session, which ends no later than the
 * token does. Throws a SignInError a person can read.
 */
export async function signIn(
	rawToken: string,
	review: TokenReviewer = reviewToken,
	now: () => number = Date.now
): Promise<SignedIn> {
	const token = cleanToken(rawToken);
	checkShape(token);
	const start = now();
	const expiresAt = sessionExpiry(token, start, sessionMaxMs());
	if (expiresAt <= start) throw new SignInError('expired', 'This token has expired. Create a new one.');
	const { username } = await review(token);
	return { sessionId: createSession(username, token, expiresAt, start), username, expiresAt };
}
