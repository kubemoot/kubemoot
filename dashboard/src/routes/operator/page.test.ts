import { render, screen, waitFor, within } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { setPageUrl } from '../../../vitest-mocks/app-state.svelte.js';
import Page from './+page.svelte';

const NOW = Date.now();
const deployment = {
	name: 'kubemoot-operator', namespace: 'kubemoot', version: '0.356.0', image: 'ghcr.io/kubemoot/operator:0.356.0', desired: 1, ready: 1,
	conditions: [
		{ type: 'Available', status: 'True', reason: 'MinimumReplicasAvailable', lastTransitionTime: '2026-11-22T00:40:45.000Z' },
		{ type: 'Progressing', status: 'False', reason: 'ProgressDeadlineExceeded', message: 'stuck' }
	],
	resource: { apiVersion: 'apps/v1', kind: 'Deployment', metadata: { name: 'kubemoot-operator' } }
};
const full = {
	deployment: { available: true, data: deployment },
	startedAt: { available: true, data: new Date(NOW - 3 * 3_600_000 - 5 * 60_000).toISOString() },
	leader: { available: true, data: 'kubemoot-operator-abc-xyz' },
	crds: { available: true, data: [{ name: 'crews.kubemoot.ai', kind: 'Crew', scope: 'Namespaced', versions: [{ name: 'v1alpha1', served: true, storage: true }] }, { name: 'agents.kubemoot.ai', kind: 'Agent', scope: 'Namespaced', versions: [{ name: 'v1alpha1', served: true, storage: false }, { name: 'v1beta1', served: false, storage: true }] }] }
};

function serve(detail: object, report: object | number = [{ name: 'operator', healthy: true, message: 'ok' }]) {
	vi.stubGlobal('fetch', vi.fn(async (url: string) => {
		if (String(url).includes('componentstatuses')) return typeof report === 'number' ? new Response('{}', { status: report }) : new Response(JSON.stringify(report), { status: 200 });
		return new Response(JSON.stringify(detail), { status: 200 });
	}));
}

beforeEach(() => setPageUrl('/dashboard/operator'));
afterEach(() => vi.unstubAllGlobals());

describe('the operator page', () => {
	it('shows version, uptime, leader, health, conditions and the registered CRDs with their versions', async () => {
		serve(full);
		const { container } = render(Page);
		await waitFor(() => expect(screen.getByTestId('crds')).toBeTruthy());
		const text = container.textContent ?? '';
		expect(text).toContain('0.356.0');
		expect(text).toContain('3h 5m');
		expect(text).toContain('kubemoot-operator-abc-xyz');
		expect(text).toContain('1 of 1');
		await waitFor(() => expect(container.textContent).toContain('Ready: ok'));
		const crds = within(screen.getByTestId('crds'));
		expect(crds.getByText('agents.kubemoot.ai')).toBeTruthy();
		expect(screen.getByTestId('crds').textContent).toContain('v1alpha1, v1beta1 (stored) (not served)');
		expect(screen.getByText('Registered resource types (2)')).toBeTruthy();
		expect(screen.getByTestId('conditions').textContent).toContain('ProgressDeadlineExceeded');
		expect(screen.getByRole<HTMLAnchorElement>('link', { name: 'Kubemoot Config' }).getAttribute('href')).toMatch(/\/config$/);
	});

	it('offers the Deployment as YAML, JSON and KYAML', async () => {
		serve(full);
		render(Page);
		await waitFor(() => expect(screen.getByRole('combobox', { name: 'View' })).toBeTruthy());
		expect([...screen.getByRole<HTMLSelectElement>('combobox', { name: 'View' }).options].map((o) => o.textContent)).toEqual(['Description', 'YAML', 'JSON', 'KYAML']);
	});

	it('says plainly what the account may not read and shows no zero or empty list for it', async () => {
		const no = (what: string) => ({ available: false, reason: `your account may not read ${what}` });
		serve({ deployment: no('the operator Deployment'), startedAt: no('the operator pods'), leader: no('the operator leader lease'), crds: no('the custom resource definitions') });
		const { container } = render(Page);
		await waitFor(() => expect(container.textContent).toContain('your account may not read the custom resource definitions'));
		const text = container.textContent ?? '';
		for (const what of ['the operator Deployment', 'the operator pods', 'the operator leader lease']) expect(text).toContain(`your account may not read ${what}`);
		expect(screen.queryByTestId('crds')).toBeNull();
		expect(text).not.toContain('No Kubemoot resource types');
		expect(screen.queryByRole('combobox', { name: 'View' })).toBeNull();
	});

	it('says no operator pod is running, and that no lease holder was found, rather than a blank', async () => {
		serve({ ...full, startedAt: { available: true, data: '' }, leader: { available: true, data: '' } });
		const { container } = render(Page);
		await waitFor(() => expect(container.textContent).toContain('no operator pod is running'));
		expect(container.textContent).toContain('no operator pod holds the lease');
	});

	it('says none are registered only when the read found none', async () => {
		serve({ ...full, crds: { available: true, data: [] } });
		render(Page);
		await waitFor(() => expect(screen.getByText('No Kubemoot resource types are registered in this cluster.')).toBeTruthy());
	});

	it('shows the health report as unread when it cannot be read', async () => {
		serve(full, 502);
		const { container } = render(Page);
		await waitFor(() => expect(container.textContent).toContain("The operator's health report could not be read"));
	});

	it('shows an error when the operator cannot be read at all', async () => {
		vi.stubGlobal('fetch', vi.fn(async () => new Response('{}', { status: 500 })));
		render(Page);
		await waitFor(() => expect(screen.getByText('HTTP 500')).toBeTruthy());
	});
});
