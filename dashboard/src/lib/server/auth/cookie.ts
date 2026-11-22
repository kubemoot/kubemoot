/** Safari refuses a Secure cookie from http://localhost, which a kubectl port-forward uses. */
function isPlainLocalhost(url: URL | undefined): boolean {
	return url?.protocol === 'http:' && (url.hostname === 'localhost' || url.hostname === '127.0.0.1');
}

/**
 * The session cookie's attributes: unreadable by page scripts (HttpOnly), sent only over
 * HTTPS (Secure, except for a plain-http localhost port-forward), never sent on a request a
 * cross-site page triggers (SameSite=Strict), and scoped to the dashboard's own path. It
 * lasts as long as the session, which never outlasts the token.
 */
export function sessionCookieOptions(base: string, expiresAt: number, url?: URL) {
	return {
		path: base || '/',
		httpOnly: true,
		secure: !isPlainLocalhost(url),
		sameSite: 'strict' as const,
		expires: new Date(expiresAt)
	};
}
