// Asking a crew from the dashboard, as the signed-in person. The browser posts the question to
// the dashboard server, which sends it to the crew's discussion gateway with the person's own
// token; the gateway's answer, or its refusal, comes back here. Nothing in this file sees the
// token.

import { resolve } from '$app/paths';
import type { CrewScope } from '#lib/crewScope.js';

/** The gateway or the dashboard refused: the status and the words to show. */
export class AskRefused extends Error {
	constructor(
		readonly status: number,
		message: string
	) {
		super(message);
		this.name = 'AskRefused';
	}
}

export interface AskResult {
	conversationId: string;
	requestedAt?: string;
}

const askPath = (s: CrewScope) => resolve('/api/gateway/[namespace]/[crew]/ask', { namespace: s.namespace, crew: s.crew });
const streamPath = (s: CrewScope, id: string) =>
	resolve('/api/gateway/[namespace]/[crew]/[conversationId]/stream', {
		namespace: s.namespace,
		crew: s.crew,
		conversationId: id
	});
const listPath = (s: CrewScope) =>
	resolve('/api/gateway/[namespace]/[crew]/conversations', { namespace: s.namespace, crew: s.crew });

/** Why an answer was refused, in the words the server gave, with a plain fallback. */
export async function refusalOf(res: Response): Promise<AskRefused> {
	const text = await res
		.json()
		.then((body: unknown) => {
			const error = (body as { error?: unknown } | null)?.error;
			return typeof error === 'string' ? error : '';
		})
		.catch(() => '');
	return new AskRefused(res.status, text || `The dashboard answered ${res.status}.`);
}

/** The asker's clock, so tools that need today's date use theirs. */
function askerClock(now: Date): { timeZone?: string; localDateTime: string } {
	const pad = (n: number) => String(n).padStart(2, '0');
	const local = `${now.getFullYear()}-${pad(now.getMonth() + 1)}-${pad(now.getDate())}T${pad(now.getHours())}:${pad(now.getMinutes())}:${pad(now.getSeconds())}`;
	const timeZone = Intl.DateTimeFormat().resolvedOptions().timeZone;
	return { ...(timeZone ? { timeZone } : {}), localDateTime: local };
}

/**
 * Asks the crew a question: a new conversation, or a follow-up when conversationId is given.
 * Throws AskRefused for a 401, 403, 404 or any other refusal, with the reason to show.
 */
export async function askCrew(
	fetchFn: typeof fetch,
	scope: CrewScope,
	message: string,
	conversationId?: string,
	now: Date = new Date()
): Promise<AskResult> {
	let res: Response;
	try {
		res = await fetchFn(askPath(scope), {
			method: 'POST',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify({ message, ...(conversationId ? { conversationId } : {}), ...askerClock(now) })
		});
	} catch {
		throw new AskRefused(0, 'The dashboard server could not be reached. Check the connection and try again.');
	}
	if (!res.ok) throw await refusalOf(res);
	const body = (await res.json()) as Partial<AskResult>;
	if (!body.conversationId) throw new AskRefused(502, 'The crew accepted the question but gave no conversation id.');
	return { conversationId: body.conversationId, requestedAt: body.requestedAt };
}

/** The conversation ids the signed-in person started with a crew, listed by the gateway. */
export async function listMyConversations(fetchFn: typeof fetch, scope: CrewScope): Promise<string[]> {
	const res = await fetchFn(listPath(scope));
	if (!res.ok) throw await refusalOf(res);
	const body = (await res.json()) as { conversations?: { conversationId?: string }[] };
	return (body.conversations ?? []).flatMap((c) => (c.conversationId ? [c.conversationId] : []));
}

/**
 * The conversation ids the signed-in person started with any of the crews, each listed by its
 * own gateway. A crew the person may not use lists nothing; with a single crew its refusal is
 * returned in words, so the list can say why "Mine" is empty.
 */
