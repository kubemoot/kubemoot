import { describe, expect, it } from 'vitest';
import type { DiscussionMessage } from '#lib/types/kubemoot.js';
import { aggregateAgents, renderAgentSummary } from './discussion-agent-summary';

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
			placement: 'g1',
			pickReason: '',
			startMs: 500,
			endMs: 500,
			inferenceMs: 40,
			tools: new Set(['pods_list'])
		});
	});

	it('defaults the metadata fields when a message has none', () => {
		const row = aggregateAgents([msg('sre', 'stand_aside', 0)], T0).get('sre');
		expect(row).toMatchObject({ placement: undefined, pickReason: '', inferenceMs: 0 });
		expect(row?.tools.size).toBe(0);
	});

	it('merges later messages: span, strongest signal, latest placement, longest inference, all tools', () => {
		const row = aggregateAgents(
			[
				msg('sre', 'agree', 1000, { gpuLabel: 'g1', inferenceMs: 900, toolsUsed: ['a'] }),
				msg('sre', 'advisory', 200, {
					server: 'ollama-gpu',
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
			placement: 'ollama-gpu',
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

describe('aggregateAgents placement', () => {
	it('follows the span graph: an engine label of a message without a placed call is not a server', () => {
		const rows = aggregateAgents(
			[
				msg('coord', 'advisory', 0, { gpuLabel: 'vllm-rig0', modelsUsed: ['m@vllm-rig0'] }),
				msg('coord', 'review_ready', 5, { gpuLabel: 'vllm' }),
				msg('scout', 'failure', 6, { gpuLabel: 'vllm' })
			],
			T0
		);
		expect(rows.get('coord')?.placement).toBe('vllm-rig0');
		expect(rows.get('scout')?.placement).toBeUndefined();
	});

	it('takes the last placed call of a message carrying modelsUsed and placementCalls', () => {
		const call = (server: string) => ({ model: 'm', server });
		const rows = aggregateAgents(
			[
				msg('a', 'agree', 0, {
					modelsUsed: ['m@vllm-rig0', 'm@vllm-rig1'],
					placementCalls: [call('vllm-rig0'), call('vllm-rig1')]
				} as never)
			],
			T0
		);
		expect(rows.get('a')?.placement).toBe('vllm-rig1');
	});
});

describe('renderAgentSummary', () => {
	it('shows the placement, with the pick reason beneath, and a dash when unknown', () => {
		const rows = aggregateAgents(
			[
				msg('a', 'agree', 0, { server: 'vllm-rig0', pickReason: 'warm', inferenceMs: 2000, toolsUsed: ['t'] }),
				msg('b', 'failure', 1, { gpuLabel: 'vllm' }),
				msg('c', 'agree', 2, { server: 'vllm-rig0' })
			],
			T0
		);
		const text = renderAgentSummary(rows, (agent) => agent.toUpperCase());
		const lines = text.split('\n');
		expect(lines[0]).toBe('## Agent Summary');
		expect(lines.find((l) => l.startsWith('| A '))).toContain('| vllm-rig0<br/>_warm_ |');
		expect(lines.find((l) => l.startsWith('| B '))).toMatch(/\| failure \| - \|/);
	});
});
