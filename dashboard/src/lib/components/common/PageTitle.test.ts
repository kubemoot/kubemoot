import { fireEvent, render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import PageTitle from './PageTitle.svelte';
import { PAGE_DEFINITIONS } from '#lib/page-definitions.js';

const tip = () => screen.getByRole('tooltip', { hidden: true });

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
		const id = target.getAttribute('aria-describedby');
		expect(id).toBeTruthy();
		expect(tip().id).toBe(id);
		expect(tip().textContent).toContain(PAGE_DEFINITIONS['/nodes'].term);
		expect(tip().textContent).toContain(PAGE_DEFINITIONS['/nodes'].purpose);
	});

	it('can be reached with the keyboard', () => {
		render(PageTitle, { route: '/crews' });
		expect(screen.getByText('Crews').getAttribute('tabindex')).toBe('0');
	});

	it('shows the tooltip on focus and hides it on blur', async () => {
		render(PageTitle, { route: '/crews' });
		expect(tip().className).not.toContain('shown');
		await fireEvent.focus(screen.getByText('Crews'));
		expect(tip().className).toContain('shown');
		await fireEvent.blur(screen.getByText('Crews'));
		expect(tip().className).not.toContain('shown');
	});

	it('shows the tooltip on hover and hides it on leave', async () => {
		const { container } = render(PageTitle, { route: '/crews' });
		const wrapper = container.querySelector('.page-title') as HTMLElement;
		await fireEvent.mouseEnter(wrapper);
		expect(tip().className).toContain('shown');
		await fireEvent.mouseLeave(wrapper);
		expect(tip().className).not.toContain('shown');
	});

	it('hides a hover tooltip on Escape', async () => {
		const { container } = render(PageTitle, { route: '/crews' });
		await fireEvent.mouseEnter(container.querySelector('.page-title') as HTMLElement);
		await fireEvent.keyDown(window, { key: 'Escape' });
		expect(tip().className).not.toContain('shown');
	});

	it('hides the tooltip on Escape', async () => {
		render(PageTitle, { route: '/crews' });
		const target = screen.getByText('Crews');
		await fireEvent.focus(target);
		await fireEvent.keyDown(target, { key: 'Escape' });
		expect(tip().className).not.toContain('shown');
	});
});
