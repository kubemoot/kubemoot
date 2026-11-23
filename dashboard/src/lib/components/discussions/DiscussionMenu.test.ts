import { fireEvent, render, screen } from '@testing-library/svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import DiscussionMenu from './DiscussionMenu.svelte';

const base = () => ({
	showTimeline: false, showDebug: true,
	ontoggletimeline: vi.fn(), ontoggledebug: vi.fn(), oncopydiscussion: vi.fn(), oncopythreadid: vi.fn(),
	downloadHref: '/api/kubemoot/conversation-export?id=x'
});
const menuItems = () => [...document.querySelectorAll<HTMLElement>('[role^="menuitem"]')];
const trigger = () => screen.getByRole('button', { name: 'Actions' });

afterEach(() => vi.restoreAllMocks());

describe('DiscussionMenu', () => {
	it('is one hamburger button that starts closed', () => {
		render(DiscussionMenu, base());
		expect(trigger().getAttribute('aria-haspopup')).toBe('menu');
		expect(trigger().getAttribute('aria-expanded')).toBe('false');
		expect(screen.queryByRole('menu')).toBeNull();
	});

	it('lists every action when opened', async () => {
		render(DiscussionMenu, base());
		await fireEvent.click(trigger());
		expect(trigger().getAttribute('aria-expanded')).toBe('true');
		const names = menuItems().map((e) => e.textContent?.trim());
		expect(names).toEqual([
			'Show timeline', 'Debug detail', 'Copy discussion', 'Copy thread id with the question',
			'Download conversation'
		]);
		expect(screen.getByRole('menuitemcheckbox', { name: /Show timeline/ }).getAttribute('aria-checked')).toBe('false');
		expect(screen.getByRole('menuitemcheckbox', { name: /Debug detail/ }).getAttribute('aria-checked')).toBe('true');
		expect(screen.getByRole('menuitem', { name: 'Download conversation' }).getAttribute('href')).toContain('conversation-export');
	});

	it('runs a toggle and closes', async () => {
		const props = base();
		render(DiscussionMenu, props);
		await fireEvent.click(trigger());
		await fireEvent.click(screen.getByRole('menuitemcheckbox', { name: /Show timeline/ }));
		expect(props.ontoggletimeline).toHaveBeenCalledOnce();
		expect(screen.queryByRole('menu')).toBeNull();
	});

	it('omits download when there is no conversation, and never offers a kmctl command', async () => {
		render(DiscussionMenu, { ...base(), downloadHref: null });
		await fireEvent.click(trigger());
		expect(screen.queryByText('Download conversation')).toBeNull();
		expect(screen.queryByText(/kmctl/)).toBeNull();
	});

	it('moves with the arrow keys, wraps, and closes on Escape back to the button', async () => {
		render(DiscussionMenu, base());
		trigger().focus();
		await fireEvent.keyDown(trigger(), { key: 'ArrowDown' });
		const items = menuItems();
		await vi.waitFor(() => expect(document.activeElement).toBe(items[0]));
		await fireEvent.keyDown(screen.getByRole('menu'), { key: 'ArrowDown' });
		expect(document.activeElement).toBe(items[1]);
		await fireEvent.keyDown(screen.getByRole('menu'), { key: 'ArrowUp' });
		await fireEvent.keyDown(screen.getByRole('menu'), { key: 'ArrowUp' });
		expect(document.activeElement).toBe(items.at(-1));
		await fireEvent.keyDown(screen.getByRole('menu'), { key: 'Home' });
		expect(document.activeElement).toBe(items[0]);
		await fireEvent.keyDown(screen.getByRole('menu'), { key: 'Escape' });
		expect(screen.queryByRole('menu')).toBeNull();
		expect(document.activeElement).toBe(trigger());
		expect(trigger().getAttribute('aria-expanded')).toBe('false');
	});

	it('closes on a click outside', async () => {
		render(DiscussionMenu, base());
		await fireEvent.click(trigger());
		await fireEvent.click(document.body);
		expect(screen.queryByRole('menu')).toBeNull();
	});

	it('links to the associated conversation only when the thread has one', async () => {
		render(DiscussionMenu, { ...base(), conversationHref: '/conversations?conversation=c-1' });
		await fireEvent.click(trigger());
		expect(screen.getByRole('menuitem', { name: 'View associated conversation' }).getAttribute('href')).toBe('/conversations?conversation=c-1');
	});

	it('offers no conversation link for a thread without one', async () => {
		render(DiscussionMenu, base());
		await fireEvent.click(trigger());
		expect(screen.queryByRole('menuitem', { name: 'View associated conversation' })).toBeNull();
	});
});
