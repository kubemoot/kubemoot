import { describe, expect, it } from 'vitest';
import type { DiscussionMessage } from '#lib/types/kubemoot.js';
import {
	CONTRIBUTION_SIGNALS,
	LEGACY_SIGNALS,
	formatCount,
	formatTimestamp,
	isContribution,
	messageStats,
	metadataStats,
	summarizeThread
} from './discussion-timing';
import { SIGNAL_COLORS, SIGNAL_PRIORITY, placementOf } from './discussion-spans';

const T0 = Date.parse('2026-10-15T12:00:00Z');

function msg(
	agentName: string,
	messageType: string,
	atMs: number | undefined,
	metadata?: DiscussionMessage['metadata']
): DiscussionMessage {
	return {
		messageId: `${agentName}-${messageType}-${atMs}`,
		threadId: 't1',
		agentName,
		messageType,
		timestamp: atMs === undefined ? undefined : new Date(T0 + atMs).toISOString(),
		metadata
	} as DiscussionMessage;
}

// A recorded thread: the coordinator convenes sre and net; net stands aside from
// triage, sre triages, evaluates, and agrees; the coordinator answers and closes.
function recordedThread(): DiscussionMessage[] {
	return [
		msg('human', 'thread_start', 0, {
			userQuery: 'Describe the proxmox nodes.'
		}),
		msg('coordinator', 'advisory_ready', 2000, {
			innerCircle: ['sre', 'net'],
			modelName: 'qwen3:8b'
		}),
		msg('sre', 'triaging', 3000),
		msg('net', 'triaging', 3500),
		msg('sre', 'evaluating', 5000),
		msg('net', 'stand_aside', 6000, {
			reason: 'not relevant',
			modelName: 'qwen3:8b',
			inputTokens: 100,
			outputTokens: 5,
			gpuLabel: 'none'
		}),
		msg('sre', 'agree', 47_000, {
			inferenceMs: 40_000,
			inputTokens: 12_345,
			outputTokens: 678,
			model: 'qwen3:32b',
			modelName: 'qwen3:8b',
			provider: 'ollama-rig1',
			gpuLabel: 'gpu-0'
		}),
		msg('coordinator', 'synthesis', 72_000, {
			inputTokens: 900,
			outputTokens: 300
		}),
		msg('coordinator', 'thread_close', 73_000, {
			totalTokens: {
				advisory: { in: 10, out: 2 },
				synthesis: { in: 30, out: 8 }
			}
		})
	];
}

describe('formatTimestamp and formatCount', () => {
	it('formats a valid time and leaves a missing or invalid one empty', () => {
		expect(formatTimestamp('2026-10-15T12:00:00Z')).not.toBe('');
		expect(formatTimestamp(undefined)).toBe('');
		expect(formatTimestamp('not a time')).toBe('');
	});

	it('groups thousands', () => {
		expect(formatCount(12_345)).toBe('12,345');
	});
});

describe('signal tables', () => {
	it('ranks every contribution signal but failure in the span graph, and colors them all', () => {
		for (const signal of CONTRIBUTION_SIGNALS) {
			if (signal !== 'failure') expect(SIGNAL_PRIORITY[signal]).toBeGreaterThan(0);
		}
	});

	it('gives each legacy alias the rank and color of the signal it stands for', () => {
		for (const [alias, signal] of Object.entries(LEGACY_SIGNALS)) {
			expect(CONTRIBUTION_SIGNALS.has(alias)).toBe(true);
			expect(SIGNAL_PRIORITY[alias]).toBe(SIGNAL_PRIORITY[signal]);
			expect(SIGNAL_COLORS[alias]).toBe(SIGNAL_COLORS[signal]);
		}
	});
});

describe('placementOf', () => {
	it('prefers the provider, falls back to the accelerator label, and drops none', () => {
		expect(placementOf('ollama-rig1', 'gpu-0')).toBe('ollama-rig1');
		expect(placementOf('', 'gpu-0')).toBe('gpu-0');
		expect(placementOf('none', 'gpu-0')).toBe('gpu-0');
		expect(placementOf(undefined, 'none')).toBeUndefined();
		expect(placementOf()).toBeUndefined();
	});
});

