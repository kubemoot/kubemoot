// Builds the per-agent spans of a discussion timeline (DiscussionSpanGraph): one
// bar per agent from its first to its last message, colored by its strongest
// signal, with GPU placement, inference timing, and GPU queue depth.

import type { DiscussionMessage } from '$types/kubemoot.js';

type MessageMeta = NonNullable<DiscussionMessage['metadata']>;

export const FALLBACK_SIGNAL_COLOR = '#6b7280';

export const SIGNAL_COLORS: Record<string, string> = {
	synthesis: '#fbbf24',
	block: '#f87171',
	concern: '#fbbf24',
	agree: '#34d399',
	contribution: '#34d399',
	advisory: '#c084fc',
	proposal: '#60a5fa',
	stand_aside: '#6b7280',
	decline: '#6b7280',
	thread_start: '#3b82f6',
	follow_up: '#f59e0b',
	reply: '#34d399',
	consent: '#34d399',
	gap_detected: '#f87171',
	thread_close: '#9ca3af',
	advisory_ready: '#38bdf8',
	review_ready: '#fbbf24',
	triaging: '#38bdf8',
	evaluating: '#818cf8',
	heartbeat: '#64748b',
	waking: '#fb923c',
	ready: '#22d3ee'
};

// Which signal colors an agent's bar when it sent several: the highest wins.
export const SIGNAL_PRIORITY: Readonly<Record<string, number>> = {
	synthesis: 10,
	block: 9,
	concern: 8,
	agree: 7,
	contribution: 7,
	advisory: 6,
	proposal: 5,
	follow_up: 4,
	reply: 4,
	consent: 3,
	stand_aside: 2,
	decline: 2,
	gap_detected: 1,
	thread_close: 1,
	advisory_ready: 1,
	review_ready: 1,
	heartbeat: 0,
	triaging: 0,
	evaluating: 0,
	waking: 0,
	ready: 0,
	thread_start: 0
};

// Signal types that get visible markers on the bar when they differ from the primary signal.
const MARKER_SIGNALS = new Set([
	'advisory',
	'triaging',
	'evaluating',
	'heartbeat',
	'concern',
	'advisory_ready',
	'review_ready',
	'waking',
	'ready'
]);

export interface SignalMarker {
	ms: number;
	type: string;
	color: string;
}

export interface AgentSpan {
	agent: string;
	displayName: string;
	startMs: number;
	endMs: number;
	primarySignal: string;
	color: string;
	isHuman: boolean;
	toolsUsed: string[];
	messages: DiscussionMessage[];
	gpuLabel?: string;
	/** FitPredictor v2 per-call reasoning ("warm, slot 1/2 | SR=0.92 ..."). */
	pickReason?: string;
	inferenceMs: number;
	inferenceStartMs?: number;
	loadDurationMs: number;
	queueDepth: number;
	signalMarkers: SignalMarker[];
}

interface AgentMessages {
	msgs: DiscussionMessage[];
	minMs: number;
	maxMs: number;
}

export function signalColor(type: string): string {
	return SIGNAL_COLORS[type] || FALLBACK_SIGNAL_COLOR;
}

/** Milliseconds since the thread started (its thread_start, else its first message). */
function threadStartMs(messages: DiscussionMessage[]): number {
	const start = messages.find((m) => m.messageType === 'thread_start') ?? messages[0];
	return new Date(start.timestamp).getTime();
}

function groupByAgent(messages: DiscussionMessage[], t0: number): Map<string, AgentMessages> {
	const byAgent = new Map<string, AgentMessages>();
	for (const msg of messages) {
		const ts = new Date(msg.timestamp).getTime() - t0;
		const existing = byAgent.get(msg.agentName);
		if (existing) {
			existing.msgs.push(msg);
			existing.minMs = Math.min(existing.minMs, ts);
			existing.maxMs = Math.max(existing.maxMs, ts);
		} else {
			byAgent.set(msg.agentName, { msgs: [msg], minMs: ts, maxMs: ts });
		}
	}
	return byAgent;
}

/** The highest-priority signal among the messages; the first one wins a tie. */
export function primarySignal(msgs: DiscussionMessage[]): string {
	let best = 'thread_start';
	let bestPriority = -1;
	for (const m of msgs) {
		const p = SIGNAL_PRIORITY[m.messageType] ?? 0;
		if (p > bestPriority) {
			bestPriority = p;
			best = m.messageType;
		}
	}
	return best;
}

