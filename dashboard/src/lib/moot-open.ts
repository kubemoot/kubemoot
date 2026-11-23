// How a link opens one thread on the Moot page: /moot?thread=<id>&subject=<subject>. The subject
// lets the page read a thread that is older than the loaded history, by its own subject.

import { parseDiscussSubject } from '#lib/crewScope.js';

export interface ThreadTarget {
	threadId: string;
	/** The NATS subject of the thread; without it only a thread in the loaded history can open. */
	subject?: string;
}

/** The query that opens a thread on the Moot page. */
export function mootQuery(target: ThreadTarget): string {
	const params = new URLSearchParams({ thread: target.threadId });
	if (target.subject) params.set('subject', target.subject);
	return `?${params.toString()}`;
}

/** The query that opens a conversation on the Conversations page; the thread lets the page read it when it is not loaded. */
export function conversationQuery(target: ThreadTarget & { conversationId: string }): string {
	const params = new URLSearchParams({ conversation: target.conversationId, thread: target.threadId });
	if (target.subject) params.set('subject', target.subject);
	return `?${params.toString()}`;
}

/** The thread an address asks for, or null. A subject that does not belong to the thread is dropped. */
export function requestedThread(search: string): ThreadTarget | null {
	const params = new URLSearchParams(search);
	const threadId = params.get('thread');
	if (!threadId) return null;
	const subject = params.get('subject') ?? '';
	return parseDiscussSubject(subject)?.threadId === threadId ? { threadId, subject } : { threadId };
}

/** A copy of the query with the open thread written in, or removed when nothing is open; other parameters stay. */
export function withThread(search: string, target: ThreadTarget | null): URLSearchParams {
	const next = new URLSearchParams(search);
	next.delete('thread');
	next.delete('subject');
	if (!target) return next;
	next.set('thread', target.threadId);
	if (target.subject) next.set('subject', target.subject);
	return next;
}
