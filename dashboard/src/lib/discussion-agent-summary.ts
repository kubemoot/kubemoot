// Per-agent aggregation behind the "Agent Summary" table of a copied discussion.

import type { DiscussionMessage } from '#lib/types/kubemoot.js';
import { SIGNAL_PRIORITY } from '#lib/discussion-spans.js';

export interface AgentAgg {
	signal: string;
	gpu: string;
	provider: string;
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
		gpu: meta.gpuLabel || '',
		provider: meta.provider || '',
		// FitPredictor v2 reasoning ("warm, slot 1/2 | SR=0.92 ..."), shown as a hover
		// tooltip on the GPU/provider column rather than a wider table.
		pickReason: meta.pickReason || '',
		startMs: ts,
		endMs: ts,
		inferenceMs: meta.inferenceMs || 0,
		tools: new Set(meta.toolsUsed || [])
	};
}

/** Folds a later message's placement, timing, and tools into the agent's row. */
function mergeMetadata(existing: AgentAgg, meta: NonNullable<DiscussionMessage['metadata']>) {
	if (meta.gpuLabel) existing.gpu = meta.gpuLabel;
	if (meta.provider) existing.provider = meta.provider;
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

// `provider` (e.g. "ollama-gpu", "ollama-rig1") is the JIT-selected ModelProvider
// name from the agent runtime's ProviderSelector: which provider this agent used for
// this inference. The summary falls back to the static `gpuLabel` when a message
// lacks it (older messages and stand-aside paths without inference).
export function aggregateAgents(messages: DiscussionMessage[], t0: number): Map<string, AgentAgg> {
	const agentMap = new Map<string, AgentAgg>();
	for (const msg of messages) {
		const ts = new Date(msg.timestamp).getTime() - t0;
		const existing = agentMap.get(msg.agentName);
		if (existing) {
			mergeAgentMessage(existing, msg, ts);
		} else {
			agentMap.set(msg.agentName, newAgentAgg(msg, ts));
		}
	}
	return agentMap;
}
