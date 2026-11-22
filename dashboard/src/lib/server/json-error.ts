/** The one shape of an error answer: `{ error, ...extra }` with the status, and any headers. */
export function jsonError(
	status: number,
	error: string,
	extra: Record<string, unknown> = {},
	headers?: HeadersInit
): Response {
	return Response.json({ ...extra, error }, { status, headers });
}
