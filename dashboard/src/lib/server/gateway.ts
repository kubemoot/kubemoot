// The dashboard asks a crew through the crew's discussion gateway, as the signed-in person: the
// same HTTP API kmctl uses, with the person's own bearer token. The gateway checks the token
// (a TokenReview) and the person's right to use the crew (a SubjectAccessReview), and records
// the person as the owner of the conversation; the dashboard decides none of that itself.

import { callerEnded, callerToken } from './auth/identity.js';
import { jsonError } from './json-error.js';
import { internalFetch } from './internal-tls.js';
import { gatewayUrlTemplate } from './mode.js';
import { getCrew } from './k8s/kubemoot-crds.js';
import { guardNamespace } from './scope.js';
import { isDenied, kubeFailure, kubeStatus } from './k8s/errors.js';

import { REASON_HEADER, validReason } from '#lib/access-reason.js';
import { isConversationId } from '#lib/conversation-export.js';

const DNS_LABEL = /^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$/;
const MAX_MESSAGE_LENGTH = 20_000;

function trimSlashes(text: string): string {
	let end = text.length;
	while (end > 0 && text[end - 1] === '/') end--;
	return text.slice(0, end);
}

export function validLabel(value: string): boolean {
	return DNS_LABEL.test(value);
}

export function validConversationId(value: string): boolean {
	return isConversationId(value);
}

/** The gateway's address for a crew: the template from the environment, else the in-cluster Service over https. */
export function gatewayUrl(namespace: string, crew: string, path: string, template = gatewayUrlTemplate()): string {
	if (!validLabel(namespace) || !validLabel(crew)) throw new Error('invalid namespace or crew name');
	const fallback = 'https://{crew}-discussion.{namespace}.svc.cluster.local';
	const base = trimSlashes((template || fallback).replaceAll('{crew}', crew).replaceAll('{namespace}', namespace));
	return `${base}${path}`;
}

/** An answer the dashboard gives for a refusal or a failure, in words a person can act on. */
export const refusal = jsonError;

const MESSAGES: Record<number, string> = {
	401: 'The crew did not accept your token. Sign out and sign in again.',
	403: 'You do not have permission to use this crew. Ask the cluster administrator for access.',
	404: 'Not found, or not yours: you can only see conversations you started.',
	503: 'The crew could not take the request right now. Try again shortly.'
};

/** The text for a gateway answer that was not a success: the gateway's own reason with a plain explanation. */
export async function describeRefusal(upstream: Response): Promise<string> {
	const plain = MESSAGES[upstream.status] ?? `The crew's gateway answered ${upstream.status}.`;
	const reason = await upstream
		.json()
		.then((body: unknown) => {
			const text = (body as { error?: unknown } | null)?.error;
			return typeof text === 'string' ? text.slice(0, 200) : '';
		})
		.catch(() => '');
	return reason && !plain.includes(reason) ? `${plain} (${reason})` : plain;
}

/** A failed answer from the gateway as the dashboard's own response, with the same status. */
export async function relayRefusal(upstream: Response): Promise<Response> {
	return refusal(upstream.status, await describeRefusal(upstream));
}

/**
 * Calls a crew's gateway with the signed-in caller's token. Redirects are not followed, so the
 * token is never sent anywhere but the gateway. Returns null when nobody is signed in.
 */
export async function callGateway(
	namespace: string,
	crew: string,
	path: string,
	init: { method?: string; body?: unknown; signal?: AbortSignal; headers?: Record<string, string> } = {}
): Promise<Response | null> {
	const token = callerToken();
	if (!token) return null;
	const headers: Record<string, string> = { ...init.headers, Authorization: `Bearer ${token.reveal()}` };
	if (init.body !== undefined) headers['Content-Type'] = 'application/json';
	// The call also ends with the person's session, so a stream cannot outlive their sign-out.
	const ended = callerEnded();
	const signal = init.signal && ended ? AbortSignal.any([init.signal, ended]) : (init.signal ?? ended);
	return internalFetch(gatewayUrl(namespace, crew, path), {
		method: init.method ?? 'GET',
		headers,
		body: init.body === undefined ? undefined : JSON.stringify(init.body),
		redirect: 'manual',
		signal
	});
}

