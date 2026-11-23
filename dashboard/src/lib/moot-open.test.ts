import { describe, expect, it } from 'vitest';
import { conversationQuery, mootQuery, requestedThread, withThread } from './moot-open.js';

const SUBJECT = 'kubemoot.discuss.team.pilot.general.t9';

describe('mootQuery', () => {
	it('carries the thread and its subject', () => {
		expect(mootQuery({ threadId: 't9', subject: SUBJECT })).toBe(`?thread=t9&subject=${encodeURIComponent(SUBJECT)}`);
	});
	it('carries only the thread when its subject is not known', () => {
		expect(mootQuery({ threadId: 't9' })).toBe('?thread=t9');
	});
});

describe('requestedThread', () => {
	it('reads the thread and its subject from the address', () => {
		expect(requestedThread(mootQuery({ threadId: 't9', subject: SUBJECT }))).toEqual({ threadId: 't9', subject: SUBJECT });
	});
	it('asks for nothing when the address names no thread', () => {
		expect(requestedThread('')).toBeNull();
		expect(requestedThread('?range=any')).toBeNull();
	});
	it('drops a subject that is malformed or belongs to another thread', () => {
		expect(requestedThread('?thread=t9&subject=nonsense')).toEqual({ threadId: 't9' });
		expect(requestedThread(`?thread=other&subject=${SUBJECT}`)).toEqual({ threadId: 'other' });
	});
});

describe('withThread', () => {
	it('writes the thread and keeps the filter', () => {
		expect(withThread('?status=failed&thread=old', { threadId: 't9' }).toString()).toBe('status=failed&thread=t9');
	});
	it('removes the thread and its subject when nothing is open', () => {
		expect(withThread(`?q=gpu&thread=t9&subject=${SUBJECT}`, null).toString()).toBe('q=gpu');
	});
});

describe('conversationQuery', () => {
	it('names the conversation and carries the thread to read when it is not loaded', () => {
		const q = new URLSearchParams(conversationQuery({ conversationId: 'c-1', threadId: 't9', subject: SUBJECT }));
		expect(q.get('conversation')).toBe('c-1');
		expect(q.get('thread')).toBe('t9');
		expect(requestedThread(`?${q}`)).toEqual({ threadId: 't9', subject: SUBJECT });
	});
});
