// What the actions of an open conversation say and when they apply: the state a thread is in,
// the title a list shows, the text a copy puts on the clipboard and the erasure report in words.

import type { Thread } from '#lib/stores/discussions.js';
import { chatTurns } from '#lib/chat-view.js';
import type { ErasureReport } from '#lib/conversation-actions-client.js';

export interface RunState {
	/** A discussion of the conversation is working toward an answer (or is paused). */
	running: boolean;
	paused: boolean;
}

/** Whether the conversation can be stopped, paused or resumed, from the messages on its thread. */
export function runState(thread: Pick<Thread, 'status' | 'messages'> | undefined): RunState {
	if (!thread || thread.status !== 'open') return { running: false, paused: false };
	const control = thread.messages.findLast((m) => m.messageType === 'thread_pause' || m.messageType === 'thread_resume');
	return { running: true, paused: control?.messageType === 'thread_pause' };
}

/** The first question of the conversation: what a list shows as its title. */
export function conversationTitle(thread: Pick<Thread, 'messages' | 'userQuery'>): string {
	const asked = chatTurns(thread.messages).find((t) => t.from === 'you')?.text;
	return (asked || thread.userQuery || '').trim() || 'Question not loaded yet';
}

/** The conversation id with the first question, to paste into a ticket or a note. */
export function idWithQuestion(conversationId: string, question: string): string {
	return `${conversationId}: ${question}`;
}

const STORES: Record<string, string> = {
	history: 'conversation history',
	owner: 'ownership record',
	discussionMessages: 'discussion messages',
	workspaceObjects: 'workspace files',
	coordinatorEvents: 'coordinator events',
	scheduled: 'scheduled follow-ups',
	memoryFacts: 'crew memory facts'
};

/** The stores a delete cleared, one line each, in the words a person reads; empty when it found nothing. */
export function removedLines(report: ErasureReport): string[] {
	return Object.entries(report.deleted)
		.filter(([, count]) => count > 0)
		.map(([key, count]) => `${count} ${STORES[key] ?? key}`);
}

/** The stores that could not be cleared, in words. */
export function failedStores(report: ErasureReport): string[] {
	return (report.failed ?? []).map((key) => STORES[key] ?? key);
}
