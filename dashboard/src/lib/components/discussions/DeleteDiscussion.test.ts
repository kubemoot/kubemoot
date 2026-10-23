import { fireEvent, render, screen } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import DeleteDiscussion from './DeleteDiscussion.svelte';
import { DELETE_TOOLTIP } from '#lib/discussion-actions.js';

describe('DeleteDiscussion', () => {
	it('shows one trash icon with the shared tooltip and no question yet', () => {
		render(DeleteDiscussion, { onconfirm: vi.fn() });
		const btn = screen.getByRole('button', { name: 'Delete discussion' });
		expect(btn.getAttribute('title')).toBe(DELETE_TOOLTIP);
		expect(screen.queryByText('Delete?')).toBeNull();
	});

	it('asks in the page and deletes only after Yes', async () => {
		const onconfirm = vi.fn();
		render(DeleteDiscussion, { onconfirm });
		await fireEvent.click(screen.getByRole('button', { name: 'Delete discussion' }));
		expect(screen.getByText('Delete?')).toBeTruthy();
		expect(onconfirm).not.toHaveBeenCalled();
		await fireEvent.click(screen.getByRole('button', { name: 'Yes' }));
		expect(onconfirm).toHaveBeenCalledTimes(1);
		expect(screen.queryByText('Delete?')).toBeNull();
	});

	it('No cancels without deleting', async () => {
		const onconfirm = vi.fn();
		render(DeleteDiscussion, { onconfirm });
		await fireEvent.click(screen.getByRole('button', { name: 'Delete discussion' }));
		await fireEvent.click(screen.getByRole('button', { name: 'No' }));
		expect(onconfirm).not.toHaveBeenCalled();
		expect(screen.getByRole('button', { name: 'Delete discussion' })).toBeTruthy();
	});

	it('does not let the click reach a parent handler', async () => {
		const parent = vi.fn();
		render(DeleteDiscussion, { onconfirm: vi.fn() });
		document.body.addEventListener('click', parent);
		await fireEvent.click(screen.getByRole('button', { name: 'Delete discussion' }));
		document.body.removeEventListener('click', parent);
		expect(parent).not.toHaveBeenCalled();
	});
});
