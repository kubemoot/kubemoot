import { describe, it, expect, vi, afterEach } from 'vitest';
import { get } from 'svelte/store';
import {
	awaitingAnswer,
	deliveredMessages,
	firstMessageUnsent,
	isPaused,
	publishDiscussionMessage,
	threadIsOpen,
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
		expect(f?.message).toMatch(/Sign in again/);
		expect(f?.message).not.toMatch(/[Rr]eload/);
	});

	it('reports a login page served with status 200 as signed out', async () => {
		const res = reply({ status: 200, body: '<html></html>', headers: { 'content-type': 'text/html' } });
		stubFetch(async () => res);
		expect((await failureOf(publishToBus('/u', 's', {})))?.kind).toBe('signed-out');
	});

	it('reports a 403 login page as signed out but a 5xx HTML page as a refusal', async () => {
		const html = { 'content-type': 'text/html' };
		stubFetch(async () => reply({ status: 403, body: '<html></html>', headers: html }));
		expect((await failureOf(publishToBus('/u', 's', {})))?.kind).toBe('signed-out');
		for (const status of [500, 502, 503]) {
			stubFetch(async () => reply({ status, body: '<html>Bad gateway</html>', headers: html }));
			const f = await failureOf(publishToBus('/u', 's', {}));
			expect(f?.kind).toBe('refused');
			expect(f?.message).toContain(`HTTP ${status}`);
		}
	});

	it('cuts a long refusal text to 200 characters', async () => {
		const res = reply({ status: 500, body: JSON.stringify({ error: 'x'.repeat(900) }), headers: { 'content-type': 'application/json' } });
		stubFetch(async () => res);
		const f = await failureOf(publishToBus('/u', 's', {}));
		expect(f?.message.length).toBeLessThan(260);
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

const person = (id: string, type: DiscussionMessage['messageType'], agent = 'human') =>
	({ messageId: id, threadId: 't', agentName: agent, messageType: type }) as DiscussionMessage;
const entry = (m: DiscussionMessage) => [m.messageId, { subject: 's', message: m, reason: 'down' }] as const;

describe('state derived with unsent messages', () => {
	it('a pause that did not go does not pause the thread, and a resume that did not go does not resume it', () => {
		const start = person('1', 'thread_start');
		const pause = person('2', 'thread_pause');
		const resume = person('3', 'thread_resume');
		expect(isPaused([start, pause], new Map([entry(pause)]))).toBe(false);
		expect(isPaused([start, pause], new Map())).toBe(true);
		expect(isPaused([start, pause, resume], new Map([entry(resume)]))).toBe(true);
	});

	it('a question that did not go leaves no answer owed', () => {
		const start = person('1', 'thread_start');
		const answer = person('2', 'synthesis', 'coordinator');
		const reply = person('3', 'reply');
		expect(awaitingAnswer([start], new Map([entry(start)]))).toBe(false);
		expect(awaitingAnswer([start], new Map())).toBe(true);
		expect(awaitingAnswer([start, answer, reply], new Map([entry(reply)]))).toBe(false);
		expect(awaitingAnswer([start, answer, reply], new Map())).toBe(true);
	});

	it('an open thread whose replay (a new thread_start) did not go is not open', () => {
		const replay = person('9', 'thread_start');
		expect(threadIsOpen({ status: 'open', messages: [replay] }, new Map([entry(replay)]))).toBe(false);
		expect(threadIsOpen({ status: 'open', messages: [replay] }, new Map())).toBe(true);
		expect(threadIsOpen({ status: 'synthesized', messages: [replay] }, new Map())).toBe(false);
	});

	it('a thread started by someone else stays open when an unrelated reply did not go', () => {
		const start = person('1', 'thread_start', 'scheduler');
		const reply = person('2', 'reply');
		expect(threadIsOpen({ status: 'open', messages: [start, reply] }, new Map([entry(reply)]))).toBe(true);
	});

	it('finds an unsent first message and lists the delivered ones', () => {
		const start = person('1', 'thread_start');
		const reply = person('2', 'reply');
		const unsent = new Map([entry(start)]);
		expect(firstMessageUnsent([start, reply], unsent)).toBe(true);
		expect(firstMessageUnsent([start, reply], new Map([entry(reply)]))).toBe(false);
		expect(deliveredMessages([start, reply], unsent)).toEqual([reply]);
	});
});

describe('publishDiscussionMessage', () => {
	it('posts to the dashboard publish route and tracks a failure', async () => {
		unsentMessages.set(new Map());
		let url = '';
		stubFetch(async (u) => {
			url = u;
			return reply({ status: 502, body: '{}', headers: { 'content-type': 'application/json' } });
		});
		const m = person('p1', 'thread_pause');
		expect(await sendTracked(publishDiscussionMessage, 'subj', m)).toBe(false);
		expect(url).toContain('/api/nats/publish');
		expect(get(unsentMessages).get('p1')?.reason).toContain('HTTP 502');
	});
});
