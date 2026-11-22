import { fireEvent, render, screen, within } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { setPageData } from '../../../vitest-mocks/app-state.svelte.js';
import Page from './+page.svelte';

const crew = (name: string) => ({
	apiVersion: 'kubemoot.ai/v1alpha1',
	kind: 'Crew',
	metadata: { name, namespace: `crew-${name}`, uid: name },
	spec: { description: `${name} crew` },
	status: { phase: 'Ready', ready: true, agentCount: 3 }
});

let fetchMock: ReturnType<typeof vi.fn>;
let deleteAnswer: () => Response;

beforeEach(() => {
	setPageData({ user: { username: 'jonathan' } });
	deleteAnswer = () => new Response(JSON.stringify({ deleted: true }), { status: 200 });
	fetchMock = vi.fn((_url: string, init?: RequestInit) =>
		Promise.resolve(init?.method === 'DELETE' ? deleteAnswer() : new Response(JSON.stringify({ items: [crew('alpha'), crew('beta')] }), { status: 200 }))
	);
	vi.stubGlobal('fetch', fetchMock);
	vi.stubGlobal('EventSource', class { close() {} onmessage = null; addEventListener() {} });
});

afterEach(() => vi.unstubAllGlobals());

describe('the Crews page', () => {
	it('has a ... menu on every crew card and no kmctl hint', async () => {
		render(Page);
		await screen.findByText('alpha');
		expect(screen.getAllByRole('button', { name: /^Actions for / })).toHaveLength(2);
		expect(document.body.textContent).not.toContain('kmctl');
		expect(document.body.textContent).not.toContain('Remove a crew');
	});

	it('deletes a crew after asking, as the person, and marks its card Deleting until the operator is done', async () => {
		render(Page);
		await screen.findByText('alpha');
		await fireEvent.click(screen.getByRole('button', { name: 'Actions for alpha' }));
		await fireEvent.click(within(screen.getByRole('menu')).getByRole('menuitem', { name: 'Delete...' }));
		expect(screen.getByRole('alertdialog').textContent).toContain('Delete the crew alpha?');
		expect(fetchMock.mock.calls.some(([, init]) => init?.method === 'DELETE')).toBe(false);
		await fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
		expect(await screen.findByText('Deleting')).toBeTruthy();
		expect(fetchMock).toHaveBeenCalledWith('/api/kubemoot/crews/alpha?namespace=crew-alpha', { method: 'DELETE' });
		expect(screen.getAllByText('Ready')).toHaveLength(1);
		await fireEvent.click(screen.getByRole('button', { name: 'Actions for alpha' }));
		expect(within(screen.getByRole('menu')).queryByRole('menuitem', { name: /Delete/ })).toBeNull();
	});

	it('answers 403 plainly and keeps the card', async () => {
		deleteAnswer = () => new Response(JSON.stringify({ error: 'You do not have permission to delete this crew. Your Kubernetes account is not allowed to; ask the cluster administrator for access.' }), { status: 403 });
		render(Page);
		await screen.findByText('alpha');
		await fireEvent.click(screen.getByRole('button', { name: 'Actions for alpha' }));
		await fireEvent.click(within(screen.getByRole('menu')).getByRole('menuitem', { name: 'Delete...' }));
		await fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
		expect((await screen.findByRole('alert')).textContent).toContain('You do not have permission to delete this crew');
		expect(screen.getAllByText('alpha').length).toBeGreaterThan(0);
	});

	it('offers no Delete to a visitor who is not signed in', async () => {
		setPageData({});
		render(Page);
		await screen.findByText('alpha');
		await fireEvent.click(screen.getByRole('button', { name: 'Actions for alpha' }));
		expect(within(screen.getByRole('menu')).queryByRole('menuitem', { name: /Delete/ })).toBeNull();
	});
});