export async function listMine(fetchFn: typeof fetch, scopes: CrewScope[]): Promise<{ ids: Set<string>; error: string }> {
	const lists = await Promise.allSettled(scopes.map((s) => listMyConversations(fetchFn, s)));
	const ids = new Set(lists.flatMap((r) => (r.status === 'fulfilled' ? r.value : [])));
	const refused = lists.find((r) => r.status === 'rejected');
	const error = refused && scopes.length === 1 ? (refused.reason?.message ?? 'Could not list your conversations.') : '';
	return { ids, error };
}

// ------------------------------------------------------------------ the answer, as it streams

export interface AnswerState {
	phase: 'working' | 'done' | 'error';
	answer: string;
	/** The person stopped the turn: it has no answer, and `answer` stays empty. */
	stopped?: boolean;
	/** The agents that have spoken so far. */
	agents: string[];
	error?: string;
}

export const WORKING: AnswerState = { phase: 'working', answer: '', agents: [] };

interface StreamEvent {
	type?: string;
	agent?: string;
	content?: string;
	error?: string;
	outcome?: string;
}

function withAgent(state: AnswerState, agent: string | undefined): AnswerState {
	return agent && !state.agents.includes(agent) ? { ...state, agents: [...state.agents, agent] } : state;
}

/** A synthesis or done event: the person's stop is no answer, and an ordinary synthesis is. */
function foldClose(state: AnswerState, ev: StreamEvent): AnswerState {
	const stopped = ev.outcome === 'stopped';
	if (ev.type === 'synthesis') return stopped ? { ...state, stopped: true } : { ...state, answer: ev.content ?? state.answer };
	return { ...state, phase: state.phase === 'error' ? 'error' : 'done', ...(stopped ? { stopped: true } : {}) };
}

/** Folds one gateway stream event into the state of the answer. */
export function reduceAnswer(state: AnswerState, ev: StreamEvent): AnswerState {
	switch (ev.type) {
		case 'synthesis':
		case 'done':
			return foldClose(state, ev);
		case 'error':
			return { ...state, phase: 'error', error: ev.error || 'The crew reported an error.' };
		case 'phase':
		case 'finding':
			return withAgent(state, ev.agent);
		default:
			return state;
	}
}

/** Splits a buffer of server-sent events into the events whose data is complete, and the unfinished rest. */
export function parseEvents(buffer: string): { events: StreamEvent[]; rest: string } {
	const blocks = buffer.split('\n\n');
	const rest = blocks.pop() ?? '';
	const events: StreamEvent[] = [];
	for (const block of blocks) {
		const data = block
			.split('\n')
			.filter((line) => line.startsWith('data:'))
			.map((line) => line.slice(5).trimStart())
			.join('\n');
		if (!data) continue;
		try {
			events.push(JSON.parse(data) as StreamEvent);
		} catch {
			// a line that is not an event (a comment or heartbeat text) is skipped
		}
	}
	return { events, rest };
}

/**
 * Follows the answer to a question through the gateway as the signed-in person, calling
 * onState with each change until the crew is done, says it failed, or the stream ends.
 * A refusal throws AskRefused with the reason.
 */
export async function followAnswer(
	fetchFn: typeof fetch,
	scope: CrewScope,
	conversationId: string,
	requestedAt: string | undefined,
	onState: (state: AnswerState) => void,
	signal?: AbortSignal
): Promise<void> {
	const since = requestedAt ? `?since=${encodeURIComponent(requestedAt)}` : '';
	const res = await fetchFn(streamPath(scope, conversationId) + since, { signal });
	if (!res.ok || !res.body) throw await refusalOf(res);
	const reader = res.body.getReader();
	const decoder = new TextDecoder();
	let state = WORKING;
	let buffer = '';
	onState(state);
	for (;;) {
		const { value, done } = await reader.read();
		if (done) break;
		buffer += decoder.decode(value, { stream: true });
		const parsed = parseEvents(buffer);
		buffer = parsed.rest;
		for (const ev of parsed.events) state = reduceAnswer(state, ev);
		onState(state);
		if (state.phase !== 'working') break;
	}
	await reader.cancel().catch(() => undefined);
	if (state.phase === 'working') onState({ ...state, phase: 'error', error: 'The stream ended before the crew answered.' });
}
