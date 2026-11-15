// Sending a message to the bus from the dashboard, and what the page shows when it fails.
// A message appears on screen as soon as the person sends it; if the publish then fails,
// the message is marked "not sent" with the reason and a Retry, so it never looks sent.

import { writable } from 'svelte/store';
import type { DiscussionMessage } from '#lib/types/kubemoot.js';

/** Why a publish failed, in the terms a person can act on. */
export type PublishFailureKind = 'signed-out' | 'unreachable' | 'refused';

export class PublishFailure extends Error {
	constructor(
		readonly kind: PublishFailureKind,
		message: string
	) {
		super(message);
		this.name = 'PublishFailure';
	}
}

const SIGNED_OUT = 'Your session has ended. Reload the page and sign in again.';

/** True when the answer is not from the dashboard server: a sign-in redirect or a login page. */
function isSignInResponse(res: Response): boolean {
	if (res.type === 'opaqueredirect' || res.redirected) return true;
	if (res.status === 401) return true;
	return (res.headers.get('content-type') ?? '').includes('text/html');
}

/** The server's own error text from a JSON body, when it gave one. */
async function serverReason(res: Response): Promise<string> {
	try {
		const body = await res.json();
		return typeof body?.error === 'string' && body.error ? body.error : '';
	} catch {
		return '';
	}
}

/** Posts one message to the bus through the dashboard server; throws a PublishFailure when it did not go. */
export async function publishToBus(url: string, subject: string, data: object): Promise<void> {
	let res: Response;
	try {
		// A redirect is not followed: an expired sign-in redirects to a login page on another origin.
		res = await fetch(url, {
			method: 'POST',
			redirect: 'manual',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify({ subject, data: JSON.stringify(data) })
		});
	} catch {
		throw new PublishFailure('unreachable', 'The dashboard server could not be reached. Check the connection and retry.');
	}
	if (isSignInResponse(res)) throw new PublishFailure('signed-out', SIGNED_OUT);
	if (res.ok) return;
	const reason = (await serverReason(res)) || `HTTP ${res.status}`;
	throw new PublishFailure('refused', `The server refused the message: ${reason}.`);
}

/** What the page tells the person about a failed send. */
export function failureText(error: unknown): string {
	if (error instanceof PublishFailure) return error.message;
	return error instanceof Error && error.message ? `Sending failed: ${error.message}` : 'Sending failed.';
}

export interface Unsent {
	subject: string;
	message: DiscussionMessage;
	reason: string;
}

/** Messages shown on screen that did not reach the bus, by message id. */
export const unsentMessages = writable<Map<string, Unsent>>(new Map());

function setUnsent(update: (map: Map<string, Unsent>) => void) {
	unsentMessages.update((current) => {
		const next = new Map(current);
		update(next);
		return next;
	});
}

/**
 * Sends a message; true when it went. On failure the message is recorded as unsent with
 * the reason and false is returned, so the caller never has to treat the failure as sent.
 */
export async function sendTracked(
	publish: (subject: string, message: DiscussionMessage) => Promise<void>,
	subject: string,
	message: DiscussionMessage
): Promise<boolean> {
	try {
		await publish(subject, message);
		setUnsent((map) => map.delete(message.messageId));
		return true;
	} catch (e) {
		const reason = failureText(e);
		setUnsent((map) => map.set(message.messageId, { subject, message, reason }));
		return false;
	}
}

/** Sends an unsent message again with the same id, so a reader that already has it drops the copy. */
export async function retryUnsent(
	publish: (subject: string, message: DiscussionMessage) => Promise<void>,
	messageId: string,
	unsent: ReadonlyMap<string, Unsent>
): Promise<boolean> {
	const entry = unsent.get(messageId);
	if (!entry) return false;
	return sendTracked(publish, entry.subject, entry.message);
}
