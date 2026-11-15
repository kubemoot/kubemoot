// Sending a message to the bus from the dashboard, and what the page shows when it fails.
// A message appears on screen as soon as the person sends it; if the publish then fails,
// the message is marked "not sent" with the reason and a Retry, so it never looks sent.

import { writable } from 'svelte/store';
import { resolve } from '$app/paths';
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

// The unsent messages live in memory only, so the text never tells the person to reload the page.
const SIGNED_OUT = 'Your session has ended. Sign in again (in this tab or another), then press Retry.';

/** The most of the server's own error text shown to a person. */
const REASON_MAX_CHARS = 200;

/**
 * True when the answer is not from the dashboard server: a sign-in redirect, a 401, or a
 * login page (HTML served with 200, 401 or 403). An HTML error page on a 5xx is the server
 * failing, not a lost session.
 */
function isSignInResponse(res: Response): boolean {
	if (res.type === 'opaqueredirect' || res.status === 401) return true;
	const loginStatus = res.ok || res.status === 403;
	return loginStatus && (res.headers.get('content-type') ?? '').includes('text/html');
}

/** The server's own error text from a JSON body, when it gave one. */
async function serverReason(res: Response): Promise<string> {
	try {
		const body = await res.json();
		const text: unknown = body?.error;
		return typeof text === 'string' ? text.slice(0, REASON_MAX_CHARS) : '';
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

/** Posts a discussion message to the bus; the one publish both Discussions and Messages use. */
export function publishDiscussionMessage(subject: string, message: DiscussionMessage): Promise<void> {
	return publishToBus(resolve('/api/nats/publish'), subject, message);
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

/** The messages the bus has: those not marked unsent. */
export function deliveredMessages(messages: DiscussionMessage[], unsent: ReadonlyMap<string, Unsent>): DiscussionMessage[] {
	return messages.filter((m) => !unsent.has(m.messageId));
}

/** Whether the thread is paused, by its latest delivered pause or resume. */
export function isPaused(messages: DiscussionMessage[], unsent: ReadonlyMap<string, Unsent>): boolean {
	const delivered = deliveredMessages(messages, unsent);
	for (let i = delivered.length - 1; i >= 0; i--) {
		if (delivered[i].messageType === 'thread_pause') return true;
		if (delivered[i].messageType === 'thread_resume') return false;
	}
	return false;
}

const PERSON_MESSAGES: ReadonlySet<string> = new Set(['thread_start', 'reply', 'follow_up']);

/** Whether the crew owes an answer: a delivered question since the last answer or close. */
export function awaitingAnswer(messages: DiscussionMessage[], unsent: ReadonlyMap<string, Unsent>): boolean {
	let waiting = false;
	for (const m of deliveredMessages(messages, unsent)) {
		if (m.messageType === 'thread_start' || (m.agentName === 'human' && PERSON_MESSAGES.has(m.messageType))) waiting = true;
		else if (m.messageType === 'synthesis' || m.messageType === 'thread_close') waiting = false;
	}
	return waiting;
}

/**
 * Whether a thread is open: the store's status, unless a message of it never reached the bus.
 * Then the status was moved by that message, so it is read again from the delivered ones.
 */
export function threadIsOpen(
	thread: { status: string; messages: DiscussionMessage[] },
	unsent: ReadonlyMap<string, Unsent>
): boolean {
	if (thread.status !== 'open') return false;
	return thread.messages.some((m) => unsent.has(m.messageId)) ? awaitingAnswer(thread.messages, unsent) : true;
}

/** Whether the thread's opening message never reached the bus, so nothing can be added to it yet. */
export function firstMessageUnsent(messages: DiscussionMessage[], unsent: ReadonlyMap<string, Unsent>): boolean {
	return messages.some((m) => m.messageType === 'thread_start' && unsent.has(m.messageId));
}
