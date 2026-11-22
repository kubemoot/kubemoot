import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { crewDirectory, namespace } from '#lib/stores/index.js';
import Page from './+page.svelte';

const fact = { namespace: 'pilot', crew: 'homelab-pilot', topic: 'gpu', key: 'rig', value: 'rtx', learnedBy: 'a', learnedAt: '', usedAt: '2026-11-22' };

function answerWith(facts: unknown[]) {
	vi.stubGlobal(
		'fetch',
		vi.fn().mockImplementation(async (url: string) => ({
			ok: true,
			status: 200,
			json: async () => (String(url).includes('/export') ? { memories: facts.length, leftOut: [] } : { facts })
		}))
	);
}

beforeEach(() => {
	crewDirectory.set([{ namespace: 'pilot', crew: 'homelab-pilot', displayName: 'Homelab Pilot' }]);
	namespace.set('pilot');
});

describe('crew memory page', () => {
	it('with no facts: one disabled Export with the reason, the empty line, and no kmctl rows', async () => {
		answerWith([]);
		const { container } = render(Page);
		await screen.findByText(/This crew has not learned any facts yet/);
		expect(screen.getAllByRole('button', { name: /Export/ })).toHaveLength(1);
		const button = screen.getByRole<HTMLButtonElement>('button', { name: /Export/ });
		expect(button.getAttribute('aria-disabled')).toBe('true');
		await fireEvent.focus(button);
		expect((await screen.findAllByRole('tooltip')).map((t) => t.textContent)).toContain('Nothing to export: this crew has not learned any facts yet');
		expect(container.textContent).not.toMatch(/kmctl/);
	});

	it('with facts: one Export link that downloads, no Download button, no kmctl rows', async () => {
		answerWith([fact]);
		const { container } = render(Page);
		await waitFor(() => expect(screen.getByRole('link', { name: 'Export' })).toBeTruthy());
		expect(screen.getAllByRole('link', { name: /Export/ })).toHaveLength(1);
		expect(screen.queryByText('Download')).toBeNull();
		expect(screen.getByRole('link', { name: 'Export' }).getAttribute('download')).toBe('homelab-pilot-memory-ompi.zip');
		expect(container.textContent).not.toMatch(/kmctl memory/);
	});

	it('shows Last used as a local date and time with the exact UTC time in the tooltip', async () => {
		answerWith([{ ...fact, usedAt: '2026-11-22T01:00:00Z' }]);
		const { container } = render(Page);
		await waitFor(() => expect(container.querySelector('td.ts')).toBeTruthy());
		const cell = container.querySelector('td.ts');
		expect(cell?.getAttribute('title')).toBeNull();
		await fireEvent.mouseEnter(cell as Element);
		expect((await screen.findAllByRole('tooltip')).map((t) => t.textContent)).toContain('2026-11-22T01:00:00Z');
		expect(cell?.textContent).not.toContain('2026-11-22T01:00:00Z');
		expect(cell?.textContent).toMatch(/Nov/);
	});
});
