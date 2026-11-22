import { fireEvent, render, screen } from '@testing-library/svelte';
import { createRawSnippet } from 'svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import ListDetailLayout from './ListDetailLayout.svelte';

const html = (text: string) => createRawSnippet(() => ({ render: () => `<p>${text}</p>` }));

function mount(props: { route?: '/moot' | '/conversations'; detailOpen?: boolean } = {}) {
	return render(ListDetailLayout, { route: props.route ?? '/moot', list: html('the list'), detail: html('the open thread'), detailOpen: props.detailOpen ?? true });
}

const toggle = () => screen.getByRole('button', { name: /the list$/i });
const column = (c: HTMLElement) => c.querySelector<HTMLElement>('#list-column')!;

beforeEach(() => localStorage.clear());
afterEach(() => vi.unstubAllGlobals());

describe('the list column toggle', () => {
	it('starts with the list shown and a button that offers to hide it', () => {
		const { container } = mount();
		expect(column(container).classList.contains('collapsed')).toBe(false);
		expect(toggle().getAttribute('aria-label')).toBe('Hide the list');
		expect(toggle().getAttribute('aria-expanded')).toBe('true');
	});

	it('hides the list so the open item takes the width, and shows it again', async () => {
		const { container } = mount();
		await fireEvent.click(toggle());
		expect(column(container).classList.contains('collapsed')).toBe(true);
		expect((column(container) as HTMLElement & { inert: boolean }).inert).toBe(true);
		expect(toggle().getAttribute('aria-label')).toBe('Show the list');
		expect(toggle().getAttribute('aria-expanded')).toBe('false');
		expect(screen.getByText('the open thread')).toBeTruthy();
		await fireEvent.click(toggle());
		expect(column(container).classList.contains('collapsed')).toBe(false);
	});

	it('is a real button, so the keyboard works it', async () => {
		mount();
		expect(toggle().tagName).toBe('BUTTON');
		expect(toggle().getAttribute('type')).toBe('button');
	});

	it('remembers the choice per page, in this browser', async () => {
		const first = mount({ route: '/moot' });
		await fireEvent.click(toggle());
		expect(localStorage.getItem('kubemoot-dashboard-list-collapsed-moot')).toBe('true');
		first.unmount();
		const again = mount({ route: '/moot' });
		await vi.waitFor(() => expect(column(again.container).classList.contains('collapsed')).toBe(true));
		again.unmount();
		const other = mount({ route: '/conversations' });
		expect(column(other.container).classList.contains('collapsed')).toBe(false);
	});

	it('keeps the list when nothing is open beside it, and offers no button then', () => {
		localStorage.setItem('kubemoot-dashboard-list-collapsed-moot', 'true');
		const { container } = mount({ detailOpen: false });
		expect(column(container).classList.contains('collapsed')).toBe(false);
		expect(screen.queryByRole('button', { name: /the list$/i })).toBeNull();
	});

	it('works when storage refuses', async () => {
		vi.stubGlobal('localStorage', {
			getItem: () => {
				throw new Error('blocked');
			},
			setItem: () => {
				throw new Error('blocked');
			}
		});
		const { container } = mount();
		await fireEvent.click(toggle());
		expect(column(container).classList.contains('collapsed')).toBe(true);
	});
});
