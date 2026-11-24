import { describe, expect, it, vi } from 'vitest';
import { fetchConversationState, isActive, queueNote, queuedQuestions, runFromState, statusFromState, waitingText } from './conversation-state';
import { statusLabel } from './thread-status';
import type { ConversationState } from '#lib/types/bus.js';

const scope = { namespace: 'ns', crew: 'pilot' };
const state = (turns: ConversationState['turns'], overall: ConversationState['state'] = 'running'): ConversationState => ({
	conversationId: 'c-1',
	state: overall,
	turns
});
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });

describe('fetchConversationState', () => {
	it('reads the state through the dashboard server', async () => {
		const fetchFn = vi.fn(async () => json(state([{ state: 'running', threadId: 't1' }]))) as unknown as typeof fetch;
		const got = await fetchConversationState(fetchFn, scope, 'c-1');
		expect(got?.state).toBe('running');
		expect((fetchFn as unknown as ReturnType<typeof vi.fn>).mock.calls[0][0]).toBe('/api/gateway/ns/pilot/c-1/state');
	});

	it('is null for a refusal, a body that is not a state, and a failed call', async () => {
		expect(await fetchConversationState(async () => json({ error: 'no' }, 404), scope, 'c-1')).toBeNull();
		expect(await fetchConversationState(async () => json({ hello: 1 }), scope, 'c-1')).toBeNull();
		expect(await fetchConversationState(async () => { throw new Error('down'); }, scope, 'c-1')).toBeNull();
	});
});

describe('runFromState', () => {
	it('is not running for stopped and finished turns', () => {
		expect(runFromState(state([{ state: 'stopped' }, { state: 'finished' }], 'finished')).running).toBe(false);
	});

	it('is running, and paused when the conversation is', () => {
		expect(runFromState(state([{ state: 'running' }]))).toMatchObject({ running: true, paused: false, queuedOnly: false });
		expect(runFromState(state([{ state: 'paused' }], 'paused'))).toMatchObject({ running: true, paused: true });
	});

	it('is queued only when nothing has begun, so the action is Cancel', () => {
		expect(runFromState(state([{ state: 'queued', queuePosition: 2 }], 'queued'))).toMatchObject({ running: true, queuedOnly: true });
		expect(runFromState(state([{ state: 'running' }, { state: 'queued', queuePosition: 2 }])).queuedOnly).toBe(false);
	});

	it('says a stop is on its way', () => {
		expect(runFromState(state([{ state: 'running', stopRequested: true }])).stopRequested).toBe(true);
	});
});

describe('queueNote', () => {
	it('says where the question waits', () => {
		expect(queueNote(state([{ state: 'queued', queuePosition: 1 }], 'queued'))).toBe("Waiting in the crew's queue: next to be taken.");
		expect(queueNote(state([{ state: 'queued', queuePosition: 2 }], 'queued'))).toBe("Waiting in the crew's queue: 1 question ahead.");
		expect(queueNote(state([{ state: 'queued', queuePosition: 4 }], 'queued'))).toBe("Waiting in the crew's queue: 3 questions ahead.");
	});

	it('is empty when nothing waits', () => {
		expect(queueNote(null)).toBe('');
		expect(queueNote(state([{ state: 'running' }]))).toBe('');
	});
});

describe('isActive', () => {
	it('is true while anything is queued, running or paused', () => {
		expect(isActive(state([{ state: 'finished' }, { state: 'queued' }]))).toBe(true);
		expect(isActive(state([{ state: 'finished' }], 'finished'))).toBe(false);
		expect(isActive(null)).toBe(false);
	});
});

describe('the waiting question', () => {
	const waiting = state([{ state: 'finished' }, { state: 'queued', question: 'And the nodes?', queuePosition: 3 }], 'queued');

	it('is listed as the person\'s question with how many are ahead', () => {
		expect(queuedQuestions(waiting)).toEqual([{ question: 'And the nodes?', ahead: 2 }]);
		expect(queuedQuestions(null)).toEqual([]);
		expect(queuedQuestions(state([{ state: 'running' }]))).toEqual([]);
	});

	it('says how many questions are ahead', () => {
		expect(waitingText(0)).toBe('Waiting, next to be taken');
		expect(waitingText(1)).toBe('Waiting, 1 question ahead');
		expect(waitingText(2)).toBe('Waiting, 2 questions ahead');
	});

	it('shows Queued, or Active when a turn runs, instead of Closed while work is in flight', () => {
		expect(statusFromState(waiting, 'closed')).toBe('queued');
		expect(statusFromState(state([{ state: 'running' }, { state: 'queued', queuePosition: 2 }]), 'closed')).toBe('open');
		expect(statusFromState(state([{ state: 'paused' }], 'paused'), 'closed')).toBe('open');
		expect(statusFromState(state([{ state: 'finished' }], 'finished'), 'closed')).toBe('closed');
		expect(statusFromState(null, 'synthesized')).toBe('synthesized');
		expect(statusLabel('queued')).toBe('Queued');
	});
});
