// A conversation is the exchange a person has with a crew, and the crew answers each question
// on a thread of its own: a follow-up opens a new thread under the same conversation id. The
// Conversations page lists one entry per conversation, so its threads are merged here into one
// thread-shaped value that the rest of the page (filters, titles, chat turns, state) reads as
// it reads a thread.

import { byLatestActivity, type Thread } from '#lib/stores/discussions.js';
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
 * The conversations of some threads: one per conversation id (a thread with no id is its own),
 * newest activity first, and equal times in one fixed order, so a reload lists them the same way.
 */
export function groupConversations(threads: readonly Thread[]): Conversation[] {
	const groups = new Map<string, Thread[]>();
	const seen = new Set<string>();
	for (const thread of threads) {
		if (seen.has(thread.threadId)) continue;
		seen.add(thread.threadId);
		const key = threadConversationId(thread) ?? `thread:${thread.threadId}`;
		const group = groups.get(key);
		if (group) group.push(thread);
		else groups.set(key, [thread]);
	}
	return [...groups.values()].map(merge).sort(byLatestActivity);
}

/** A question the crew has taken, before the bus shows its thread. */
export interface PendingAsk {
	conversationId: string;
	question: string;
	namespace: string;
	crew: string;
	/** When the crew took it (the gateway's time when it gave one, else the browser's). */
	askedAt: string;
	/** The crew's answer so far, from the gateway's own stream. */
	answer?: string;
	state?: 'working' | 'done' | 'error';
}

function pendingThread(p: PendingAsk): Thread {
	const threadId = `pending:${p.conversationId}`;
	const base = { threadId, agentName: 'human', channel: 'general', timestamp: p.askedAt };
	const start = { ...base, messageId: `${threadId}:start`, messageType: 'thread_start', content: p.question, metadata: { conversationId: p.conversationId, crew: p.crew, userQuery: p.question } };
	const answer = p.answer ? [{ ...base, messageId: `${threadId}:answer`, agentName: p.crew, messageType: 'synthesis', content: p.answer }] : [];
	return {
		threadId,
		channel: 'general',
		userQuery: p.question,
		startedBy: 'human',
		startedAt: p.askedAt,
		messages: [start, ...answer] as unknown as Thread['messages'],
		status: p.state === 'working' || p.state === undefined ? 'open' : 'closed',
		namespace: p.namespace,
		crew: p.crew,
		agreeCount: 0,
		concernCount: 0,
		blockCount: 0,
		standAsideCount: 0,
		advisoryCount: 0,
		proposalCount: 0,
		contributorCount: 0,
		declineCount: 0
	};
}

/** Whether the bus already shows this question: its conversation has a thread that opened with it. */
export function onBus(p: PendingAsk, threads: readonly Thread[]): boolean {
	return threads.some((t) => {
		if (threadConversationId(t) !== p.conversationId) return false;
		return t.messages.some((m) => m.messageType === 'thread_start' && (m.content ?? '').trim() === p.question.trim());
	});
}

/** The threads with a stand-in for each asked question the bus does not show yet, so it is listed at once. */
export function withPending(threads: readonly Thread[], pending: readonly PendingAsk[]): Thread[] {
	const waiting = pending.filter((p) => !onBus(p, threads));
	return waiting.length === 0 ? [...threads] : [...threads, ...waiting.map(pendingThread)];
}
