import { describe, expect, it } from 'vitest';
import type { DiscussionMessage } from '$types/kubemoot.js';
import {
	FALLBACK_SIGNAL_COLOR,
	SIGNAL_COLORS,
	buildAgentSpans,
	primarySignal,
	queueDepth,
	signalColor,
	type AgentSpan
} from './discussion-spans';

const T0 = Date.parse('2026-09-30T12:00:00Z');

function msg(
	agentName: string,
	messageType: string,
	atMs: number,
	metadata?: DiscussionMessage['metadata']
): DiscussionMessage {
	return {
		messageId: `${agentName}-${atMs}`,
		threadId: 't1',
		agentName,
		messageType,
		content: '',
		channel: 'general',
		timestamp: new Date(T0 + atMs).toISOString(),
		metadata
	} as DiscussionMessage;
}

describe('signalColor', () => {
	it('uses the signal color and falls back for unknown signals', () => {
		expect(signalColor('block')).toBe(SIGNAL_COLORS.block);
		expect(signalColor('unheard_of')).toBe(FALLBACK_SIGNAL_COLOR);
	});
});

describe('primarySignal', () => {
	it('picks the highest-priority signal, the first on a tie', () => {
		expect(
			primarySignal([msg('a', 'heartbeat', 0), msg('a', 'agree', 1), msg('a', 'concern', 2)])
		).toBe('concern');
		expect(primarySignal([msg('a', 'agree', 0), msg('a', 'contribution', 1)])).toBe('agree');
	});

	it('treats unknown signals as priority 0 and defaults to thread_start when empty', () => {
		expect(primarySignal([msg('a', 'mystery', 0)])).toBe('mystery');
		expect(primarySignal([])).toBe('thread_start');
	});
});

describe('buildAgentSpans', () => {
	it('returns no spans without messages', () => {
		expect(buildAgentSpans([])).toEqual([]);
	});

	it('builds one span per agent from the thread start, human first then by end time', () => {
		const spans = buildAgentSpans([
			msg('coordinator', 'heartbeat', -5000),
			msg('human', 'thread_start', 0),
			msg('homelab-sre', 'triaging', 1000),
			msg('homelab-sre', 'agree', 4000, {
				toolsUsed: ['pods_list', 'nodes_top'],
				gpuLabel: 'ollama-gpu',
				inferenceMs: 2500,
				inferenceStartMs: 1500,
				loadDurationMs: 300,
				pickReason: 'warm, slot 1/2'
			}),
			msg('homelab-sre', 'agree', 4500, { toolsUsed: ['pods_list'], inferenceMs: 900 }),
			msg('coordinator', 'synthesis', 6000)
		]);

		expect(spans.map((s) => s.agent)).toEqual(['human', 'homelab-sre', 'coordinator']);
		const [human, sre, coordinator] = spans;
		expect(human).toMatchObject({ displayName: 'You', isHuman: true, startMs: 0, endMs: 0 });
		expect(sre).toMatchObject({
			displayName: 'sre',
			startMs: 1000,
			endMs: 4500,
			primarySignal: 'agree',
			color: SIGNAL_COLORS.agree,
			toolsUsed: ['pods_list', 'nodes_top'],
			gpuLabel: 'ollama-gpu',
			pickReason: 'warm, slot 1/2',
			inferenceMs: 2500,
			inferenceStartMs: 1500,
			loadDurationMs: 300,
			queueDepth: 0
		});
		expect(sre.signalMarkers).toEqual([
			{ ms: 1000, type: 'triaging', color: SIGNAL_COLORS.triaging }
		]);
		expect(coordinator).toMatchObject({ startMs: -5000, endMs: 6000, primarySignal: 'synthesis' });
		expect(coordinator.signalMarkers.map((m) => m.type)).toEqual(['heartbeat']);
	});

	it('measures from the first message when there is no thread_start', () => {
		const spans = buildAgentSpans([msg('a', 'agree', 2000), msg('b', 'agree', 3000)]);
		expect(spans.map((s) => [s.agent, s.startMs])).toEqual([
			['a', 0],
			['b', 1000]
		]);
	});

	it('counts overlapping inference on the same GPU as queue depth', () => {
		const spans = buildAgentSpans([
			msg('a', 'agree', 0, { gpuLabel: 'g1', inferenceStartMs: 100, inferenceMs: 1000 }),
			msg('b', 'agree', 1, { gpuLabel: 'g1', inferenceStartMs: 500, inferenceMs: 1000 }),
			msg('c', 'agree', 2, { gpuLabel: 'g1', inferenceStartMs: 1100, inferenceMs: 1000 }),
			msg('d', 'agree', 3, { gpuLabel: 'g2', inferenceStartMs: 0, inferenceMs: 5000 })
		]);
		const depth = Object.fromEntries(spans.map((s) => [s.agent, s.queueDepth]));
		expect(depth).toEqual({ a: 1, b: 2, c: 1, d: 0 });
	});
});

describe('queueDepth', () => {
	it('is 0 for a span without a GPU or timed inference', () => {
		const bare = { gpuLabel: undefined, inferenceStartMs: 5, inferenceMs: 5 } as AgentSpan;
		const untimed = { gpuLabel: 'g1', inferenceStartMs: undefined, inferenceMs: 5 } as AgentSpan;
		const other = { gpuLabel: 'g1', inferenceStartMs: 0, inferenceMs: 100 } as AgentSpan;
		expect(queueDepth(bare, [bare, other])).toBe(0);
		expect(queueDepth(untimed, [untimed, other])).toBe(0);
	});
});
