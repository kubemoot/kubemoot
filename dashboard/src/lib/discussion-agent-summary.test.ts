import { describe, expect, it } from 'vitest';
import type { DiscussionMessage } from '#lib/types/kubemoot.js';
import { aggregateAgents } from './discussion-agent-summary';

const T0 = Date.parse('2026-09-30T12:00:00Z');

function msg(
	agentName: string,
	messageType: string,
	atMs: number,
	metadata?: DiscussionMessage['metadata']
): DiscussionMessage {
	return {
		agentName,
		messageType,
		timestamp: new Date(T0 + atMs).toISOString(),
		metadata
	} as DiscussionMessage;
}

describe('aggregateAgents', () => {
	it('starts a row from the first message of each agent', () => {
		const rows = aggregateAgents(
			[msg('sre', 'advisory', 500, { gpuLabel: 'g1', inferenceMs: 40, toolsUsed: ['pods_list'] })],
			T0
		);
		expect(rows.get('sre')).toEqual({
			signal: 'advisory',
			gpu: 'g1',
			provider: '',
			pickReason: '',
			startMs: 500,
			endMs: 500,
			inferenceMs: 40,
			tools: new Set(['pods_list'])
		});
	});

	it('defaults the metadata fields when a message has none', () => {
		const row = aggregateAgents([msg('sre', 'stand_aside', 0)], T0).get('sre');
		expect(row).toMatchObject({ gpu: '', provider: '', pickReason: '', inferenceMs: 0 });
		expect(row?.tools.size).toBe(0);
	});

	it('merges later messages: span, strongest signal, latest placement, longest inference, all tools', () => {
		const row = aggregateAgents(
			[
				msg('sre', 'agree', 1000, { gpuLabel: 'g1', inferenceMs: 900, toolsUsed: ['a'] }),
				msg('sre', 'advisory', 200, {
					provider: 'ollama-gpu',
					pickReason: 'warm',
					inferenceMs: 100
				}),
				msg('sre', 'concern', 3000, { gpuLabel: 'g2', toolsUsed: ['b', 'a'] }),
				msg('sre', 'heartbeat', 4000)
			],
			T0
		).get('sre');
		expect(row).toMatchObject({
			signal: 'concern',
			gpu: 'g2',
			provider: 'ollama-gpu',
			pickReason: 'warm',
			startMs: 200,
			endMs: 4000,
			inferenceMs: 900
		});
		expect([...(row?.tools ?? [])]).toEqual(['a', 'b']);
	});

	it('keeps the first signal when a later one has no higher priority', () => {
		const row = aggregateAgents([msg('sre', 'block', 0), msg('sre', 'agree', 1)], T0).get('sre');
		expect(row?.signal).toBe('block');
	});

	it('ranks only the consensus signals; any other signal never replaces them', () => {
		const row = aggregateAgents(
			[msg('sre', 'stand_aside', 0), msg('sre', 'proposal', 1), msg('sre', 'contribution', 2)],
			T0
		).get('sre');
		expect(row?.signal).toBe('stand_aside');
		const first = aggregateAgents([msg('x', 'heartbeat', 0), msg('x', 'reply', 1)], T0).get('x');
		expect(first?.signal).toBe('heartbeat');
	});

	it('keeps one row per agent', () => {
		const rows = aggregateAgents(
			[msg('a', 'agree', 0), msg('b', 'agree', 0), msg('a', 'agree', 1)],
			T0
		);
		expect([...rows.keys()]).toEqual(['a', 'b']);
	});
});
