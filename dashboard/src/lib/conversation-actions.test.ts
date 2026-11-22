import { describe, expect, it } from 'vitest';
import type { Thread } from '#lib/stores/discussions.js';
import { conversationTitle, failedStores, idWithQuestion, removedLines, runState } from './conversation-actions.js';

let n = 0;
const msg = (agentName: string, messageType: string, content = '', at = '00:00') =>
	({ messageId: `m${n++}`, threadId: 't', agentName, messageType, content, timestamp: `2026-11-21T12:${at}Z` }) as never;
const thread = (status: Thread['status'], messages: Thread['messages'], userQuery = ''): Pick<Thread, 'status' | 'messages' | 'userQuery'> => ({
	status,
	messages,
	userQuery
});

describe('runState', () => {
	it('says a conversation with no thread, or a closed one, is not running', () => {
		expect(runState(undefined)).toEqual({ running: false, paused: false });
		expect(runState(thread('closed', []))).toEqual({ running: false, paused: false });
		expect(runState(thread('synthesized', []))).toEqual({ running: false, paused: false });
	});

	it('says an open thread is running, and paused after a pause', () => {
		expect(runState(thread('open', [msg('c', 'thread_start')]))).toEqual({ running: true, paused: false });
		expect(runState(thread('open', [msg('c', 'thread_pause')]))).toEqual({ running: true, paused: true });
	});

	it('follows the latest of a pause and a resume', () => {
		const resumed = thread('open', [msg('g', 'thread_pause'), msg('g', 'thread_resume')]);
		expect(runState(resumed).paused).toBe(false);
		const pausedAgain = thread('open', [msg('g', 'thread_pause'), msg('g', 'thread_resume'), msg('g', 'thread_pause')]);
		expect(runState(pausedAgain).paused).toBe(true);
	});
});

describe('conversationTitle', () => {
	it('is the first question, not the last answer', () => {
		const t = thread('closed', [msg('coordinator', 'thread_start', 'What are the two GPU temperatures?'), msg('coordinator', 'synthesis', '33C and 36C')]);
		expect(conversationTitle(t)).toBe('What are the two GPU temperatures?');
	});

	it('uses the recorded question when the start message is not loaded, never the answer', () => {
		const t = thread('closed', [msg('coordinator', 'synthesis', '33C and 36C')], 'What are the two GPU temperatures?');
		expect(conversationTitle(t)).toBe('What are the two GPU temperatures?');
	});

	it('says plainly when nothing is known yet', () => {
		expect(conversationTitle(thread('closed', [msg('coordinator', 'synthesis', 'an answer')]))).toBe('Question not loaded yet');
	});
});

describe('copy text and erasure report', () => {
	it('pairs the id with the question', () => {
		expect(idWithQuestion('c-1', 'Why?')).toBe('c-1: Why?');
	});

	it('names each store that held something, in words, and skips the empty ones', () => {
		const lines = removedLines({ conversationId: 'c', deleted: { history: 1, owner: 1, discussionMessages: 42, workspaceObjects: 0, memoryFacts: 2 }, kept: [] });
		expect(lines).toEqual(['1 conversation history', '1 ownership record', '42 discussion messages', '2 crew memory facts']);
		expect(removedLines({ conversationId: 'c', deleted: {}, kept: [] })).toEqual([]);
	});

	it('names the stores that could not be cleared and passes an unknown store as it is', () => {
		expect(failedStores({ conversationId: 'c', deleted: {}, kept: [], failed: ['workspace', 'scheduled'] })).toEqual(['workspace', 'scheduled follow-ups']);
		expect(failedStores({ conversationId: 'c', deleted: {}, kept: [] })).toEqual([]);
	});
});
