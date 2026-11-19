import { render, screen, waitFor } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import MemoryExportPanel from './MemoryExportPanel.svelte';

const scope = { namespace: 'pilot', crew: 'homelab-pilot' };
const held = { namespace: 'pilot', crew: 'homelab-pilot', memories: 2, leftOut: [] };
const withLeftOut = {
	...held,
	leftOut: [
		{ key: 'pilot.homelab-pilot.#1a2b3c4d', reason: 'its topic or key holds a credential; not exported as memory' },
		{ key: 'pilot.homelab-pilot.ops.login', reason: 'its value holds a credential (bearer token); not exported as memory' }
	]
};

const answer = (body: unknown, status = 200) =>
	vi.fn().mockResolvedValue({ ok: status < 400, status, json: async () => body });

describe('MemoryExportPanel', () => {
	it('says the export is screened', async () => {
		const fetchFn = answer(held);
		render(MemoryExportPanel, { scope, endpoint: '/api/export', fetchFn });
		expect(screen.getByText(/Screened: a fact whose topic, key or value holds a credential is left out/)).toBeTruthy();
		await waitFor(() => expect(screen.getByText('The export holds 2 facts; nothing was left out.')).toBeTruthy());
		expect(fetchFn).toHaveBeenCalledWith('/api/export?namespace=pilot&crew=homelab-pilot&report=true');
	});

	it('offers a download of the screened zip, named for the crew', async () => {
		render(MemoryExportPanel, { scope, endpoint: '/api/export', fetchFn: answer(held) });
		const link = await screen.findByRole('link', { name: 'Download' });
		expect(link.getAttribute('href')).toBe('/api/export?namespace=pilot&crew=homelab-pilot');
		expect(link.getAttribute('download')).toBe('homelab-pilot-memory-ompi.zip');
	});

	it('lists what was left out by key or opaque id and reason, with no fact text', async () => {
		render(MemoryExportPanel, { scope, endpoint: '/api/export', fetchFn: answer(withLeftOut) });
		expect(await screen.findByText('pilot.homelab-pilot.#1a2b3c4d')).toBeTruthy();
		expect(screen.getByText(/bearer token/)).toBeTruthy();
		expect(screen.getByText('The export holds 2 facts; 2 entries were left out.')).toBeTruthy();
	});

	it('offers no way to include facts that hold a credential, and points to kmctl', async () => {
		render(MemoryExportPanel, { scope, endpoint: '/api/export', fetchFn: answer(held) });
		await screen.findByRole('link', { name: 'Download' });
		expect(screen.queryByRole('checkbox')).toBeNull();
		expect(screen.getByText('kmctl')).toBeTruthy();
		expect(document.body.innerHTML).not.toMatch(/allowUnscreened/);
	});

	it('shows the failure and offers no download when the report cannot be had', async () => {
		render(MemoryExportPanel, { scope, endpoint: '/api/export', fetchFn: answer({ error: 'crew memory is not reachable' }, 502) });
		expect((await screen.findByRole('alert')).textContent).toContain('crew memory is not reachable');
		expect(screen.queryByRole('link', { name: 'Download' })).toBeNull();
	});
});
