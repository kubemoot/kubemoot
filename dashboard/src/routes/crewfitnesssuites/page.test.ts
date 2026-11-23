import { render, screen } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { setPageData } from '../../../vitest-mocks/app-state.svelte.js';
import Page from './+page.svelte';

const failedScenarios = ['a', 'b', 'c', 'd'].map((n) => ({ name: `scn-${n}`, failureReason: `gate ${n} failed` }));

const suite = (name: string, status: Record<string, unknown>) => ({
	apiVersion: 'kubemoot.ai/v1alpha1',
	kind: 'CrewFitnessSuite',
	metadata: { name, namespace: 'crew-a', uid: name, creationTimestamp: '2026-11-21T10:00:00Z' },
	spec: { crewRef: 'homelab-pilot', scripts: [{ testRef: 'x' }] },
	status
});

const items = [
	suite('warn-run', { phase: 'Completed', passed: 28, failed: 4, errored: 0, judge: { phase: 'Complete', mean: 64 }, scenarios: failedScenarios }),
	suite('bad-spec', { phase: 'Error', error: 'spec.scripts is empty' }),
	suite('smoke-run', {
		phase: 'ToolSmokeFailed',
		conditions: [{ type: 'ToolSmoke', status: 'False', reason: 'Failed', message: '2 tool(s) failed the smoke, no model ran', lastTransitionTime: '' }]
	})
];

beforeEach(() => {
	setPageData({ user: { username: 'jonathan' } });
	vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(new Response(JSON.stringify({ items }), { status: 200 }))));
	vi.stubGlobal('EventSource', class { close() {} onmessage = null; });
});
afterEach(() => vi.unstubAllGlobals());

describe('the fitness suites list', () => {
	it('labels gates and judge score apart and shows the judge mean', async () => {
		render(Page);
		await screen.findByText('warn-run');
		const headers = Array.from(document.querySelectorAll('thead th')).map((h) => h.textContent?.trim());
		expect(headers).toContain('Gates pass / fail / err i');
		expect(headers).toContain('Judge score');
		const row = screen.getByText('warn-run').closest('tr') as HTMLElement;
		expect(row.textContent).toContain('64');
	});

	it('shows the first failure reasons and counts the rest', async () => {
		render(Page);
		await screen.findByText('warn-run');
		expect(screen.getByText(/gate a failed/)).toBeTruthy();
		expect(screen.getByText(/gate c failed/)).toBeTruthy();
		expect(screen.queryByText(/gate d failed/)).toBeNull();
		expect(screen.getByText('and 1 more failed scenario')).toBeTruthy();
	});

	it('shows why an Error and a ToolSmokeFailed suite did not run', async () => {
		render(Page);
		expect(await screen.findByText('spec.scripts is empty')).toBeTruthy();
		expect(screen.getByText('2 tool(s) failed the smoke, no model ran')).toBeTruthy();
	});
});
