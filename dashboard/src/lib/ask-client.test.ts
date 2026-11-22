import { describe, expect, it, vi } from 'vitest';
import { AskRefused, WORKING, askCrew, followAnswer, listMyConversations, parseEvents, reduceAnswer, type AnswerState } from './ask-client';

const scope = { namespace: 'team-a', crew: 'pilot' };
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });

describe('askCrew', () => {
	it('posts the question to the dashboard ask route and returns the conversation', async () => {
		const fetchFn = vi.fn(async () => json({ conversationId: 'c-1', requestedAt: 't' }));
		const result = await askCrew(fetchFn, scope, 'hello', undefined, new Date(2026, 10, 21, 9, 5, 7));
		expect(result).toEqual({ conversationId: 'c-1', requestedAt: 't' });
		const [url, init] = fetchFn.mock.calls[0] as unknown as [string, RequestInit];
		expect(url).toBe('/api/gateway/team-a/pilot/ask');
		const body = JSON.parse(init.body as string);
		expect(body).toMatchObject({ message: 'hello', localDateTime: '2026-11-21T09:05:07' });
		expect(body.conversationId).toBeUndefined();
		expect(JSON.stringify(init)).not.toMatch(/authorization|bearer/i);
	});

	it('names the conversation for a follow-up', async () => {
		const fetchFn = vi.fn(async () => json({ conversationId: 'c-1' }));
		await askCrew(fetchFn, scope, 'more', 'c-1');
		expect(JSON.parse((fetchFn.mock.calls[0] as unknown as [string, RequestInit])[1].body as string).conversationId).toBe('c-1');
	});

	it.each([
		[401, 'sign in again'],
		[403, 'no permission'],
		[404, 'not yours']
	])('shows a %i refusal with the reason given', async (status, error) => {
		const fetchFn = async () => json({ error }, status);
		await expect(askCrew(fetchFn as never, scope, 'q')).rejects.toMatchObject({ name: 'AskRefused', status, message: error });
	});

	it('says plainly when the server cannot be reached or gives no id', async () => {
		await expect(askCrew((async () => Promise.reject(new Error('net'))), scope, 'q')).rejects.toBeInstanceOf(AskRefused);
		await expect(askCrew((async () => json({})), scope, 'q')).rejects.toMatchObject({ status: 502 });
	});
});

describe('listMyConversations', () => {
	it('reads the ids the gateway lists', async () => {
		const fetchFn = async () => json({ conversations: [{ conversationId: 'a' }, { conversationId: 'b' }, {}] });
		expect(await listMyConversations(fetchFn as never, scope)).toEqual(['a', 'b']);
	});

	it('throws the refusal', async () => {
		await expect(listMyConversations((async () => json({ error: 'no' }, 403)), scope)).rejects.toMatchObject({ status: 403 });
	});
});

describe('the answer stream', () => {
	it('folds events into the answer', () => {
		let s: AnswerState = WORKING;
		for (const ev of [{ type: 'connected' }, { type: 'phase', agent: 'a' }, { type: 'finding', agent: 'a' }, { type: 'synthesis', content: 'It is 42.' }, { type: 'done' }]) {
			s = reduceAnswer(s, ev);
		}
		expect(s).toEqual({ phase: 'done', answer: 'It is 42.', agents: ['a'] });
	});

	it('keeps an error over a later done', () => {
		const s = reduceAnswer(reduceAnswer(WORKING, { type: 'error', error: 'boom' }), { type: 'done' });
		expect(s).toMatchObject({ phase: 'error', error: 'boom' });
	});

	it('parses complete events and keeps the unfinished rest', () => {
		const { events, rest } = parseEvents('data: {"type":"a"}\n\n: hb\n\ndata: {"type":"b"}\n\ndata: {"ty');
		expect(events).toEqual([{ type: 'a' }, { type: 'b' }]);
		expect(rest).toBe('data: {"ty');
	});

	it('follows the stream to the end and reports each state', async () => {
		const body = new ReadableStream({
			start(c) {
				const e = new TextEncoder();
				c.enqueue(e.encode('data: {"type":"synthesis","content":"Hi"}\n\ndata: {"ty'));
				c.enqueue(e.encode('pe":"done"}\n\n'));
				c.close();
			}
		});
		const states: AnswerState[] = [];
		const fetchFn = vi.fn(async () => new Response(body));
		await followAnswer(fetchFn, scope, 'c-1', '2026-11-21T00:00:00Z', (s) => states.push(s));
		expect((fetchFn.mock.calls as unknown as string[][])[0][0]).toBe('/api/gateway/team-a/pilot/c-1/stream?since=2026-11-21T00%3A00%3A00Z');
		expect(states.at(-1)).toEqual({ phase: 'done', answer: 'Hi', agents: [] });
	});

	it('reports a stream that ends early, and throws a refusal', async () => {
		const states: AnswerState[] = [];
		await followAnswer((async () => new Response(new ReadableStream({ start: (c) => c.close() }))), scope, 'c', undefined, (s) => states.push(s));
		expect(states.at(-1)?.phase).toBe('error');
		await expect(followAnswer((async () => json({ error: 'not yours' }, 404)), scope, 'c', undefined, () => undefined)).rejects.toMatchObject({ status: 404 });
	});
});
