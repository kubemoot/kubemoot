/**
 * Whether a state-changing request came from the dashboard's own pages: its Origin header
 * names the origin the request was made to. A page on another site, or a request that
 * names no origin, is refused, so a visitor's browser cannot be made to ask a crew or sign
 * out on a stranger's behalf. (The session cookie is SameSite=Strict as well.)
 */
export function sameOrigin(request: Request, url: URL): boolean {
	const origin = request.headers.get('origin');
	return origin !== null && origin === url.origin;
}