/** The last set value of a metadata field across the messages. */
function lastMeta<K extends 'gpuLabel' | 'pickReason'>(
	msgs: DiscussionMessage[],
	key: K
): MessageMeta[K] | undefined {
	let value: MessageMeta[K] | undefined;
	for (const m of msgs) {
		const v = m.metadata?.[key];
		if (v) value = v;
	}
	return value;
}

/** The largest positive value of a numeric metadata field, 0 when none is set. */
function maxMeta(msgs: DiscussionMessage[], key: 'inferenceMs' | 'loadDurationMs'): number {
	let max = 0;
	for (const m of msgs) {
		const v = m.metadata?.[key];
		if (v && v > max) max = v;
	}
	return max;
}

function firstInferenceStart(msgs: DiscussionMessage[]): number | undefined {
	return msgs.find((m) => m.metadata?.inferenceStartMs)?.metadata?.inferenceStartMs;
}

function toolsUsed(msgs: DiscussionMessage[]): string[] {
	return Array.from(new Set(msgs.flatMap((m) => m.metadata?.toolsUsed ?? [])));
}

/** Markers for the marker signals other than the agent's primary signal. */
function signalMarkers(msgs: DiscussionMessage[], t0: number, primary: string): SignalMarker[] {
	return msgs
		.filter((m) => MARKER_SIGNALS.has(m.messageType) && m.messageType !== primary)
		.map((m) => ({
			ms: new Date(m.timestamp).getTime() - t0,
			type: m.messageType,
			color: signalColor(m.messageType)
		}));
}

function agentSpan(agent: string, data: AgentMessages, t0: number): AgentSpan {
	const primary = primarySignal(data.msgs);
	const isHuman = agent === 'human';
	return {
		agent,
		displayName: isHuman ? 'You' : agent.replace('homelab-', ''),
		startMs: data.minMs,
		endMs: data.maxMs,
		primarySignal: primary,
		color: signalColor(primary),
		isHuman,
		toolsUsed: toolsUsed(data.msgs),
		messages: data.msgs,
		gpuLabel: lastMeta(data.msgs, 'gpuLabel'),
		pickReason: lastMeta(data.msgs, 'pickReason'),
		inferenceMs: maxMeta(data.msgs, 'inferenceMs'),
		inferenceStartMs: firstInferenceStart(data.msgs),
		loadDurationMs: maxMeta(data.msgs, 'loadDurationMs'),
		queueDepth: 0,
		signalMarkers: signalMarkers(data.msgs, t0, primary)
	};
}

/** The span's inference window, or null when it has no GPU or timed inference. */
function inferenceWindow(span: AgentSpan): { start: number; end: number } | null {
	if (!span.gpuLabel || !span.inferenceStartMs || span.inferenceMs <= 0) return null;
	return { start: span.inferenceStartMs, end: span.inferenceStartMs + span.inferenceMs };
}

/** How many other spans ran inference on the same GPU while this one did. */
export function queueDepth(span: AgentSpan, all: AgentSpan[]): number {
	const mine = inferenceWindow(span);
	if (!mine) return 0;
	return all.filter((other) => {
		if (other === span || other.gpuLabel !== span.gpuLabel) return false;
		const theirs = inferenceWindow(other);
		return !!theirs && mine.start < theirs.end && mine.end > theirs.start;
	}).length;
}

/** Human first, then by when the agent's last message arrived. */
function compareSpans(a: AgentSpan, b: AgentSpan): number {
	if (a.isHuman !== b.isHuman) return a.isHuman ? -1 : 1;
	return a.endMs - b.endMs;
}

/** One span per agent that sent a message in the thread, sorted for display. */
export function buildAgentSpans(messages: DiscussionMessage[]): AgentSpan[] {
	if (messages.length === 0) return [];
	const t0 = threadStartMs(messages);
	const spans = Array.from(groupByAgent(messages, t0), ([agent, data]) =>
		agentSpan(agent, data, t0)
	);
	for (const span of spans) span.queueDepth = queueDepth(span, spans);
	return spans.sort(compareSpans);
}
