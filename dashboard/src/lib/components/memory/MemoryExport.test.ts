import { render, screen, waitFor } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import MemoryExport from './MemoryExport.svelte';

const scope = { namespace: 'pilot', crew: 'homelab-pilot' };
const held = { namespace: 'pilot', crew: 'homelab-pilot', memories: 2, leftOut: [] };
const answer = (body: unknown, status = 200) =>
	vi.fn().mockResolvedValue({ ok: status < 400, status, json: async () => body });

describe('MemoryExport', () => {
	it('is one Export control that downloads the zip named for the crew when there are facts', () => {
		render(MemoryExport, { scope, endpoint: '/api/export', hasFacts: true, fetchFn: answer(held) });
		const links = screen.getAllByRole('link');
		expect(links).toHaveLength(1);
		expect(screen.queryByRole('button')).toBeNull();
		expect(screen.queryByText('Download')).toBeNull();
		expect(links[0].textContent).toBe('Export');
		expect(links[0].getAttribute('href')).toBe('/api/export?namespace=pilot&crew=homelab-pilot');
		expect(links[0].getAttribute('download')).toBe('homelab-pilot-memory-ompi.zip');
		expect(links[0].getAttribute('title')).toMatch(/OMPI zip: a manifest and one JSON file per fact, with the agent that learned it and when/);
	});

	it('is disabled with the reason when there are no facts, and asks nothing', () => {
		const fetchFn = answer(held);
		render(MemoryExport, { scope, endpoint: '/api/export', hasFacts: false, fetchFn });
		const button = screen.getByRole<HTMLButtonElement>('button', { name: /Export/ });
		expect(button.disabled).toBe(true);
		expect(button.getAttribute('aria-label')).toContain('Nothing to export: this crew has not learned any facts yet');
		expect(button.parentElement?.getAttribute('title')).toBe('Nothing to export: this crew has not learned any facts yet');
		expect(screen.queryByRole('link')).toBeNull();
		expect(fetchFn).not.toHaveBeenCalled();
	});

	it('says how many entries the screen left out', async () => {
		const left = { ...held, leftOut: [{ key: 'pilot.homelab-pilot.#1a2b', reason: 'credential' }] };
		render(MemoryExport, { scope, endpoint: '/api/export', hasFacts: true, fetchFn: answer(left) });
		await waitFor(() => expect(screen.getByText('The export holds 2 facts; 1 entry was left out.')).toBeTruthy());
	});

	it('still downloads when the report cannot be had', async () => {
		const fetchFn = answer({ error: 'down' }, 502);
		render(MemoryExport, { scope, endpoint: '/api/export', hasFacts: true, fetchFn });
		await waitFor(() => expect(fetchFn).toHaveBeenCalled());
		expect(screen.getByRole('link', { name: 'Export' })).toBeTruthy();
		expect(screen.queryByText(/left out/)).toBeNull();
	});
});
