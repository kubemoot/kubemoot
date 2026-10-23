// The Messages page: a person's chat with a crew. It shows what the person said and
// the crew's answers (the synthesis on each thread), and nothing of the agents'
// internal discussion; that stays on the Discussions page. Each answer carries
// optional details: the time from the question to the answer and the tokens used.

import type { DiscussionMessage } from '#lib/types/kubemoot.js';
import { isContribution, metadataStats } from '#lib/discussion-timing.js';
import { formatDuration } from '#lib/duration-format.js';
import type { CrewScope } from '#lib/crewScope.js';
import type { Thread } from '#lib/stores/discussions.js';

/** What a person sends: the opening question, a reply, a follow-up. */
const PERSON_TYPES: ReadonlySet<string> = new Set(['thread_start', 'reply', 'follow_up']);

export interface AnswerDetails {
	/** Milliseconds from the person's question to this answer. */
	elapsedMs?: number;
	inputTokens?: number;
	outputTokens?: number;
}

export interface ChatTurn {
	id: string;
	from: 'you' | 'crew';
	text: string;
	timestamp?: string;
	/** On a crew turn: how the answer was made. */
	details?: AnswerDetails;
}

function isPersonTurn(msg: DiscussionMessage): boolean {
	return msg.agentName === 'human' && PERSON_TYPES.has(msg.messageType);
}

function isAnswer(msg: DiscussionMessage): boolean {
	return msg.messageType === 'synthesis';
}

/** The messages the chat shows: the person's own and the crew's syntheses. */
export function chatMessages(messages: DiscussionMessage[]): DiscussionMessage[] {
	return messages.filter((m) => isPersonTurn(m) || isAnswer(m));
}

function sum(a: number | undefined, b: number | undefined): number | undefined {
	return a === undefined && b === undefined ? undefined : (a ?? 0) + (b ?? 0);
}

/** The coordinator's tokens a thread_close reports, summed over its steps. */
function closeTokens(close: DiscussionMessage): { in?: number; out?: number } {
	const totals = close.metadata?.totalTokens;
	if (!totals || typeof totals !== 'object') return {};
	const steps = Object.values(totals);
	const total = (pick: (s: { in?: number; out?: number }) => number | undefined) =>
		steps.reduce((acc, s) => acc + (pick(s ?? {}) ?? 0), 0) || undefined;
	return { in: total((s) => s.in), out: total((s) => s.out) };
}

/** The coordinator's tokens for a round: its thread_close totals, else the answer's own. */
function coordinatorTokens(round: DiscussionMessage[], answer: DiscussionMessage) {
	const close = round.find((m) => m.messageType === 'thread_close');
	if (close) return closeTokens(close);
	const own = metadataStats(answer);
	return { in: own.inputTokens, out: own.outputTokens };
}

/** Tokens a round used: the agents' contributions, plus the coordinator's own. */
function roundTokens(round: DiscussionMessage[], answer: DiscussionMessage) {
	let input: number | undefined;
	let output: number | undefined;
	for (const m of round.filter(isContribution)) {
		const s = metadataStats(m);
		input = sum(input, s.inputTokens);
		output = sum(output, s.outputTokens);
	}
	const coordinator = coordinatorTokens(round, answer);
	return { inputTokens: sum(input, coordinator.in), outputTokens: sum(output, coordinator.out) };
}

function elapsedBetween(from: string | undefined, to: string | undefined): number | undefined {
	if (!from || !to) return undefined;
	const ms = Date.parse(to) - Date.parse(from);
	return Number.isFinite(ms) && ms >= 0 ? ms : undefined;
}

/**
 * The thread as a chat: the person's messages and the crew's answers in order. Each
 * answer's details cover the round from the person's last message to the answer.
 */
export function chatTurns(messages: DiscussionMessage[]): ChatTurn[] {
	const turns: ChatTurn[] = [];
	let round: DiscussionMessage[] = [];
	let asked: string | undefined;
	const answers: { turn: ChatTurn; answer: DiscussionMessage; round: DiscussionMessage[]; asked?: string }[] = [];
	for (const msg of messages) {
		if (isPersonTurn(msg)) {
			round = [];
			asked = msg.timestamp;
			turns.push({
				id: msg.messageId,
				from: 'you',
				text: msg.content || msg.metadata?.userQuery || '',
				timestamp: msg.timestamp
			});
			continue;
		}
		round.push(msg);
		if (!isAnswer(msg)) continue;
		const turn: ChatTurn = { id: msg.messageId, from: 'crew', text: msg.content ?? '', timestamp: msg.timestamp };
		turns.push(turn);
		// The round keeps filling after the answer (the coordinator's thread_close follows
		// it), so the details are worked out once every message is in.
		answers.push({ turn, answer: msg, round, asked });
	}
	for (const { turn, answer, round: rounded, asked: from } of answers) {
		turn.details = { elapsedMs: elapsedBetween(from, answer.timestamp), ...roundTokens(rounded, answer) };
	}
	return turns;
}

/** The details line of an answer; empty when the answer carries none. */
export function detailsLine(d: AnswerDetails | undefined): string {
	if (!d) return '';
	const parts: string[] = [];
	const time = formatDuration(d.elapsedMs);
	if (time) parts.push(`${time} to answer`);
	if (d.inputTokens !== undefined || d.outputTokens !== undefined) {
		const total = (d.inputTokens ?? 0) + (d.outputTokens ?? 0);
		parts.push(
			`${total.toLocaleString('en-US')} tokens (${(d.inputTokens ?? 0).toLocaleString('en-US')} in, ${(d.outputTokens ?? 0).toLocaleString('en-US')} out)`
		);
	}
	return parts.join(' - ');
}

/** The threads that belong to the selected crew, newest first as given. */
export function crewThreads(threads: Thread[], scope: CrewScope | null): Thread[] {
	if (!scope) return threads;
	return threads.filter(
		(t) => t.namespace === scope.namespace && (t.crew === undefined || t.crew === scope.crew)
	);
}

/** Sending needs one selected crew, text, and no send in flight. */
export function canSend(opts: {
	scope: CrewScope | null;
	text: string;
	sending: boolean;
	readOnly: boolean;
}): boolean {
	return !!opts.scope && !opts.readOnly && !opts.sending && opts.text.trim().length > 0;
}

/** The chat as plain text for the clipboard. */
export function chatAsText(turns: ChatTurn[], showDetails: boolean): string {
	return turns
		.map((t) => {
			const line = `${t.from === 'you' ? 'You' : 'Crew'}: ${t.text}`;
			const details = showDetails && t.from === 'crew' ? detailsLine(t.details) : '';
			return details ? `${line}\n(${details})` : line;
		})
		.join('\n\n');
}
