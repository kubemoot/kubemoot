import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import PlacementPanel from './PlacementPanel.svelte';
import type { PlacementCall } from '#lib/types/bus.js';

const WITH_PREDICTION: PlacementCall = {
	agent: 'kubernetes-specialist',
	phase: 'evaluation',
	model: 'qwen3:14b',
	server: 'ollama-gpu',
	coldStart: true,
	policy: 'algorithm',
	reason: 'cold-load fits 14000 <= 32605',
	alternatives: [
		{
			server: 'ollama-cpu',
			warm: true,
			startsNow: true,
			predicted: { waitMs: 0, loadMs: 0, promptMs: 4100, generateMs: 56900, totalMs: 61000 }
		}
	],
	predicted: { waitMs: 0, loadMs: 7000, promptMs: 2100, generateMs: 2300, totalMs: 11400 },
	actual: { ok: true, totalMs: 13800, inputTokens: 4100, outputTokens: 260, requests: 3 }
};

const SHADOW: PlacementCall = {
	agent: 'obs-metrics',
	phase: 'triage',
	model: 'qwen3:8b',
	server: 'ollama-cpu',
	policy: 'shadow',
	actual: { ok: true, totalMs: 31000, inputTokens: 360, outputTokens: 148, requests: 1 }
};

describe('PlacementPanel', () => {
	it('shows the summary line and one row per call', () => {
		const { container } = render(PlacementPanel, { props: { calls: [WITH_PREDICTION, SHADOW] } });
		expect(screen.getByText(/^2 calls: 1 algorithm, 1 shadow\./)).toBeTruthy();
		expect(container.querySelectorAll('li.call')).toHaveLength(2);
		expect(screen.getByText('qwen3:14b@ollama-gpu')).toBeTruthy();
		expect(screen.getByText('cold start')).toBeTruthy();
		expect(screen.getByText('cold-load fits 14000 <= 32605')).toBeTruthy();
		expect(screen.getByText(/predicted 11\.4s, actual 13\.8s, error \+2\.4s \(\+17\.4%\)/)).toBeTruthy();
	});

	it('says no prediction for a call without one', () => {
		render(PlacementPanel, { props: { calls: [SHADOW] } });
		expect(screen.getByText('no prediction; actual total 31.0s')).toBeTruthy();
	});

	it('lists the alternatives considered with their predicted parts', () => {
		render(PlacementPanel, { props: { calls: [WITH_PREDICTION] } });
		expect(screen.getByText('1 alternative considered')).toBeTruthy();
		expect(screen.getByText('qwen3:14b@ollama-cpu')).toBeTruthy();
		expect(screen.getByText('wait 0ms, load 0ms, prompt 4.1s, generate 56.9s')).toBeTruthy();
	});

	it('uses the agent label function', () => {
		render(PlacementPanel, {
			props: { calls: [SHADOW], agentLabel: (a: string) => a.toUpperCase() }
		});
		expect(screen.getByText('OBS-METRICS')).toBeTruthy();
	});

	it('shows a short note when there are no calls', () => {
		render(PlacementPanel, { props: { calls: [] } });
		expect(screen.getByText('No placement decisions were recorded for this thread.')).toBeTruthy();
	});
});
