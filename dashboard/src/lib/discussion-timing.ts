// Timing, tokens, and placement of a discussion's messages for the Discussions view:
// how long after the thread started each message came, how long each agent spent in
// triage and evaluation before its signal, and the thread's summary. Step durations
// are derived the way kmctl derives them, so the two tools agree: triage runs from an
// agent's triaging message to its evaluating message (or to its signal when it never
// evaluated), and evaluation from its evaluating message to its signal. Fields a
// message does not carry stay undefined; nothing is shown as zero in their place.

import type { DiscussionMessage } from '#lib/types/kubemoot.js';
import { messageTimeMs, placementOf, threadStartMs } from '#lib/discussion-spans.js';

/**
 * The signals that end an agent's part in a round: its contribution. These are the
 * agent signals of the bus contract's messageType that the span graph ranks in
 * SIGNAL_PRIORITY, plus failure; a test pins the two together.
 */
export const CONTRIBUTION_SIGNALS: ReadonlySet<string> = new Set([
	'agree',
	'concern',
	'block',
	'stand_aside',
	'failure',
	// Legacy aliases of agree and stand_aside.
	'contribution',
	'decline'
]);

/** Legacy message types and the signals they stand for. */
export const LEGACY_SIGNALS: Readonly<Record<string, string>> = {
	contribution: 'agree',
	decline: 'stand_aside'
};

/**
 * The order signal kinds are listed in a summary, the same order kmctl's summary
 * uses; other kinds follow alphabetically.
 */
const SIGNAL_ORDER = ['agree', 'concern', 'block', 'failure', 'stand_aside'];

export function isContribution(msg: DiscussionMessage): boolean {
	return CONTRIBUTION_SIGNALS.has(msg.messageType);
}

function signalKind(messageType: string): string {
	return LEGACY_SIGNALS[messageType] ?? messageType;
}

function finite(ms: number): number | undefined {
	return Number.isFinite(ms) ? ms : undefined;
}

/** A message time as a local date and time; empty when the message has none. */
export function formatTimestamp(iso: string | undefined): string {
	if (!iso) return '';
	const date = new Date(iso);
	if (Number.isNaN(date.getTime())) return '';
	return date.toLocaleString([], {
		month: 'short',
		day: 'numeric',
		hour: '2-digit',
		minute: '2-digit',
		second: '2-digit'
	});
}

/** A count with thousands separators. */
export function formatCount(n: number): string {
	return n.toLocaleString('en-US');
}

/** How one message was made, as far as it says. */
export interface MessageStats {
	/** Milliseconds after the thread started. */
	elapsedMs?: number;
	/** On a contribution: the agent's triage and evaluation before it. */
	triageMs?: number;
	evaluationMs?: number;
	inferenceMs?: number;
	inputTokens?: number;
	outputTokens?: number;
	model?: string;
	accelerator?: string;
}

function positive(n: number | undefined): number | undefined {
	return typeof n === 'number' && n > 0 ? n : undefined;
}

/** The model the call ran on, else the model the sender is configured with. */
function modelOf(meta: NonNullable<DiscussionMessage['metadata']>): string | undefined {
	return meta.model || meta.modelName || undefined;
}

/** What a message's own metadata says about how it was made. */
export function metadataStats(msg: DiscussionMessage): MessageStats {
	const meta = msg.metadata ?? {};
	return {
		inferenceMs: positive(meta.inferenceMs),
		inputTokens: positive(meta.inputTokens),
		outputTokens: positive(meta.outputTokens),
		model: modelOf(meta),
		accelerator: placementOf(meta.provider, meta.gpuLabel)
	};
}

/**
 * An agent's current round: when its open triage or evaluation began, in epoch
 * milliseconds, and the triage it already finished.
 */
interface AgentSteps {
	triageAt?: number;
	evalAt?: number;
	/** The triage this round already closed, kept for the signal that ends the round. */
	triageMs?: number;
}

/** The time from began to end, when both are known and end is not before began. */
function stepMs(began: number | undefined, end: number): number | undefined {
	if (began === undefined || !Number.isFinite(end) || end < began) return undefined;
	return end - began;
}

