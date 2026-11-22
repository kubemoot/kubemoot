import { jsonError } from '../json-error.js';

/** Kubernetes API errors as the answer a page can show. */

interface MaybeStatus {
	code?: number;
	statusCode?: number;
	response?: { statusCode?: number };
}

/** The HTTP status the API server answered with, when the error carries one. */
export function kubeStatus(err: unknown): number | undefined {
	const e = err as MaybeStatus | null;
	return e?.code ?? e?.statusCode ?? e?.response?.statusCode;
}

export const FORBIDDEN_MESSAGE =
	'You do not have permission to see this. Your Kubernetes account is not allowed to read it; ask the cluster administrator for access.';
export const SIGNED_OUT_MESSAGE = 'The cluster no longer accepts your sign-in. Sign in again.';

/** True when the API server refused the caller: not allowed (403) or not accepted (401). */
export function isDenied(err: unknown): boolean {
	const status = kubeStatus(err);
	return status === 401 || status === 403;
}

/** The text a person reads for a denied call. */
export function deniedMessage(err: unknown): string {
	return kubeStatus(err) === 401 ? SIGNED_OUT_MESSAGE : FORBIDDEN_MESSAGE;
}

/** A read that failed, as a reading: the plain reason it is missing, and whether the account was refused. */
export type FailedReading = { available: false; reason: string; denied?: boolean };

/**
 * Why a Kubernetes read failed, as a line a person reads. `what` is the thing read, in lower case
 * ("the pod list"). A refusal says the account may not read it (401: sign in again); a 404 says
 * `notFound`, else that `what` is not served; anything else says it could not be read. A failure is
 * never turned into an empty or zero reading.
 */
export function failedReading(err: unknown, what: string, notFound = `${what} is not served by this cluster`): FailedReading {
	const status = kubeStatus(err);
	if (status === 401) return { available: false, reason: SIGNED_OUT_MESSAGE, denied: true };
	if (status === 403) return { available: false, reason: `your account may not read ${what}`, denied: true };
	if (status === 404) return { available: false, reason: notFound };
	return { available: false, reason: `${what} could not be read` };
}

/** The reading of `read`, or the reason it failed. */
export async function readingOf<T>(what: string, read: () => Promise<T>, notFound?: string): Promise<{ available: true; data: T } | FailedReading> {
	try {
		return { available: true, data: await read() };
	} catch (e) {
		return failedReading(e, what, notFound);
	}
}

/**
 * The response for a failed Kubernetes read. A call the caller's RBAC refuses is a clear
 * 403 the view shows as "you do not have permission", never a server error; a token the
 * API server stopped accepting is a 401 that asks for a new sign-in. Anything else is a 500
 * with the error's text.
 */
export function kubeFailure(err: unknown, fallback: string, extra: Record<string, unknown> = {}): Response {
	if (isDenied(err)) {
		const status = kubeStatus(err) === 401 ? 401 : 403;
		return jsonError(status, deniedMessage(err), { ...extra, denied: true });
	}
	const message = err instanceof Error ? err.message : fallback;
	return jsonError(500, message, extra);
}
