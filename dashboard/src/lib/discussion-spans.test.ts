import { describe, expect, it } from 'vitest';
import type { DiscussionMessage } from '#lib/types/kubemoot.js';
import {
	FALLBACK_SIGNAL_COLOR,
	SIGNAL_COLORS,
	buildAgentSpans,
	messageTimeMs,
	primarySignal,
	queueDepth,
	signalColor,
	threadStartMs,
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

	it('reads the wait for the first token, and whether it included a model load', () => {
		const [, warm] = buildAgentSpans([
			msg('human', 'thread_start', 0),
			msg('advisor', 'advisory', 2000, { inferenceMs: 1800, firstTokenMs: 400, coldStart: false })
		]);
		expect(warm).toMatchObject({ firstTokenMs: 400, coldStart: false });
		const [, cold] = buildAgentSpans([
			msg('human', 'thread_start', 0),
			msg('advisor', 'advisory', 9000, { inferenceMs: 8000, firstTokenMs: 6200, coldStart: true })
		]);
		expect(cold).toMatchObject({ firstTokenMs: 6200, coldStart: true });
		const [, none] = buildAgentSpans([msg('human', 'thread_start', 0), msg('advisor', 'advisory', 900)]);
		expect(none).toMatchObject({ firstTokenMs: 0, coldStart: false });
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
			// An older message carries only a load time: the wait was a load.
			firstTokenMs: 300,
			coldStart: true,
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

describe('messageTimeMs', () => {
	it('reads the message timestamp', () => {
		expect(messageTimeMs(msg('a', 'agree', 1500))).toBe(T0 + 1500);
	});

	it('is NaN without a usable timestamp', () => {
		const untimed = { ...msg('a', 'agree', 0), timestamp: undefined };
		expect(messageTimeMs(untimed)).toBeNaN();
		expect(messageTimeMs({ ...untimed, timestamp: '' })).toBeNaN();
		expect(messageTimeMs({ ...untimed, timestamp: 'yesterday' })).toBeNaN();
	});
});

describe('threadStartMs', () => {
	it('is the thread_start, else the first message', () => {
		const later = msg('a', 'agree', 2000);
		expect(threadStartMs([later, msg('coordinator', 'thread_start', 5000)])).toBe(T0 + 5000);
		expect(threadStartMs([later, msg('b', 'agree', 1000)])).toBe(T0 + 2000);
	});
});

describe('buildAgentSpans placement and model', () => {
	it('names the server and model an agent span ran on', () => {
		const [span] = buildAgentSpans([
			msg('k8s', 'agree', 1000, { server: 'vllm-rig0', model: 'qwen3-32b' })
		]);
		expect(span.gpuLabel).toBe('vllm-rig0');
		expect(span.model).toBe('qwen3-32b');
	});

	it('falls back to the accelerator label when no server is reported', () => {
		const [span] = buildAgentSpans([msg('k8s', 'agree', 1000, { gpuLabel: 'ollama-rig1' })]);
		expect(span.gpuLabel).toBe('ollama-rig1');
		expect(span.model).toBeUndefined();
	});

	it('shows no placement for a call that ran on none', () => {
		const [span] = buildAgentSpans([msg('k8s', 'agree', 1000, { gpuLabel: 'none' })]);
		expect(span.gpuLabel).toBeUndefined();
	});
});

describe('buildAgentSpans without signal timings', () => {
	const untimed = (agent: string, type: string): DiscussionMessage => ({
		...msg(agent, type, 0),
		timestamp: undefined
	});

	it('draws known spans and flags the rest, with no NaN', () => {
		const spans = buildAgentSpans([
			untimed('human', 'thread_start'),
			msg('coordinator', 'agree', 2000),
			msg('coordinator', 'synthesis', 5000),
			untimed('smoke-agent', 'agree')
		]);
		const coordinator = spans.find((s) => s.agent === 'coordinator')!;
		expect(coordinator.timed).toBe(true);
		expect(coordinator.endMs - coordinator.startMs).toBe(3000);
		const smoke = spans.find((s) => s.agent === 'smoke-agent')!;
		expect(smoke.timed).toBe(false);
		for (const s of spans) {
			expect(Number.isNaN(s.startMs)).toBe(false);
			expect(Number.isNaN(s.endMs)).toBe(false);
			for (const m of s.signalMarkers) expect(Number.isNaN(m.ms)).toBe(false);
		}
	});

	it('marks every span untimed when no message has a timestamp', () => {
		const spans = buildAgentSpans([untimed('human', 'thread_start'), untimed('coordinator', 'synthesis')]);
		expect(spans.map((s) => s.timed)).toEqual([false, false]);
		expect(spans.every((s) => s.startMs === 0 && s.endMs === 0)).toBe(true);
	});

	it('falls back to the earliest known timestamp for the thread start', () => {
		expect(threadStartMs([untimed('human', 'thread_start'), msg('a', 'agree', 4000)])).toBe(T0 + 4000);
	});
});