function startTriage(steps: AgentSteps, at: number) {
	steps.triageAt = finite(at);
	steps.evalAt = undefined;
	steps.triageMs = undefined;
}

function startEvaluation(steps: AgentSteps, at: number) {
	steps.triageMs = stepMs(steps.triageAt, at);
	steps.triageAt = undefined;
	steps.evalAt = finite(at);
}

/** Closes the round at the agent's signal: the steps it took, and a fresh round. */
function closeRound(
	steps: AgentSteps,
	at: number
): Pick<MessageStats, 'triageMs' | 'evaluationMs'> {
	const triageMs = steps.triageMs ?? stepMs(steps.triageAt, at);
	const evaluationMs = stepMs(steps.evalAt, at);
	steps.triageAt = undefined;
	steps.evalAt = undefined;
	steps.triageMs = undefined;
	return { triageMs, evaluationMs };
}

type StepHandler = (steps: AgentSteps, at: number) => void;

const STEP_STARTS: Readonly<Record<string, StepHandler>> = {
	triaging: startTriage,
	evaluating: startEvaluation
};

function stepsOf(byAgent: Map<string, AgentSteps>, agent: string): AgentSteps {
	let steps = byAgent.get(agent);
	if (!steps) {
		steps = {};
		byAgent.set(agent, steps);
	}
	return steps;
}

/**
 * Stats for every message of a thread, in message order: its time since the thread
 * started, what its metadata says, and, on a contribution, the agent's triage and
 * evaluation before it.
 */
export function messageStats(messages: DiscussionMessage[]): Map<DiscussionMessage, MessageStats> {
	const t0 = messages.length > 0 ? threadStartMs(messages) : Number.NaN;
	const byAgent = new Map<string, AgentSteps>();
	const stats = new Map<DiscussionMessage, MessageStats>();
	for (const msg of messages) {
		const at = messageTimeMs(msg);
		const entry: MessageStats = {
			elapsedMs: finite(at - t0),
			...metadataStats(msg)
		};
		const start = STEP_STARTS[msg.messageType];
		if (start) start(stepsOf(byAgent, msg.agentName), at);
		if (isContribution(msg)) Object.assign(entry, closeRound(stepsOf(byAgent, msg.agentName), at));
		stats.set(msg, entry);
	}
	return stats;
}

/** How a thread was answered, from what its messages carry. */
export interface ThreadSummary {
	startedAt?: string;
	/** The answer's time, else the close's, when the thread has one. */
	endedAt?: string;
	endedBy?: 'answer' | 'close';
	totalMs?: number;
	firstSignalMs?: number;
	/** The agents the coordinator convened; absent when every agent could take part. */
	convened?: string[];
	/** The agents that sent a contribution, in the order they first did. */
	participants: string[];
	stoodAside: string[];
	/** Each agent's latest signal, counted by kind, in display order. */
	signals: Array<{ kind: string; count: number }>;
	/** Tokens of the agents' contributions; undefined when none carried any. */
	inputTokens?: number;
	outputTokens?: number;
	/** The coordinator's own tokens, from thread_close. */
	coordinatorInputTokens?: number;
	coordinatorOutputTokens?: number;
	models: string[];
	accelerators: string[];
}

function lastOfType(messages: DiscussionMessage[], type: string): DiscussionMessage | undefined {
	return messages.findLast((m) => m.messageType === type);
}

/** The thread's end: its last answer, else its close. */
function threadEnd(messages: DiscussionMessage[]): Pick<ThreadSummary, 'endedAt' | 'endedBy'> {
	const answer = lastOfType(messages, 'synthesis');
	if (answer?.timestamp) return { endedAt: answer.timestamp, endedBy: 'answer' };
	const close = lastOfType(messages, 'thread_close');
	if (close?.timestamp) return { endedAt: close.timestamp, endedBy: 'close' };
	return {};
}

