/**
 * The dashboard's one cross-site request check. A request that could change something must
 * come from the dashboard's own pages: its Origin header names the origin the request was made
 * to. A page on another site, or a request that names no origin, is refused, so a visitor's
 * browser cannot be made to ask a crew or sign out on a stranger's behalf. (The session cookie
 * is SameSite=Strict as well.) SvelteKit's own form-only check is turned off in the kit
 * configuration so that this one decides, for every method and content type.
 */

/** What the server knows about how a request reached it. */
export interface OriginContext {
	/** The request URL as the server sees it; adapter-node builds it from ORIGIN, or from the forwarded headers, or from Host. */
	url: URL;
	/** The configured ORIGIN (the chart's auth.origin); when set it is the origin. */
	configuredOrigin?: string;
	/** The name of the header a trusted proxy sets to the client's scheme (the chart's auth.trustForwardedHeaders), when trusted. */
	protocolHeader?: string;
	/** The request headers, to read the forwarded scheme from. */
	headers: Headers;
	/** True when the connection to this server is TLS, false when it is plain HTTP, undefined when the server cannot tell. */
	encrypted?: boolean;
}

const SCHEMES = new Set(['http', 'https']);

/** The scheme a trusted proxy says the client used, or undefined when it sent none or something that is not a scheme. */
function forwardedScheme(ctx: OriginContext): string | undefined {
	const raw = ctx.protocolHeader ? ctx.headers.get(ctx.protocolHeader) : null;
	const first = raw?.split(',')[0]?.trim().toLowerCase();
	return first && SCHEMES.has(first) ? first : undefined;
}

/** The scheme of the connection itself: TLS on the socket is https, plain HTTP is http. */
function connectionScheme(ctx: OriginContext): string {
	if (ctx.encrypted === undefined) return ctx.url.protocol.slice(0, -1);
	return ctx.encrypted ? 'https' : 'http';
}

/**
 * The origin a request must name in its Origin header. A configured ORIGIN wins. Without one it
 * is the scheme the client used with the host the server was asked for: the scheme a trusted
 * proxy forwards, else the connection's own (a kubectl port-forward is plain HTTP), and the
 * host from the request URL, which already follows Host or the forwarded host.
 */
export function expectedOrigin(ctx: OriginContext): string {
	if (ctx.configuredOrigin) return ctx.url.origin;
	return `${forwardedScheme(ctx) ?? connectionScheme(ctx)}://${ctx.url.host}`;
}

/** Whether the request names the expected origin as its Origin. */
export function sameOrigin(request: Request, expected: string): boolean {
	const origin = request.headers.get('origin');
	return origin !== null && origin === expected;
}
