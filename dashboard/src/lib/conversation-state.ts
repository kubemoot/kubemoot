// Where a conversation stands, from the crew's gateway: running, paused, queued behind other
// questions, stopped or finished, for the conversation and each turn. The page offers Stop,
// Pause, Resume and Cancel from it, so they work for a conversation another client started and
// for a question the crew has not begun, and says where a waiting question stands in the queue.

import { resolve } from '$app/paths';
import type { CrewScope } from '#lib/crewScope.js';
import type { ConversationState, TurnStatus } from '#lib/types/bus.js';
import type { RunState } from '#lib/conversation-actions.js';

const statePath = (s: CrewScope, id: string) =>
	resolve('/api/gateway/[namespace]/[crew]/[conversationId]/state', { namespace: s.namespace, crew: s.crew, conversationId: id });

function isState(body: unknown): body is ConversationState {
	const s = body as ConversationState | null;
	return typeof s === 'object' && s !== null && typeof s.state === 'string' && Array.isArray(s.turns);
}

/**
 * The conversation's state, or null when the gateway cannot say (it is older than the state
 * read, the conversation is not the person's, the crew is away): the page then goes by the
 * messages it has.
 */
export async function fetchConversationState(fetchFn: typeof fetch, scope: CrewScope, id: string): Promise<ConversationState | null> {
	try {
		const res = await fetchFn(statePath(scope, id));
		if (!res.ok) return null;
		const body: unknown = await res.json();
		return isState(body) ? body : null;
	} catch {
		return null;
	}
}

/** What the actions menu needs from the state: whether the conversation can be controlled and how. */
export function runFromState(state: ConversationState): RunState {
	const turns = state.turns;
	const working = turns.some((t) => t.state === 'running' || t.state === 'paused');
	const queued = turns.some((t) => t.state === 'queued');
	return {
		running: working || queued,
		paused: state.state === 'paused',
		// Only a question the crew has not taken is cancelled; once a turn runs, Stop covers it.
		queuedOnly: queued && !working,
		stopRequested: turns.some((t) => t.stopRequested)
	};
}

/** The turn that is waiting in the crew's queue, the one nearest the front. */
function waitingTurn(state: ConversationState): TurnStatus | undefined {
	return state.turns.filter((t) => t.state === 'queued').sort((a, b) => (a.queuePosition ?? 0) - (b.queuePosition ?? 0))[0];
}

/** Where a waiting question stands, in words; empty when none waits. */
export function queueNote(state: ConversationState | null): string {
	const turn = state && waitingTurn(state);
	if (!turn) return '';
	const ahead = (turn.queuePosition ?? 1) - 1;
	if (ahead <= 0) return 'Waiting in the crew\'s queue: next to be taken.';
	return `Waiting in the crew's queue: ${ahead} ${ahead === 1 ? 'question' : 'questions'} ahead.`;
}

/** The questions that wait in the crew's queue, nearest the front first, with how many questions are ahead of each. */
export function queuedQuestions(state: ConversationState | null): { question: string; ahead: number }[] {
	if (!state) return [];
	return state.turns
		.filter((t) => t.state === 'queued')
		.sort((a, b) => (a.queuePosition ?? 0) - (b.queuePosition ?? 0))
		.map((t) => ({ question: t.question ?? '', ahead: Math.max((t.queuePosition ?? 1) - 1, 0) }));
}

/** What a question that waits says under the person's message. */
export function waitingText(ahead: number): string {
	if (ahead <= 0) return 'Waiting, next to be taken';
	return `Waiting, ${ahead} ${ahead === 1 ? 'question' : 'questions'} ahead`;
}

/**
 * The status a conversation shows while the crew says work of it is in flight: Queued when
 * only waiting questions remain, Active while a turn runs or is paused; otherwise the status
 * the messages give.
 */
export function statusFromState(state: ConversationState | null, fallback: string): string {
	if (!state) return fallback;
	const run = runFromState(state);
	if (!run.running) return fallback;
	return run.queuedOnly ? 'queued' : 'open';
}

/** Whether the state shows work in flight, so the page keeps reading it. */
export function isActive(state: ConversationState | null): boolean {
	return !!state && state.turns.some((t) => t.state === 'queued' || t.state === 'running' || t.state === 'paused');
}
