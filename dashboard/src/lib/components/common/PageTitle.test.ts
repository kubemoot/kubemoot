import { fireEvent, render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import PageTitle from './PageTitle.svelte';
import { PAGE_DEFINITIONS } from '#lib/page-definitions.js';

describe('PageTitle', () => {
	it('shows the defined title as the page heading', () => {
		render(PageTitle, { route: '/agents' });
		expect(screen.getByRole('heading', { level: 1 }).textContent?.trim()).toBe('Agents');
	});

	it('shows a title override for a detail page', () => {
		render(PageTitle, { route: '/agents/[name]', title: 'homelab-coordinator' });
		expect(screen.getByRole('heading', { level: 1 }).textContent?.trim()).toBe('homelab-coordinator');
	});

	it('has no tooltip on the title, and no subtitle line', () => {
		const { container } = render(PageTitle, { route: '/nodes' });
		expect(screen.getByText('Kubernetes Nodes').getAttribute('aria-describedby')).toBeNull();
		expect(container.querySelector('[role="tooltip"], [tabindex], p')).toBeNull();
		expect(document.querySelector('[role="tooltip"]')).toBeNull();
	});

	it('puts an (i) beside the title that opens the term and the purpose on click', async () => {
		render(PageTitle, { route: '/nodes' });
		expect(screen.queryByRole('dialog')).toBeNull();
		await fireEvent.click(screen.getByRole('button', { name: 'About Kubernetes Nodes' }));
		const help = screen.getByRole('dialog').textContent ?? '';
		expect(help).toContain(PAGE_DEFINITIONS['/nodes'].term);
		expect(help).toContain(PAGE_DEFINITIONS['/nodes'].purpose);
	});

	it('names the (i) after a detail page\'s resource, and adds a page\'s notes', async () => {
		render(PageTitle, { route: '/conversations', notes: ['An extra line.'] });
		await fireEvent.click(screen.getByRole('button', { name: 'About Conversations' }));
		expect(screen.getByRole('dialog').textContent).toContain('An extra line.');
	});

	it('closes on Escape', async () => {
		render(PageTitle, { route: '/crews' });
		await fireEvent.click(screen.getByRole('button', { name: 'About Crews' }));
		await fireEvent.keyDown(window, { key: 'Escape' });
		expect(screen.queryByRole('dialog')).toBeNull();
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
