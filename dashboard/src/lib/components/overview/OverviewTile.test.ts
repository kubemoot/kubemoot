import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import OverviewTile from './OverviewTile.svelte';
import type { Tile } from '#lib/overview-model.js';

const tile = (over: Partial<Tile> = {}): Tile => ({ key: 'operator', label: 'Operator', link: { path: '/operator' }, phase: 'ready', health: 'healthy', figure: 'Ready', line: '', bars: [], ...over });

describe('OverviewTile', () => {
	it('opens the page its link names, with the query when it has one', () => {
		const { unmount } = render(OverviewTile, { tile: tile() });
		expect(screen.getByTestId('tile-operator').getAttribute('href')).toMatch(/\/operator$/);
		unmount();
		render(OverviewTile, { tile: tile({ key: 'accelerators', link: { path: '/nodes', search: '?role=accelerator' } }) });
		expect(screen.getByTestId('tile-accelerators').getAttribute('href')).toMatch(/\/nodes\?role=accelerator$/);
	});

	it('lists each fact under the figure, and marks one that could not be read', () => {
		render(OverviewTile, { tile: tile({ details: [{ text: 'Version 1.2.3', read: true }, { text: 'Kubemoot CRDs: your account may not read them', read: false }] }) });
		const items = screen.getAllByRole('listitem');
		expect(items.map((i) => i.textContent)).toEqual(['Version 1.2.3', 'Kubemoot CRDs: your account may not read them']);
		expect(items[0].classList.contains('refused')).toBe(false);
		expect(items[1].classList.contains('refused')).toBe(true);
	});

	it('draws no empty line or list when there is nothing to say', () => {
		const { container } = render(OverviewTile, { tile: tile({ details: [] }) });
		expect(container.querySelector('.line')).toBeNull();
		expect(container.querySelector('.details')).toBeNull();
	});
});
