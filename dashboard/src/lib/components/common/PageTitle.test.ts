import { fireEvent, render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import PageTitle from './PageTitle.svelte';
import { PAGE_DEFINITIONS } from '#lib/page-definitions.js';

const tip = () => screen.getByRole('tooltip', { hidden: true });
const isOpen = () => tip().hasAttribute('data-open');

describe('PageTitle', () => {
	it('shows the defined title as the page heading', () => {
		render(PageTitle, { route: '/agents' });
		expect(screen.getByRole('heading', { level: 1 }).textContent?.trim()).toBe('Agents');
	});

	it('shows a title override for a detail page', () => {
		render(PageTitle, { route: '/agents/[name]', title: 'homelab-coordinator' });
		expect(screen.getByRole('heading', { level: 1 }).textContent?.trim()).toBe('homelab-coordinator');
	});

	it('describes the title with the term and the purpose for a screen reader', () => {
		render(PageTitle, { route: '/nodes' });
		const target = screen.getByText('Kubernetes Nodes');
		expect(target.getAttribute('aria-describedby')).toBe(tip().id);
		expect(tip().textContent).toContain(PAGE_DEFINITIONS['/nodes'].term);
		expect(tip().textContent).toContain(PAGE_DEFINITIONS['/nodes'].purpose);
	});

	it('can be reached with the keyboard and shows the tooltip on focus', async () => {
		render(PageTitle, { route: '/crews' });
		const target = screen.getByText('Crews');
		expect(target.getAttribute('tabindex')).toBe('0');
		expect(isOpen()).toBe(false);
		await fireEvent.focus(target);
		expect(isOpen()).toBe(true);
		await fireEvent.blur(target);
		expect(isOpen()).toBe(false);
	});

	it('closes on Escape', async () => {
		render(PageTitle, { route: '/crews' });
		const target = screen.getByText('Crews');
		await fireEvent.focus(target);
		await fireEvent.keyDown(window, { key: 'Escape' });
		expect(isOpen()).toBe(false);
	});

	it('keeps the normal heading font unless it is a resource name', () => {
		const { container } = render(PageTitle, { route: '/crews' });
		expect(container.querySelector('h1')?.classList.contains('mono')).toBe(false);
	});

	it('uses monospace for a resource name', () => {
		const { container } = render(PageTitle, { route: '/crews/[name]', title: 'homelab-pilot', mono: true });
		expect(container.querySelector('h1')?.classList.contains('mono')).toBe(true);
	});
});
