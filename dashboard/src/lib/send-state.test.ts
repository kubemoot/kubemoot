import { describe, it, expect, vi, afterEach } from 'vitest';
import { get } from 'svelte/store';
import {
	PublishFailure,
	failureText,
	publishToBus,
	retryUnsent,
	sendTracked,
	unsentMessages
} from './send-state.js';
import type { DiscussionMessage } from '#lib/types/kubemoot.js';

const msg = (id: string) =>
	({ messageId: id, threadId: 't', agentName: 'human', messageType: 'thread_start', content: 'q' }) as DiscussionMessage;

const reply = (init: ResponseInit & { body?: string }, extra: Partial<Response> = {}) => {
	const res = new Response(init.body ?? '{}', init);
	for (const [k, v] of Object.entries(extra)) Object.defineProperty(res, k, { value: v });
	return res;
};

const failureOf = async (p: Promise<unknown>) => {
	try {
		await p;
	} catch (e) {
		return e as PublishFailure;
	}
	return undefined;
};

function stubFetch(impl: (url: string, init: RequestInit) => Promise<Response>) {
	vi.stubGlobal('fetch', vi.fn(impl));
}

afterEach(() => vi.unstubAllGlobals());

describe('publishToBus', () => {
	it('resolves when the server accepts the message', async () => {
		let init: RequestInit | undefined;
		stubFetch(async (_u, i) => {
			init = i;
			return reply({ status: 200 });
		});
		await publishToBus('/api/nats/publish', 's', { a: 1 });
		expect(init?.redirect).toBe('manual');
		expect(JSON.parse(init?.body as string) as unknown).toEqual({ subject: 's', data: '{"a":1}' });
	});

	it('reports a sign-in redirect as signed out', async () => {
		const res = reply({ status: 200 }, { type: 'opaqueredirect' });
		stubFetch(async () => res);
		const f = await failureOf(publishToBus('/u', 's', {}));
		expect(f?.kind).toBe('signed-out');
		expect(f?.message).toMatch(/sign in again/);
	});

	it('reports a login page served with status 200 as signed out', async () => {
		const res = reply({ status: 200, body: '<html></html>', headers: { 'content-type': 'text/html' } });
		stubFetch(async () => res);
		expect((await failureOf(publishToBus('/u', 's', {})))?.kind).toBe('signed-out');
	});

	it('reports 401 as signed out', async () => {
		stubFetch(async () => reply({ status: 401 }));
		expect((await failureOf(publishToBus('/u', 's', {})))?.kind).toBe('signed-out');
	});

	it('reports a network error as unreachable', async () => {
		stubFetch(async () => {
			throw new TypeError('Failed to fetch');
		});
		const f = await failureOf(publishToBus('/u', 's', {}));
		expect(f?.kind).toBe('unreachable');
	});

	it('reports a JSON error as refused with the server text', async () => {
		const res = reply({ status: 403, body: '{"error":"forbidden subject"}', headers: { 'content-type': 'application/json' } });
		stubFetch(async () => res);
		const f = await failureOf(publishToBus('/u', 's', {}));
		expect(f?.kind).toBe('refused');
		expect(f?.message).toContain('forbidden subject');
	});

	it('reports a refusal without a body by its status', async () => {
		const res = reply({ status: 500, body: 'not json', headers: { 'content-type': 'text/plain' } });
		stubFetch(async () => res);
		expect((await failureOf(publishToBus('/u', 's', {})))?.message).toContain('HTTP 500');
	});
});

describe('failureText', () => {
	it('uses a publish failure as written', () => {
		expect(failureText(new PublishFailure('refused', 'No.'))).toBe('No.');
	});
	it('wraps other errors and handles non-errors', () => {
		expect(failureText(new Error('boom'))).toBe('Sending failed: boom');
		expect(failureText(undefined)).toBe('Sending failed.');
	});
});

describe('sendTracked and retryUnsent', () => {
	it('marks a failed message unsent and clears the mark when a retry goes', async () => {
		unsentMessages.set(new Map());
		const m = msg('a');
		const failing = async () => {
			throw new PublishFailure('unreachable', 'down');
		};
		expect(await sendTracked(failing, 'subj', m)).toBe(false);
		expect(get(unsentMessages).get('a')?.reason).toBe('down');

		const sent: string[] = [];
		const ok = async (s: string, x: DiscussionMessage) => {
			sent.push(`${s}:${x.messageId}`);
		};
		expect(await retryUnsent(ok, 'a', get(unsentMessages))).toBe(true);
		expect(sent).toEqual(['subj:a']);
		expect(get(unsentMessages).has('a')).toBe(false);
	});

	it('does not mark a message that went, and ignores a retry of an unknown id', async () => {
		unsentMessages.set(new Map());
		expect(await sendTracked(async () => {}, 's', msg('b'))).toBe(true);
		expect(get(unsentMessages).size).toBe(0);
		expect(await retryUnsent(async () => {}, 'nope', get(unsentMessages))).toBe(false);
	});
});
