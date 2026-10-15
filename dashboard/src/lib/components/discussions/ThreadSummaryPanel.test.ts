import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import ThreadSummaryPanel from './ThreadSummaryPanel.svelte';
import type { ThreadSummary } from '#lib/discussion-timing.js';

function rowValue(container: HTMLElement, label: string): string | undefined {
	const dt = [...container.querySelectorAll('dt')].find((d) => d.textContent === label);
	return dt?.nextElementSibling?.textContent ?? undefined;
}

const FULL: ThreadSummary = {
	startedAt: '2026-10-15T12:00:00.000Z',
	endedAt: '2026-10-15T12:01:12.000Z',
	endedBy: 'answer',
	totalMs: 72_000,
	firstSignalMs: 6_000,
	convened: ['homelab-sre', 'homelab-net'],
	participants: ['homelab-net', 'homelab-sre'],
	stoodAside: ['homelab-net'],
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
};

describe('ThreadSummaryPanel', () => {
	it('shows the answer time and every total of a thread that carries them', () => {
		const { container } = render(ThreadSummaryPanel, {
			props: {
				summary: FULL,
				agentLabel: (a: string) => a.replace('homelab-', '')
			}
		});
		expect(screen.getByText(/^Answered /).getAttribute('title')).toBe(FULL.endedAt);
		expect(rowValue(container, 'Total time')).toBe('1m12s');
		expect(rowValue(container, 'First signal')).toBe('after 6.0s');
		expect(rowValue(container, 'Convened')).toBe('sre, net');
		expect(rowValue(container, 'Took part')).toBe('net, sre');
		expect(rowValue(container, 'Stood aside')).toBe('net');
		expect(rowValue(container, 'Signals')).toBe('1 agree, 1 stood aside');
		expect(rowValue(container, 'Agent tokens')).toBe('12,445 in / 683 out');
		expect(rowValue(container, 'Coordinator tokens')).toBe('40 in / 10 out');
		expect(rowValue(container, 'Models')).toBe('qwen3:32b, qwen3:8b');
		expect(rowValue(container, 'Accelerators')).toBe('ollama-rig1');
	});

	it('leaves out the rows an old thread cannot fill', () => {
		const { container } = render(ThreadSummaryPanel, {
			props: {
				summary: {
					participants: ['sre'],
					stoodAside: [],
					signals: [{ kind: 'agree', count: 1 }],
					models: [],
					accelerators: []
				}
			}
		});
		const labels = [...container.querySelectorAll('dt')].map((d) => d.textContent);
		expect(labels).toEqual(['Agents', 'Signals']);
		expect(screen.queryByText(/^Answered /)).toBeNull();
	});

	it('says Closed when the thread ended without an answer', () => {
		render(ThreadSummaryPanel, {
			props: {
				summary: {
					endedAt: '2026-10-15T12:00:30.000Z',
					endedBy: 'close',
					totalMs: 30_000,
					participants: [],
					stoodAside: [],
					signals: [],
					models: [],
					accelerators: []
				}
			}
		});
		expect(screen.getByText(/^Closed /)).toBeTruthy();
	});
});
