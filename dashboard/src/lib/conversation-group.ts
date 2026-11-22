// A conversation is the exchange a person has with a crew, and the crew answers each question
// on a thread of its own: a follow-up opens a new thread under the same conversation id. The
// Conversations page lists one entry per conversation, so its threads are merged here into one
// thread-shaped value that the rest of the page (filters, titles, chat turns, state) reads as
// it reads a thread.

import type { Thread } from '#lib/stores/discussions.js';
import { threadConversationId } from '#lib/conversation-export.js';

/** A conversation as one thread: the first thread's identity, every thread's messages in time order. */
export interface Conversation extends Thread {
	/** The ids of the threads merged in, oldest first. */
	threadIds: string[];
	/** The newest thread, which the Moot link opens. */
	latestThreadId: string;
}

function startOf(thread: Thread): number {
	const t = Date.parse(thread.startedAt);
	return Number.isNaN(t) ? 0 : t;
}

function messageTime(m: { timestamp?: string }): number {
	const t = Date.parse(m.timestamp ?? '');
	return Number.isNaN(t) ? Number.MAX_SAFE_INTEGER : t;
}

/** The status of the merged conversation: working while any thread is, else the newest thread's. */
function mergedStatus(threads: Thread[]): Thread['status'] {
	if (threads.some((t) => t.status === 'open')) return 'open';
	return threads.at(-1)?.status ?? 'closed';
}

function merge(threads: Thread[]): Conversation {
	const ordered = [...threads].sort((a, b) => startOf(a) - startOf(b));
	const first = ordered[0];
	if (ordered.length === 1) return { ...first, threadIds: [first.threadId], latestThreadId: first.threadId };
	const messages = ordered.flatMap((t) => t.messages).sort((a, b) => messageTime(a) - messageTime(b));
	return {
		...first,
		messages,
		status: mergedStatus(ordered),
		threadIds: ordered.map((t) => t.threadId),
		latestThreadId: ordered.at(-1)?.threadId ?? first.threadId
	};
}

/**
 * The conversations of some threads, in the order their newest thread appears in the input
 * (newest first, as the store gives them). A thread with no conversation id is its own conversation.
 */
export function groupConversations(threads: readonly Thread[]): Conversation[] {
	const groups = new Map<string, Thread[]>();
	for (const thread of threads) {
		const key = threadConversationId(thread) ?? `thread:${thread.threadId}`;
		const group = groups.get(key);
		if (group) group.push(thread);
		else groups.set(key, [thread]);
	}
	return [...groups.values()].map(merge);
}
