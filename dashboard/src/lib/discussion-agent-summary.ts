// Per-agent aggregation behind the "Agent Summary" table of a copied discussion.

import type { DiscussionMessage } from '#lib/types/kubemoot.js';
import { formatDuration } from '#lib/duration-format.js';
import { SIGNAL_PRIORITY, messageTimeMs, placementOfMessages, threadServers } from '#lib/discussion-spans.js';

export interface AgentAgg {
	signal: string;
	/** Where the agent's calls ran, by the rule the span graph uses; absent when unknown. */
	placement?: string;
	pickReason: string;
	startMs: number;
	endMs: number;
	inferenceMs: number;
	tools: Set<string>;
}

// Signal priority for the per-agent summary: a later, higher-priority signal
// (synthesis > block > concern > agree > advisory > stand_aside) wins as the
// agent's representative signal. The ranks come from the span graph's table; the
// summary ranks only these consensus signals, and every other signal ranks 0.
const SUMMARY_SIGNALS = new Set([
	'synthesis',
	'block',
	'concern',
	'agree',
	'advisory',
	'stand_aside'
]);

const priorityOf = (signal: string) =>
	SUMMARY_SIGNALS.has(signal) ? (SIGNAL_PRIORITY[signal] ?? 0) : 0;

export function newAgentAgg(msg: DiscussionMessage, ts: number): AgentAgg {
	const meta = msg.metadata ?? {};
	return {
		signal: msg.messageType,
		// FitPredictor v2 reasoning ("warm, slot 1/2 | SR=0.92 ..."), shown as a hover
		// tooltip on the GPU/server column rather than a wider table.
		pickReason: meta.pickReason || '',
		startMs: ts,
		endMs: ts,
		inferenceMs: meta.inferenceMs || 0,
		tools: new Set(meta.toolsUsed || [])
	};
}

/** Folds a later message's placement, timing, and tools into the agent's row. */
function mergeMetadata(existing: AgentAgg, meta: NonNullable<DiscussionMessage['metadata']>) {
	if (meta.pickReason) existing.pickReason = meta.pickReason;
	if (meta.inferenceMs && meta.inferenceMs > existing.inferenceMs)
		existing.inferenceMs = meta.inferenceMs;
	meta.toolsUsed?.forEach((t) => existing.tools.add(t));
}

export function mergeAgentMessage(existing: AgentAgg, msg: DiscussionMessage, ts: number) {
	existing.startMs = Math.min(existing.startMs, ts);
	existing.endMs = Math.max(existing.endMs, ts);
	if (priorityOf(msg.messageType) > priorityOf(existing.signal)) existing.signal = msg.messageType;
	if (msg.metadata) mergeMetadata(existing, msg.metadata);
}

// Each agent's row, with where its calls ran: the ModelServer of its last placed call,
// else its reported accelerator label when that names a server of the thread (the same
// rule as the span graph; older messages carry only the label).
export function aggregateAgents(messages: DiscussionMessage[], t0: number): Map<string, AgentAgg> {
	const agentMap = new Map<string, AgentAgg>();
	const messagesOf = new Map<string, DiscussionMessage[]>();
	for (const msg of messages) {
		const ts = messageTimeMs(msg) - t0;
		const existing = agentMap.get(msg.agentName);
		if (existing) {
			mergeAgentMessage(existing, msg, ts);
			messagesOf.get(msg.agentName)?.push(msg);
		} else {
			agentMap.set(msg.agentName, newAgentAgg(msg, ts));
			messagesOf.set(msg.agentName, [msg]);
		}
	}
	const known = threadServers(messages);
	for (const [agent, row] of agentMap) {
		row.placement = placementOfMessages(messagesOf.get(agent) ?? [], known);
	}
	return agentMap;
}

/**
 * The "Agent Summary" table of a copied discussion, one row per agent. The accelerator
 * column is where the agent's calls ran (its placement); FitPredictor's pickReason, when
 * present, rides under it as the per-call decision narrative.
 */
export function renderAgentSummary(
	agentMap: Map<string, AgentAgg>,
	agentLabel: (agent: string) => string
): string {
	let text = `## Agent Summary\n`;
	text += `| Agent | Signal | Accelerator | Duration | Inference | Tools |\n`;
	text += `|-------|--------|-----|----------|-----------|-------|\n`;
	for (const [agent, data] of agentMap) {
		const dur = formatDuration(data.endMs - data.startMs) || '-';
		const inf = data.inferenceMs > 0 ? formatDuration(data.inferenceMs) : '-';
		const tools = data.tools.size > 0 ? Array.from(data.tools).join(', ') : '-';
		const why = data.pickReason ? `<br/>_${data.pickReason}_` : '';
		text += `| ${agentLabel(agent)} | ${data.signal} | ${data.placement ?? '-'}${why} | ${dur} | ${inf} | ${tools} |\n`;
	}
	return text + `\n`;
}
