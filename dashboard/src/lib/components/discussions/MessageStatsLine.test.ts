import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import MessageStatsLine from './MessageStatsLine.svelte';

describe('MessageStatsLine', () => {
	it('shows durations, tokens, the model, and the accelerator a contribution carries', () => {
		const { container } = render(MessageStatsLine, {
			props: {
				stats: {
					triageMs: 2_000,
					evaluationMs: 42_000,
					inferenceMs: 40_000,
					inputTokens: 12_345,
					outputTokens: 678,
					model: 'qwen3:32b',
					accelerator: 'ollama-rig1'
				},
				acceleratorNames: { 'ollama-rig1': 'RTX 5090' }
			}
		});
		const text = container.textContent?.replaceAll(/\s+/g, ' ') ?? '';
		expect(text).toContain('triage 2.0s');
		expect(text).toContain('evaluation 42.0s');
		expect(text).toContain('inference 40.0s');
		expect(text).toContain('12,345 in / 678 out tokens');
		expect(screen.getByText('qwen3:32b')).toBeTruthy();
		expect(screen.getByText('on ollama-rig1').getAttribute('title')).toBe('Accelerator: RTX 5090');
	});

	it('omits fields the message does not carry rather than showing zero', () => {
		const { container } = render(MessageStatsLine, {
			props: { stats: { evaluationMs: 7_000, outputTokens: 12 } }
		});
		const text = container.textContent?.replaceAll(/\s+/g, ' ').trim() ?? '';
		expect(text).toBe('evaluation 7.0s 12 out tokens');
	});

	it('renders nothing for an old message without stats', () => {
		const { container } = render(MessageStatsLine, {
			props: { stats: { elapsedMs: 5_000 } }
		});
		expect(container.querySelector('.stats-line')).toBeNull();
	});
});
