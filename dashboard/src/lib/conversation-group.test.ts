import { describe, expect, it } from 'vitest';
import type { Thread } from '#lib/stores/discussions.js';
import { chatTurns } from './chat-view.js';
import { groupConversations, withPending, type PendingAsk } from './conversation-group.js';

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

describe('the order and the count of the list', () => {
	const single = (id: string, at: string, conv?: string) =>
		thread(id, 'closed', at, [msg(id, 'coordinator', 'thread_start', `question ${id}`, at, conv), msg(id, 'coordinator', 'synthesis', 'ok', at)]);

	it('lists one entry per conversation id, even when the threads arrive in any order or from two reads', () => {
		const again = { ...first, messages: [...first.messages] };
		const list = groupConversations([again, second, first, single('t9', '09:00', 'c-9')]);
		expect(list.map((c) => c.threadIds)).toEqual([['t9'], ['t1', 't2']]);
	});

	it('sorts by the newest activity, newest first, whatever order the threads come in', () => {
		const a = single('a', '01:00');
		const b = single('b', '05:00');
		const c = single('c', '03:00');
		const order = (input: Thread[]) => groupConversations(input).map((x) => x.threadId);
		expect(order([a, b, c])).toEqual(['b', 'c', 'a']);
		expect(order([c, a, b])).toEqual(['b', 'c', 'a']);
	});

	it('moves a conversation up when a follow-up makes it the newest', () => {
		const old = single('old', '01:00', 'c-old');
		const mid = single('mid', '03:00');
		const followUp = single('old-2', '08:00', 'c-old');
		expect(groupConversations([mid, old]).map((c) => c.threadId)).toEqual(['mid', 'old']);
		expect(groupConversations([mid, old, followUp]).map((c) => c.threadId)).toEqual(['old', 'mid']);
	});

	it('puts a thread with no times last, in a fixed order', () => {
		const bare = (id: string): Thread => ({ ...thread(id, 'closed', '00:00', [{ messageId: `b-${id}`, threadId: id, agentName: 'human', messageType: 'thread_start', content: id }] as never), startedAt: '' });
		const dated = single('d', '04:00');
		expect(groupConversations([bare('z'), bare('y'), dated]).map((c) => c.threadId)).toEqual(['d', 'y', 'z']);
		expect(groupConversations([dated, bare('y'), bare('z')]).map((c) => c.threadId)).toEqual(['d', 'y', 'z']);
	});
});

describe('a question just asked', () => {
	const ask = (over: Partial<PendingAsk> = {}): PendingAsk => ({
		conversationId: 'c-new',
		question: 'Is it up?',
		namespace: 'ns',
		crew: 'pilot',
		askedAt: '2026-11-21T23:00:00Z',
		state: 'working',
		...over
	});
	const older = thread('t-old', 'closed', '01:00', [msg('t-old', 'coordinator', 'thread_start', 'Older', '01:00', 'c-old')]);

	it('is listed first at once, working, before the bus shows it', () => {
		const [top, next] = groupConversations(withPending([older], [ask()]));
		expect(top.status).toBe('open');
		expect(chatTurns(top.messages).map((t) => t.text)).toEqual(['Is it up?']);
		expect(top.namespace).toBe('ns');
		expect(top.crew).toBe('pilot');
		expect(next.threadId).toBe('t-old');
	});

	it('shows the answer the gateway streamed, and is closed when the crew is done', () => {
		const [top] = groupConversations(withPending([older], [ask({ answer: 'Yes.', state: 'done' })]));
		expect(chatTurns(top.messages).map((t) => `${t.from}: ${t.text}`)).toEqual(['you: Is it up?', 'crew: Yes.']);
		expect(top.status).toBe('closed');
	});

	it('gives way to the crew thread once the bus shows the question, so it is listed once', () => {
		const real = thread('t-real', 'open', '23:00', [msg('t-real', 'coordinator', 'thread_start', 'Is it up?', '23:00', 'c-new')]);
		const list = groupConversations(withPending([older, real], [ask()]));
		expect(list.map((c) => c.threadIds)).toEqual([['t-real'], ['t-old']]);
	});

	it('joins the conversation it follows up until the bus shows the follow-up', () => {
		const list = groupConversations(withPending([older], [ask({ conversationId: 'c-old', question: 'And now?' })]));
		expect(list).toHaveLength(1);
		expect(chatTurns(list[0].messages).map((t) => t.text)).toEqual(['Older', 'And now?']);
		expect(list[0].status).toBe('open');
	});

	it('changes nothing when nothing is pending', () => {
		expect(withPending([older], [])).toEqual([older]);
	});
});