describe('isContribution', () => {
	it('is true for terminal signals, including legacy aliases', () => {
		for (const type of [
			'agree',
			'concern',
			'block',
			'stand_aside',
			'failure',
			'contribution',
			'decline'
		]) {
			expect(isContribution(msg('a', type, 0))).toBe(true);
		}
	});

	it('is false for phases and coordinator messages', () => {
		for (const type of ['triaging', 'evaluating', 'synthesis', 'advisory_ready', 'thread_start']) {
			expect(isContribution(msg('a', type, 0))).toBe(false);
		}
	});
});

describe('metadataStats', () => {
	it('prefers the model the call ran on and the provider that served it', () => {
		const stats = metadataStats(
			msg('sre', 'agree', 0, {
				model: 'big',
				modelName: 'small',
				provider: 'rig1',
				gpuLabel: 'gpu-0'
			})
		);
		expect(stats).toMatchObject({ model: 'big', accelerator: 'rig1' });
	});

	it('falls back to the configured model and the reported accelerator', () => {
		const stats = metadataStats(msg('sre', 'agree', 0, { modelName: 'small', gpuLabel: 'gpu-0' }));
		expect(stats).toMatchObject({ model: 'small', accelerator: 'gpu-0' });
	});

	it('leaves out zero counts, an empty model, and an accelerator of none', () => {
		const stats = metadataStats(
			msg('sre', 'agree', 0, {
				inputTokens: 0,
				outputTokens: 0,
				inferenceMs: 0,
				model: '',
				gpuLabel: 'none'
			})
		);
		expect(stats).toEqual({
			inferenceMs: undefined,
			inputTokens: undefined,
			outputTokens: undefined,
			model: undefined,
			accelerator: undefined
		});
	});
});

describe('messageStats', () => {
	it("derives elapsed time and each contribution's triage and evaluation from a recorded thread", () => {
		const messages = recordedThread();
		const stats = messageStats(messages);
		const byType = (agent: string, type: string) =>
			stats.get(messages.find((m) => m.agentName === agent && m.messageType === type)!);

		expect(byType('human', 'thread_start')?.elapsedMs).toBe(0);
		expect(byType('sre', 'agree')).toEqual({
			elapsedMs: 47_000,
			triageMs: 2_000,
			evaluationMs: 42_000,
			inferenceMs: 40_000,
			inputTokens: 12_345,
			outputTokens: 678,
			model: 'qwen3:32b',
			accelerator: 'ollama-rig1'
		});
		// net stood aside from triage: its triage ends at the signal and it never evaluated.
		expect(byType('net', 'stand_aside')).toMatchObject({
			triageMs: 2_500,
			evaluationMs: undefined
		});
		// Phase and coordinator messages carry no step durations.
		expect(byType('sre', 'evaluating')?.triageMs).toBeUndefined();
		expect(byType('coordinator', 'synthesis')).toMatchObject({
			elapsedMs: 72_000,
			inputTokens: 900
		});
		expect(byType('coordinator', 'synthesis')).not.toHaveProperty('triageMs');
	});

	it('starts a new round when an agent triages again', () => {
		const messages = [
			msg('human', 'thread_start', 0),
			msg('sre', 'triaging', 1_000),
			msg('sre', 'evaluating', 2_000),
			msg('sre', 'agree', 5_000),
			msg('sre', 'triaging', 10_000),
			msg('sre', 'concern', 14_000)
		];
		const stats = messageStats(messages);
		expect(stats.get(messages[3])).toMatchObject({
			triageMs: 1_000,
			evaluationMs: 3_000
		});
		expect(stats.get(messages[5])).toMatchObject({
			triageMs: 4_000,
			evaluationMs: undefined
		});
	});

	it('times evaluation alone when the triage start was not seen', () => {
		const messages = [
			msg('human', 'thread_start', 0),
			msg('sre', 'evaluating', 2_000),
			msg('sre', 'agree', 9_000)
		];
		expect(messageStats(messages).get(messages[2])).toMatchObject({
			triageMs: undefined,
			evaluationMs: 7_000
		});
	});

	it('leaves a step untimed when its end comes before its start or lacks a time', () => {
		const messages = [
			msg('human', 'thread_start', 0),
			msg('sre', 'triaging', 9_000),
			msg('sre', 'agree', 4_000),
			msg('net', 'triaging', 1_000),
			msg('net', 'agree', undefined)
		];
		const stats = messageStats(messages);
		expect(stats.get(messages[2])).toMatchObject({
			triageMs: undefined,
			evaluationMs: undefined
		});
		expect(stats.get(messages[4])).toMatchObject({
			elapsedMs: undefined,
			triageMs: undefined
		});
	});

	it('renders an old thread without timestamps or metadata as empty stats', () => {
		const messages = [
			msg('human', 'thread_start', undefined),
			msg('sre', 'contribution', undefined)
		];
		const stats = messageStats(messages);
		expect(stats.get(messages[1])).toEqual({
			elapsedMs: undefined,
			triageMs: undefined,
			evaluationMs: undefined,
			inferenceMs: undefined,
			inputTokens: undefined,
			outputTokens: undefined,
			model: undefined,
			accelerator: undefined
		});
	});

	it('is empty for a thread with no messages', () => {
		expect(messageStats([]).size).toBe(0);
	});
});

