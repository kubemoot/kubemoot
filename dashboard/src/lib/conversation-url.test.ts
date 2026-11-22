import { describe, expect, it } from 'vitest';
import { openRef, withOpen } from './conversation-url.js';

const params = (q: string) => new URLSearchParams(q);

describe('openRef', () => {
	it('reads the conversation, or the thread of an item that has no conversation id', () => {
		expect(openRef(params('conversation=c-1'))).toEqual({ conversation: 'c-1' });
		expect(openRef(params('thread=t_9'))).toEqual({ thread: 't_9' });
		expect(openRef(params('conversation=c-1&thread=t1'))).toEqual({ conversation: 'c-1' });
	});

	it('reads nothing from an empty or malformed value', () => {
		expect(openRef(params(''))).toEqual({});
		expect(openRef(params('conversation='))).toEqual({});
		expect(openRef(params('conversation=a/b&thread=x y'))).toEqual({});
		expect(openRef(params(`conversation=${'x'.repeat(129)}`))).toEqual({});
	});
});

describe('withOpen', () => {
	it('writes the conversation id and keeps the filters and the search', () => {
		const next = withOpen(params('q=namespace+-smoke&status=closed&thread=old'), { conversationId: 'c-1', threadId: 't1' });
		expect(next.get('conversation')).toBe('c-1');
		expect(next.get('thread')).toBeNull();
		expect(next.get('q')).toBe('namespace -smoke');
		expect(next.get('status')).toBe('closed');
	});

	it('falls back to the thread id when there is no conversation id, and clears the item when nothing is open', () => {
		expect(withOpen(params('conversation=c-1'), { threadId: 't1' }).toString()).toBe('thread=t1');
		expect(withOpen(params('conversation=c-1&q=gpu'), null).toString()).toBe('q=gpu');
	});

	it('does not change the query it was given', () => {
		const original = params('conversation=c-1');
		withOpen(original, null);
		expect(original.get('conversation')).toBe('c-1');
	});
});
