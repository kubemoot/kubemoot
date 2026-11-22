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

describe('Sidebar menu items', () => {
	it('names every menu item after a page, with no tooltip, since the label says what it opens', () => {
		render(Sidebar);
		const links = navLinks();
		expect(links.length).toBeGreaterThan(15);
		const known = Object.keys(PAGE_DEFINITIONS) as PageRoute[];
		for (const link of links) {
			const label = link.textContent?.trim() ?? '';
			expect(known.find((r) => !r.includes('[') && menuLabel(r) === label), `no page for menu item ${label}`).toBeTruthy();
			expect(link.getAttribute('aria-describedby')).toBeNull();
		}
	});

	it('shows no tooltip on focus or after a hover pause', async () => {
		vi.useFakeTimers();
		render(Sidebar);
		const link = screen.getByRole('link', { name: 'Agents' });
		await fireEvent.focus(link);
		await fireEvent.mouseEnter(link);
		await vi.advanceTimersByTimeAsync(2000);
		expect(document.querySelector('[role="tooltip"][data-open]')).toBeNull();
		expect(link.getAttribute('aria-describedby')).toBeNull();
	});
});

describe('Sidebar order and names', () => {
	it('lists Conversations first and the Moot second, under Messaging', () => {
		render(Sidebar, { version: '0.1.0' });
		const all = [...document.querySelectorAll('nav li')].map((li) => li.textContent?.trim());
		const at = all.indexOf('Messaging');
		expect(all.slice(at, at + 3)).toEqual(['Messaging', 'Conversations', 'Moot']);
	});

	it('has no Discussions entry, and the Moot points at /moot', () => {
		render(Sidebar, { version: '0.1.0' });
		const links = navLinks();
		expect(links.map((a) => a.textContent?.trim())).not.toContain('Discussions');
		expect(links.find((a) => a.textContent?.trim() === 'Moot')?.getAttribute('href')).toBe('/moot');
	});
});
