// What the discussion view shows as the conversation's answer: the answer itself, or a
// plain statement of why there is none yet (still working, failed, stopped).
import type { DiscussionMessage } from '#lib/types/kubemoot.js';

export type AnswerState =
	| { kind: 'answered'; text: string; note?: string; messageId: string }
	| { kind: 'failed'; reason: string }
	| { kind: 'stopped' }
	| { kind: 'working' };

const SOURCES_NOTE = 'Some sources could not be reached';

/** Splits the coordinator's trailing "Some sources could not be reached" paragraph from the answer. */
export function splitSourcesNote(content: string): { text: string; note?: string } {
	const trimmed = content.trim();
	const at = trimmed.lastIndexOf(SOURCES_NOTE);
	if (at < 0 || trimmed.slice(at).includes('\n\n')) return { text: trimmed };
	return { text: trimmed.slice(0, at).trim(), note: trimmed.slice(at).trim() };
}

function failureReason(messages: DiscussionMessage[]): string {
	const failure = messages.findLast((m) => m.messageType === 'failure');
	const reason = failure?.metadata?.reason || failure?.metadata?.lastError || failure?.content;
	return reason ? String(reason).trim() : 'The thread closed without an answer.';
}

/** The answer of the thread's latest round, or the plain reason there is none. */
export function answerState(messages: DiscussionMessage[], status: 'open' | 'synthesized' | 'closed'): AnswerState {
	const lastHuman = messages.findLastIndex((m) => m.agentName === 'human' && m.messageType !== 'thread_start');
	const lastAnswer = messages.findLastIndex((m) => m.messageType === 'synthesis' && !!m.content);
	if (lastAnswer >= 0 && lastAnswer > lastHuman && messages[lastAnswer].metadata?.outcome === 'stopped') return { kind: 'stopped' };
	if (lastAnswer >= 0 && lastAnswer > lastHuman) {
		const { text, note } = splitSourcesNote(messages[lastAnswer].content ?? '');
		return { kind: 'answered', text, note, messageId: messages[lastAnswer].messageId };
	}
	const round = messages.slice(Math.max(lastHuman, 0));
	if (round.some((m) => m.messageType === 'stop_requested')) return { kind: 'stopped' };
	if (status === 'open') return { kind: 'working' };
	return { kind: 'failed', reason: failureReason(round) };
}
