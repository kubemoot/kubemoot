import { fireEvent, render, screen } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import ActionMenu from './ActionMenu.svelte';

const entries = [{ kind: 'item' as const, label: 'Copy', tip: 'Copy it', onselect: vi.fn() }];

function pictureOf(icon?: 'menu' | 'more' | 'options'): string {
	const { container } = render(ActionMenu, { props: { entries, tip: 'Open the menu', ...(icon ? { icon } : {}) } });
	const svg = container.querySelector('button svg') as SVGElement;
	return `${svg.querySelectorAll('circle').length}c ${svg.querySelectorAll('line').length}l`;
}

describe('ActionMenu icon', () => {
	it('draws lines by default, three dots for a row menu and sliders for view options', () => {
		const lines = pictureOf();
		const dots = pictureOf('more');
		const sliders = pictureOf('options');
		expect(lines).toBe('0c 3l');
		expect(dots).toBe('3c 0l');
		expect(sliders).toBe('2c 2l');
	});

	it('keeps the same accessible button and menu whatever the picture', async () => {
		render(ActionMenu, { props: { entries, tip: 'Open the menu', icon: 'more', label: 'Actions for x' } });
		await fireEvent.click(screen.getByRole('button', { name: 'Actions for x' }));
		expect(screen.getByRole('menu', { name: 'Actions for x' })).toBeTruthy();
		expect(screen.getByRole('menuitem', { name: 'Copy' })).toBeTruthy();
	});
});
