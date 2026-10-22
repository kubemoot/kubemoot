import { describe, expect, it } from 'vitest';
import type { DiscussionMessage } from '#lib/types/kubemoot.js';
import { afterAnswerMessages, debugMessageCount, visibleMessages } from './conversation-view';

function msg(agentName: string, messageType: DiscussionMessage['messageType'], id = `${agentName}-${messageType}`): DiscussionMessage {
	return { messageId: id, threadId: 't1', agentName, messageType, content: '', channel: 'general' };
}

const question = msg('human', 'thread_start');
const subcommittee = msg('coordinator', 'subcommittee');
const triage = msg('coordinator', 'triage_result');
const agree = msg('k8s', 'agree');
const answer = msg('coordinator', 'synthesis');
const close = msg('coordinator', 'thread_close');
const journal = msg('k8s', 'agree', 'k8s-journal');
const metrics = msg('coordinator', 'heartbeat');

describe('visibleMessages', () => {
	const thread = [question, subcommittee, triage, agree, answer, close, journal, metrics];

	it('shows the conversation: question, subcommittee line, positions and answer', () => {
		expect(visibleMessages(thread, false)).toEqual([question, subcommittee, agree, answer]);
	});

	it('moves triage, the close and post-answer work out of view, deleting nothing', () => {
		expect(visibleMessages(thread, true)).toEqual(thread);
		expect(debugMessageCount(thread)).toBe(4);
	});

	it('handles an empty thread', () => {
		expect(visibleMessages([], false)).toEqual([]);
		expect(debugMessageCount([])).toBe(0);
	});

	it('keeps everything while the thread has no answer yet', () => {
		const live = [question, subcommittee, agree];
		expect(visibleMessages(live, false)).toEqual(live);
	});
});

describe('afterAnswerMessages', () => {
	it('holds only what follows the answer', () => {
		const after = afterAnswerMessages([question, agree, answer, journal]);
		expect([...after]).toEqual([journal]);
	});

	it('ends at the next message from the person, whose round shows normally', () => {
		const reply = msg('human', 'reply');
		const round2 = msg('k8s', 'agree', 'round2');
		const after = afterAnswerMessages([question, answer, journal, reply, round2]);
		expect([...after]).toEqual([journal]);
		expect(visibleMessages([question, answer, journal, reply, round2], false)).toEqual([
			question,
			answer,
			reply,
			round2
		]);
	});

	it('is empty when nothing follows the answer', () => {
		expect(afterAnswerMessages([question, answer]).size).toBe(0);
	});
});
