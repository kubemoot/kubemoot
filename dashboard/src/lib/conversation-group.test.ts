import { describe, expect, it } from 'vitest';
import type { Thread } from '#lib/stores/discussions.js';
import { chatTurns } from './chat-view.js';
import { groupConversations } from './conversation-group.js';

let n = 0;
const msg = (threadId: string, agentName: string, messageType: string, content: string, at: string, conversationId?: string) =>
	({ messageId: `m${n++}`, threadId, agentName, messageType, content, timestamp: `2026-11-21T12:${at}Z`, ...(conversationId ? { metadata: { conversationId } } : {}) }) as never;

function thread(threadId: string, status: Thread['status'], startedAt: string, messages: Thread['messages']): Thread {
	return { threadId, status, startedAt: `2026-11-21T12:${startedAt}Z`, messages, userQuery: '', channel: 'general' } as Thread;
}

const first = thread('t1', 'closed', '00:00', [
	msg('t1', 'coordinator', 'thread_start', 'What are the two GPU temperatures?', '00:00', 'c-1'),
	msg('t1', 'coordinator', 'synthesis', '61C and 46C', '00:30')
]);
const second = thread('t2', 'closed', '01:00', [
	msg('t2', 'coordinator', 'thread_start', 'Which one is hotter?', '01:00', 'c-1'),
	msg('t2', 'coordinator', 'synthesis', 'The first, at 61C', '01:20')
]);

describe('groupConversations', () => {
	it('makes one conversation of the threads that share a conversation id, in order', () => {
		const grouped = groupConversations([second, first]);
		expect(grouped).toHaveLength(1);
		expect(grouped[0].threadIds).toEqual(['t1', 't2']);
		expect(grouped[0].latestThreadId).toBe('t2');
		expect(chatTurns(grouped[0].messages).map((t) => `${t.from}: ${t.text}`)).toEqual([
			'you: What are the two GPU temperatures?',
			'crew: 61C and 46C',
			'you: Which one is hotter?',
			'crew: The first, at 61C'
		]);
	});

	it('is working while any of its threads is, and otherwise has the newest thread status', () => {
		const running = thread('t2', 'open', '01:00', second.messages);
		expect(groupConversations([running, first])[0].status).toBe('open');
		expect(groupConversations([second, first])[0].status).toBe('closed');
	});

	it('keeps a thread with no conversation id, or another conversation, apart', () => {
		const loose = thread('t3', 'closed', '02:00', [msg('t3', 'human', 'thread_start', 'old', '02:00')]);
		const other = thread('t4', 'closed', '03:00', [msg('t4', 'coordinator', 'thread_start', 'other', '03:00', 'c-2')]);
		expect(groupConversations([other, loose, second, first]).map((c) => c.threadIds)).toEqual([['t4'], ['t3'], ['t1', 't2']]);
	});

	it('leaves a single thread as it is and does not change the threads it merges', () => {
		const [only] = groupConversations([first]);
		expect(only.messages).toEqual(first.messages);
		expect(only.threadIds).toEqual(['t1']);
		groupConversations([second, first]);
		expect(first.messages).toHaveLength(2);
	});

	it('handles no threads', () => {
		expect(groupConversations([])).toEqual([]);
	});
});