describe('summarizeThread', () => {
	it('totals a recorded thread', () => {
		expect(summarizeThread(recordedThread())).toEqual({
			startedAt: '2026-10-15T12:00:00.000Z',
			endedAt: '2026-10-15T12:01:12.000Z',
			endedBy: 'answer',
			totalMs: 72_000,
			firstSignalMs: 6_000,
			convened: ['sre', 'net'],
			participants: ['net', 'sre'],
			stoodAside: ['net'],
			signals: [
				{ kind: 'agree', count: 1 },
				{ kind: 'stand_aside', count: 1 }
			],
			inputTokens: 12_445,
			outputTokens: 683,
			coordinatorInputTokens: 40,
			coordinatorOutputTokens: 10,
			models: ['qwen3:32b', 'qwen3:8b'],
			accelerators: ['ollama-rig1']
		});
	});

	it('counts each agent once by its latest signal, legacy aliases included', () => {
		const summary = summarizeThread([
			msg('human', 'thread_start', 0),
			msg('a', 'contribution', 1_000),
			msg('b', 'decline', 2_000),
			msg('a', 'concern', 3_000),
			msg('c', 'failure', 4_000),
			msg('d', 'zeta', 5_000),
			msg('e', 'block', 6_000)
		]);
		expect(summary.signals).toEqual([
			{ kind: 'concern', count: 1 },
			{ kind: 'block', count: 1 },
			{ kind: 'failure', count: 1 },
			{ kind: 'stand_aside', count: 1 }
		]);
		expect(summary.stoodAside).toEqual(['b']);
		expect(summary.participants).toEqual(['a', 'b', 'c', 'e']);
	});

	it('ends at the close when the thread has no answer', () => {
		const summary = summarizeThread([
			msg('human', 'thread_start', 0),
			msg('coordinator', 'thread_close', 30_000)
		]);
		expect(summary).toMatchObject({ endedBy: 'close', totalMs: 30_000 });
	});

	it('leaves out what an old or open thread does not carry', () => {
		const summary = summarizeThread([msg('human', 'thread_start', 0), msg('sre', 'agree', 5_000)]);
		expect(summary).toMatchObject({
			totalMs: undefined,
			convened: undefined,
			inputTokens: undefined,
			outputTokens: undefined,
			models: [],
			accelerators: []
		});
		expect(summary.endedAt).toBeUndefined();
		expect(summary.coordinatorInputTokens).toBeUndefined();
		expect(summary.firstSignalMs).toBe(5_000);
	});

	it('ignores a thread_close whose totals are a number', () => {
		const summary = summarizeThread([
			msg('human', 'thread_start', 0),
			msg('coordinator', 'thread_close', 1_000, { totalTokens: 42 })
		]);
		expect(summary.coordinatorInputTokens).toBeUndefined();
	});

	it('summarizes an empty thread without failing', () => {
		expect(summarizeThread([])).toMatchObject({
			participants: [],
			signals: [],
			startedAt: undefined
		});
	});
});
