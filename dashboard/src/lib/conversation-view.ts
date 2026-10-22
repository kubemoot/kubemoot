// What the discussion view shows as the conversation and what it keeps in the debug
// layer. The conversation is the question, the coordinator's subcommittee line, the
// agents' positions and the answer. Everything else is still on the thread and shows
// when the viewer turns the debug layer on.
import type { DiscussionMessage } from '#lib/types/kubemoot.js';

/** Message types that are bookkeeping about the work, not part of the conversation. */
const DEBUG_TYPES: ReadonlySet<string> = new Set(['triage_result', 'thread_close']);

/** Message types a person sends; the first one after an answer starts a new round. */
function isFromPerson(msg: DiscussionMessage): boolean {
	return msg.agentName === 'human';
}

/**
 * The messages that arrive after the thread's answer and before the person speaks
 * again: learning, memory journaling and metrics. The answer itself is not among them.
 */
export function afterAnswerMessages(messages: DiscussionMessage[]): Set<DiscussionMessage> {
	const after = new Set<DiscussionMessage>();
	let answered = false;
	for (const msg of messages) {
		if (isFromPerson(msg)) answered = false;
		else if (msg.messageType === 'synthesis') answered = true;
		else if (answered) after.add(msg);
	}
	return after;
}

/** The messages to show: all of them with the debug layer on, else the conversation. */
export function visibleMessages(
	messages: DiscussionMessage[],
	showDebug: boolean
): DiscussionMessage[] {
	if (showDebug) return messages;
	const after = afterAnswerMessages(messages);
	return messages.filter((m) => !DEBUG_TYPES.has(m.messageType) && !after.has(m));
}

/** How many messages the debug layer holds that the conversation does not show. */
export function debugMessageCount(messages: DiscussionMessage[]): number {
	return messages.length - visibleMessages(messages, false).length;
}
