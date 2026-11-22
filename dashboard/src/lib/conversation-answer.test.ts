import { describe, expect, it } from 'vitest';
import { answerState, splitSourcesNote } from './conversation-answer.js';
import type { DiscussionMessage } from '#lib/types/kubemoot.js';

let n = 0;
const msg = (agentName: string, messageType: string, content = '', metadata?: object): DiscussionMessage =>
	({ messageId: `m${n++}`, threadId: 't', agentName, messageType, content, metadata }) as unknown as DiscussionMessage;

describe('splitSourcesNote', () => {
	it('moves the trailing sources note out of the answer', () => {
		const r = splitSourcesNote('gpu-0: 33C\n\nSome sources could not be reached: workspace_query. This answer does not include what they would have provided.');
		expect(r.text).toBe('gpu-0: 33C');
		expect(r.note).toMatch(/^Some sources could not be reached: workspace_query/);
	});
	it('leaves an answer without the note whole', () => {
		expect(splitSourcesNote('plain answer')).toEqual({ text: 'plain answer' });
	});
});

describe('answerState', () => {
	const start = msg('human', 'thread_start', 'q');
	it('is the synthesis when there is one', () => {
		const s = answerState([start, msg('coordinator', 'synthesis', 'the answer')], 'closed');
		expect(s).toMatchObject({ kind: 'answered', text: 'the answer' });
	});
	it('is working when open with no answer', () => {
		expect(answerState([start], 'open')).toEqual({ kind: 'working' });
	});
	it('is working again after a new question follows the answer', () => {
		const s = answerState([start, msg('coordinator', 'synthesis', 'a'), msg('human', 'reply', 'again')], 'open');
		expect(s.kind).toBe('working');
	});
	it('names the reason when a closed thread has no answer', () => {
		const s = answerState([start, msg('compute', 'failure', 'model timed out')], 'closed');
		expect(s).toEqual({ kind: 'failed', reason: 'model timed out' });
	});
	it('says so when closed with no answer and no failure', () => {
		expect(answerState([start], 'closed')).toEqual({ kind: 'failed', reason: 'The thread closed without an answer.' });
	});
	it('is stopped after a stop request', () => {
		expect(answerState([start, msg('human', 'stop_requested')], 'closed')).toEqual({ kind: 'stopped' });
	});
});
