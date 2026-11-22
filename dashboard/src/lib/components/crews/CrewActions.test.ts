import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import CrewActions from './CrewActions.svelte';
import type { Crew } from '#lib/types/kubemoot.js';

const crew = {
	metadata: { name: 'homelab-pilot', namespace: 'crew-a', annotations: { 'kubemoot.ai/display-name': 'Homelab Pilot' } },
	spec: {},
	status: { agentCount: 23 }
} as unknown as Crew;

const answer = (status: number, body: unknown = {}) => vi.fn().mockResolvedValue(new Response(JSON.stringify(body), { status }));
const writeText = vi.fn();

beforeEach(() => {
	writeText.mockReset().mockResolvedValue(undefined);
	Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
});

function mount(over: { signedIn?: boolean; openHref?: string; fetchFn?: typeof fetch } = {}) {
	const ondeleted = vi.fn();
	render(CrewActions, { props: { crew, signedIn: true, ondeleted, ...over } });
	return { ondeleted };
}

async function openMenu() {
	await fireEvent.click(screen.getByRole('button', { name: 'Actions for Homelab Pilot' }));
	return screen.getByRole('menu');
}

describe('CrewActions', () => {
	it('offers Open on the list, Copy name and Delete', async () => {
		mount({ openHref: '/crews/homelab-pilot?namespace=crew-a' });
		const menu = await openMenu();
		expect(within(menu).getAllByRole('menuitem').map((e) => e.textContent?.trim())).toEqual(['Open', 'Copy name', 'Delete...']);
		expect(within(menu).getByRole('menuitem', { name: 'Open' }).getAttribute('href')).toBe('/crews/homelab-pilot?namespace=crew-a');
	});

	it('offers no Open on the crew\'s own page', async () => {
		mount();
		expect(within(await openMenu()).queryByRole('menuitem', { name: 'Open' })).toBeNull();
	});

	it('offers no Delete to a visitor who is not signed in', async () => {
		mount({ signedIn: false });
		expect(within(await openMenu()).queryByRole('menuitem', { name: /Delete/ })).toBeNull();
	});

	it('copies the namespace and name', async () => {
		mount();
		await fireEvent.click(within(await openMenu()).getByRole('menuitem', { name: 'Copy name' }));
		await waitFor(() => expect(writeText).toHaveBeenCalledWith('crew-a/homelab-pilot'));
	});

	it('asks first, naming the crew, its agents and its memory', async () => {
		mount({ fetchFn: answer(200) });
		await fireEvent.click(within(await openMenu()).getByRole('menuitem', { name: 'Delete...' }));
		const text = screen.getByRole('alertdialog').textContent ?? '';
		expect(text).toContain('Delete the crew Homelab Pilot?');
		expect(text).toContain('Its 23 agents');
		expect(text).toContain('Its discussion history and working memory');
	});

	it('deletes the crew as the person and tells the page', async () => {
		const fetchFn = answer(200, { deleted: true });
		const { ondeleted } = mount({ fetchFn });
		await fireEvent.click(within(await openMenu()).getByRole('menuitem', { name: 'Delete...' }));
		await fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
		await waitFor(() => expect(ondeleted).toHaveBeenCalledWith(crew));
		expect(fetchFn).toHaveBeenCalledWith('/api/kubemoot/crews/homelab-pilot?namespace=crew-a', { method: 'DELETE' });
	});

	it('answers 403 plainly and keeps the crew', async () => {
		const { ondeleted } = mount({ fetchFn: answer(403, { error: 'You do not have permission to delete this crew. Your Kubernetes account is not allowed to; ask the cluster administrator for access.' }) });
		await fireEvent.click(within(await openMenu()).getByRole('menuitem', { name: 'Delete...' }));
		await fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
		expect((await screen.findByRole('alert')).textContent).toContain('You do not have permission to delete this crew');
		expect(ondeleted).not.toHaveBeenCalled();
	});

	it('answers 404 when the crew is already gone', async () => {
		mount({ fetchFn: answer(404, { error: 'This crew no longer exists.' }) });
		await fireEvent.click(within(await openMenu()).getByRole('menuitem', { name: 'Delete...' }));
		await fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
		expect((await screen.findByRole('alert')).textContent).toBe('This crew no longer exists.');
	});

	it('does not delete when the person keeps it', async () => {
		const fetchFn = answer(200);
		const { ondeleted } = mount({ fetchFn });
		await fireEvent.click(within(await openMenu()).getByRole('menuitem', { name: 'Delete...' }));
		await fireEvent.click(screen.getByRole('button', { name: 'Keep it' }));
		expect(fetchFn).not.toHaveBeenCalled();
		expect(ondeleted).not.toHaveBeenCalled();
		expect(screen.queryByRole('alertdialog')).toBeNull();
	});
});