export function notSignedIn(): Response {
	return refusal(401, 'Sign in to ask a crew.', { signIn: true });
}

/**
 * The caller must be allowed to get the Crew, checked with their own token (the same right the
 * gateway checks on a crew that enforces access). Returns a response to send when they are not.
 */
export async function requireCrewAccess(namespace: string, crew: string): Promise<Response | null> {
	try {
		await getCrew(namespace, crew);
		return null;
	} catch (err) {
		if (isDenied(err)) return kubeFailure(err, 'You do not have permission to use this crew.');
		if (kubeStatus(err) === 404) return refusal(404, 'That crew was not found.');
		return refusal(503, 'The cluster could not be asked just now. Try again shortly.');
	}
}

/** What an ask may carry to the gateway: the question, the conversation it continues, and the asker's clock. */
export interface AskBody {
	message: string;
	conversationId?: string;
	timeZone?: string;
	localDateTime?: string;
}

/** The checked body of an ask, or the reason it is refused. Nothing else a client sends reaches the gateway. */
export function parseAsk(raw: unknown): { ok: true; body: AskBody } | { ok: false; error: string } {
	const input = (raw ?? {}) as Record<string, unknown>;
	const text = (key: string): string => (typeof input[key] === 'string' ? input[key] : '');
	const message = text('message');
	if (message.trim() === '') return { ok: false, error: 'Write a question first.' };
	if (message.length > MAX_MESSAGE_LENGTH) return { ok: false, error: 'The question is too long.' };
	const conversationId = text('conversationId');
	if (conversationId !== '' && !validConversationId(conversationId)) {
		return { ok: false, error: 'The conversation id is not valid.' };
	}
	const body: AskBody = { message };
	if (conversationId) body.conversationId = conversationId;
	if (text('timeZone')) body.timeZone = text('timeZone').slice(0, 64);
	if (text('localDateTime')) body.localDateTime = text('localDateTime').slice(0, 32);
	return { ok: true, body };
}

/**
 * The checks every gateway route makes before it calls the gateway: the names are valid, the
 * namespace is one this dashboard shows, and the caller may get the Crew. Returns the response
 * to send when one fails, or null to go on.
 */
export async function gateRequest(namespace: string, crew: string, conversationId?: string): Promise<Response | null> {
	if (!validLabel(namespace) || !validLabel(crew)) return refusal(400, 'Invalid crew.');
	if (conversationId !== undefined && !validConversationId(conversationId)) return refusal(400, 'Invalid conversation.');
	return (await guardNamespace(namespace)) ?? (await requireCrewAccess(namespace, crew));
}

/**
 * Calls a crew's gateway as the signed-in person and turns the answer into a response: a
 * refusal comes back with its own status and plain words, a gateway that cannot be reached is a
 * 502, and a success goes to `onOk`.
 */
export async function proxyGateway(
	namespace: string,
	crew: string,
	path: string,
	init: Parameters<typeof callGateway>[3],
	onOk: (upstream: Response) => Response | Promise<Response>,
	/** A status that is not a success but whose JSON body the caller wants as it is, such as a report. */
	relayBodyOf: (status: number) => boolean = () => false
): Promise<Response> {
	try {
		const upstream = await callGateway(namespace, crew, path, init);
		if (!upstream) return notSignedIn();
		if (!upstream.ok && !relayBodyOf(upstream.status)) return await relayRefusal(upstream);
		return await onOk(upstream);
	} catch {
		return refusal(502, "The crew's gateway could not be reached.");
	}
}


/**
 * The reason header to pass on: none when the request carries none, the trimmed reason when it
 * is 1 to 200 characters, and an error to answer when it is not.
 */
export function accessReasonHeaders(request: Request): { ok: true; headers: Record<string, string> } | { ok: false; error: string } {
	const raw = request.headers.get(REASON_HEADER);
	if (raw === null) return { ok: true, headers: {} };
	const reason = validReason(raw);
	if (reason === undefined) return { ok: false, error: 'Give a reason of 1 to 200 characters, with no line breaks.' };
	return { ok: true, headers: { [REASON_HEADER]: reason } };
}

