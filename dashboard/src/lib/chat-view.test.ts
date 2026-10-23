import { describe, it, expect } from 'vitest';
import type { DiscussionMessage } from '#lib/types/kubemoot.js';
import type { Thread } from '#lib/stores/discussions.js';
import { canSend, chatAsText, chatMessages, chatTurns, crewThreads, detailsLine } from './chat-view';

let n = 0;
function msg(agentName: string, messageType: string, at: string, extra: Partial<DiscussionMessage> = {}): DiscussionMessage {
	return {
		messageId: `m${n++}`,
		threadId: 't1',
		agentName,
		messageType,
		content: `${agentName} ${messageType}`,
		timestamp: `2026-10-23T12:${at}Z`,
		...extra
	} as DiscussionMessage;
}

function conversation(): DiscussionMessage[] {
	return [
		msg('human', 'thread_start', '00:00'),
		msg('coordinator', 'advisory', '00:01'),
		msg('a1', 'triage_result', '00:02'),
		msg('a1', 'agree', '00:05', { metadata: { inputTokens: 100, outputTokens: 20 } }),
		msg('a2', 'stand_aside', '00:06'),
		msg('coordinator', 'synthesis', '00:10', { content: 'the answer' }),
		msg('coordinator', 'thread_close', '00:11', { metadata: { totalTokens: { synth: { in: 50, out: 30 } } } }),
		msg('human', 'reply', '01:00', { content: 'and then?' }),
		msg('a1', 'agree', '01:03', { metadata: { inputTokens: 10, outputTokens: 5 } }),
		msg('coordinator', 'synthesis', '01:08', { content: 'second answer', metadata: { inputTokens: 7, outputTokens: 3 } })
	];
}

describe('chatMessages', () => {
	it('keeps only the person and the syntheses', () => {
		const kept = chatMessages(conversation());
		expect(kept.map((m) => `${m.agentName}:${m.messageType}`)).toEqual([
			'human:thread_start',
			'coordinator:synthesis',
			'human:reply',
			'coordinator:synthesis'
		]);
	});

	it('drops the person\'s pause and stop signals', () => {
		const out = chatMessages([msg('human', 'thread_pause', '00:00'), msg('human', 'stop_requested', '00:01')]);
		expect(out).toEqual([]);
	});

	it('shows nothing for a thread with no answer yet but the question', () => {
		expect(chatMessages([msg('human', 'thread_start', '00:00'), msg('a1', 'waking', '00:01')])).toHaveLength(1);
	});
});

describe('chatTurns', () => {
	it('orders you and crew turns, follow-ups in the same conversation', () => {
		const turns = chatTurns(conversation());
		expect(turns.map((t) => t.from)).toEqual(['you', 'crew', 'you', 'crew']);
		expect(turns[1].text).toBe('the answer');
		expect(turns[2].text).toBe('and then?');
	});

	it('falls back to the userQuery metadata for the question text', () => {
		const turns = chatTurns([msg('human', 'thread_start', '00:00', { content: '', metadata: { userQuery: 'hello' } })]);
		expect(turns[0].text).toBe('hello');
	});

	it('measures elapsed time from the question to the answer, per round', () => {
		const turns = chatTurns(conversation());
		expect(turns[1].details?.elapsedMs).toBe(10_000);
		expect(turns[3].details?.elapsedMs).toBe(8_000);
	});

	it('sums agent and coordinator tokens for the round', () => {
		const turns = chatTurns(conversation());
		expect(turns[1].details).toMatchObject({ inputTokens: 150, outputTokens: 50 });
		expect(turns[3].details).toMatchObject({ inputTokens: 17, outputTokens: 8 });
	});

	it('leaves tokens undefined when nothing carries them', () => {
		const turns = chatTurns([msg('human', 'thread_start', '00:00'), msg('coordinator', 'synthesis', '00:02')]);
		expect(turns[1].details?.inputTokens).toBeUndefined();
		expect(turns[1].details?.elapsedMs).toBe(2_000);
	});

	it('leaves elapsed undefined for a bad timestamp', () => {
		const turns = chatTurns([
			msg('human', 'thread_start', '00:00', { timestamp: 'nope' }),
			msg('coordinator', 'synthesis', '00:02')
		]);
		expect(turns[1].details?.elapsedMs).toBeUndefined();
	});
});

describe('detailsLine', () => {
	it('is empty without details', () => {
		expect(detailsLine(undefined)).toBe('');
		expect(detailsLine({})).toBe('');
	});

	it('shows elapsed time and tokens', () => {
		expect(detailsLine({ elapsedMs: 72_000, inputTokens: 1000, outputTokens: 234 })).toBe(
			'1m12s to answer - 1,234 tokens (1,000 in, 234 out)'
		);
	});
});

describe('details toggle in the copied chat', () => {
	const turns = chatTurns(conversation());

	it('omits details when the toggle is off', () => {
		expect(chatAsText(turns, false)).not.toContain('tokens');
	});

	it('includes details under each answer when on', () => {
		const text = chatAsText(turns, true);
		expect(text).toContain('Crew: the answer\n(10.0s to answer - 200 tokens (150 in, 50 out))');
		expect(text).toContain('You: and then?');
	});
});

describe('canSend', () => {
	const scope = { namespace: 'kubemoot', crew: 'pilot' };
	const ok = { scope, text: 'hi', sending: false, readOnly: false };

	it('allows a send with a crew selected', () => {
		expect(canSend(ok)).toBe(true);
	});
	it('is disabled until one crew is selected', () => {
		expect(canSend({ ...ok, scope: null })).toBe(false);
	});
	it('is disabled for blank text, a send in flight, or read-only mode', () => {
		expect(canSend({ ...ok, text: '   ' })).toBe(false);
		expect(canSend({ ...ok, sending: true })).toBe(false);
		expect(canSend({ ...ok, readOnly: true })).toBe(false);
	});
});

describe('crewThreads', () => {
	const t = (namespace: string, crew?: string) => ({ threadId: `${namespace}-${crew}`, namespace, crew }) as Thread;
	it('keeps threads of the selected crew only', () => {
		const list = [t('a', 'x'), t('a', 'y'), t('b', 'x')];
		expect(crewThreads(list, { namespace: 'a', crew: 'x' })).toEqual([list[0]]);
	});
	it('keeps a thread whose crew is not yet known in the namespace', () => {
		const list = [t('a')];
		expect(crewThreads(list, { namespace: 'a', crew: 'x' })).toEqual(list);
	});
	it('returns all threads with no crew selected', () => {
		const list = [t('a', 'x')];
		expect(crewThreads(list, null)).toEqual(list);
	});
});
