import { fireEvent, render, screen } from '@testing-library/svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import Sidebar from './Sidebar.svelte';
import { PAGE_DEFINITIONS, menuLabel, type PageRoute } from '#lib/page-definitions.js';

afterEach(() => {
	vi.useRealTimers();
	document.body.replaceChildren();
});

function navLinks(): HTMLAnchorElement[] {
	return screen.getAllByRole('link').filter((a) => a.classList.contains('nav-link')) as HTMLAnchorElement[];
}

describe('Sidebar menu tooltips', () => {
	it('gives every menu item a defined description, keeping the label as its name', () => {
		render(Sidebar);
		const links = navLinks();
		expect(links.length).toBeGreaterThan(15);
		const known = Object.keys(PAGE_DEFINITIONS) as PageRoute[];
		for (const link of links) {
			const label = link.textContent?.trim() ?? '';
			const route = known.find((r) => !r.includes('[') && menuLabel(r) === label);
			expect(route, `no definition for menu item ${label}`).toBeTruthy();
			const def = PAGE_DEFINITIONS[route as PageRoute];
			const tip = document.getElementById(link.getAttribute('aria-describedby') ?? '');
			expect(tip?.getAttribute('role')).toBe('tooltip');
			expect(tip?.textContent).toBe(`${def.term}${def.purpose}`);
		}
	});

	it('shows the tooltip on keyboard focus right away and hides it on blur', async () => {
		render(Sidebar);
		const link = screen.getByRole('link', { name: 'Nodes' });
		await fireEvent.focus(link);
		expect(document.querySelector('[role="tooltip"][data-open]')?.textContent).toContain(PAGE_DEFINITIONS['/nodes'].term);
		await fireEvent.blur(link);
		expect(document.querySelector('[role="tooltip"][data-open]')).toBeNull();
	});

	it('shows the tooltip on hover only after a short pause', async () => {
		vi.useFakeTimers();
		render(Sidebar);
		const link = screen.getByRole('link', { name: 'Agents' });
		await fireEvent.mouseEnter(link);
		expect(document.querySelector('[role="tooltip"][data-open]')).toBeNull();
		await vi.advanceTimersByTimeAsync(500);
		expect(document.querySelector('[role="tooltip"][data-open]')?.textContent).toContain(PAGE_DEFINITIONS['/agents'].purpose);
	});

	it('shows nothing when the pointer leaves before the pause ends', async () => {
		vi.useFakeTimers();
		render(Sidebar);
		const link = screen.getByRole('link', { name: 'Agents' });
		await fireEvent.mouseEnter(link);
		await fireEvent.mouseLeave(link);
		await vi.advanceTimersByTimeAsync(500);
		expect(document.querySelector('[role="tooltip"][data-open]')).toBeNull();
	});
});