/** The inner circle of the coordinator's latest advisory_ready that names one. */
function convenedAgents(messages: DiscussionMessage[]): string[] | undefined {
	const ready = messages.findLast(
		(m) => m.messageType === 'advisory_ready' && (m.metadata?.innerCircle?.length ?? 0) > 0
	);
	return ready?.metadata?.innerCircle;
}

function addOptional(sum: number | undefined, n: number | undefined): number | undefined {
	return n === undefined ? sum : (sum ?? 0) + n;
}

function compareSignalKinds(a: string, b: string): number {
	const ia = SIGNAL_ORDER.indexOf(a);
	const ib = SIGNAL_ORDER.indexOf(b);
	if (ia !== ib)
		return (ia === -1 ? SIGNAL_ORDER.length : ia) - (ib === -1 ? SIGNAL_ORDER.length : ib);
	return a.localeCompare(b);
}

function countSignals(latest: Map<string, string>): ThreadSummary['signals'] {
	const counts = new Map<string, number>();
	for (const kind of latest.values()) counts.set(kind, (counts.get(kind) ?? 0) + 1);
	return [...counts.keys()]
		.sort(compareSignalKinds)
		.map((kind) => ({ kind, count: counts.get(kind) ?? 0 }));
}

function sortedSet(values: Iterable<string | undefined>): string[] {
	return [...new Set([...values].filter((v): v is string => !!v))].sort((a, b) =>
		a.localeCompare(b)
	);
}

/** The coordinator's tokens summed over the steps thread_close reports. */
function coordinatorTokens(
	messages: DiscussionMessage[]
): Pick<ThreadSummary, 'coordinatorInputTokens' | 'coordinatorOutputTokens'> {
	const totals = lastOfType(messages, 'thread_close')?.metadata?.totalTokens;
	if (!totals || typeof totals !== 'object') return {};
	const steps = Object.values(totals);
	return {
		coordinatorInputTokens: positive(steps.reduce((sum, s) => sum + (s?.in ?? 0), 0)),
		coordinatorOutputTokens: positive(steps.reduce((sum, s) => sum + (s?.out ?? 0), 0))
	};
}

interface ContributionTally {
	latest: Map<string, string>;
	firstAt?: number;
	inputTokens?: number;
	outputTokens?: number;
}

function tallyContributions(
	contributions: DiscussionMessage[],
	stats: Map<DiscussionMessage, MessageStats>
): ContributionTally {
	const tally: ContributionTally = { latest: new Map() };
	for (const msg of contributions) {
		const s = stats.get(msg);
		tally.latest.set(msg.agentName, signalKind(msg.messageType));
		if (tally.firstAt === undefined) tally.firstAt = s?.elapsedMs;
		tally.inputTokens = addOptional(tally.inputTokens, s?.inputTokens);
		tally.outputTokens = addOptional(tally.outputTokens, s?.outputTokens);
	}
	return tally;
}

/** The thread's summary: times, who took part, signals, tokens, models and accelerators. */
export function summarizeThread(
	messages: DiscussionMessage[],
	stats: Map<DiscussionMessage, MessageStats> = messageStats(messages)
): ThreadSummary {
	const start = messages.find((m) => m.messageType === 'thread_start') ?? messages[0];
	const end = threadEnd(messages);
	const contributions = messages.filter(isContribution);
	const tally = tallyContributions(contributions, stats);
	const agentStats = contributions.map((m) => stats.get(m));
	return {
		startedAt: start?.timestamp,
		...end,
		totalMs: end.endedAt ? finite(Date.parse(end.endedAt) - threadStartMs(messages)) : undefined,
		firstSignalMs: tally.firstAt,
		convened: convenedAgents(messages),
		participants: [...tally.latest.keys()],
		stoodAside: [...tally.latest]
			.filter(([, kind]) => kind === 'stand_aside')
			.map(([agent]) => agent),
		signals: countSignals(tally.latest),
		inputTokens: tally.inputTokens,
		outputTokens: tally.outputTokens,
		...coordinatorTokens(messages),
		models: sortedSet(agentStats.map((s) => s?.model)),
		accelerators: sortedSet(agentStats.map((s) => s?.accelerator))
	};
}
