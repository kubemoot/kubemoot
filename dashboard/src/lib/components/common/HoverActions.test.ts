import { fireEvent, render, screen } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import HoverActions from './HoverActions.svelte';
import type { HoverAction } from '#lib/hover-actions.js';

const action = (over: Partial<HoverAction> = {}): HoverAction => ({ label: 'Copy message', id: 'copy-message', icon: 'copy', run: vi.fn(), ...over });

describe('HoverActions', () => {
	it('is a labelled toolbar of icon buttons, each with an accessible name', () => {
		render(HoverActions, { props: { label: 'Message actions', actions: [action(), action({ label: 'Edit question', id: 'edit-question', icon: 'edit' })] } });
		const bar = screen.getByRole('toolbar', { name: 'Message actions' });
		expect(bar).toBeTruthy();
		expect(screen.getAllByRole('button').map((b) => b.getAttribute('aria-label'))).toEqual(['Copy message', 'Edit question']);
	});

	it('keeps the buttons in the page for the keyboard while the bar is hidden', () => {
		render(HoverActions, { props: { label: 'Message actions', actions: [action()] } });
		const button = screen.getByRole('button', { name: 'Copy message' });
		button.focus();
		expect(document.activeElement).toBe(button);
	});

	it('runs the action on click', async () => {
		const run = vi.fn();
		render(HoverActions, { props: { label: 'Row actions', actions: [action({ run })] } });
		await fireEvent.click(screen.getByRole('button', { name: 'Copy message' }));
		expect(run).toHaveBeenCalledOnce();
	});

	it('shows a tick after a copy that worked, and says so for assistive technology', async () => {
		render(HoverActions, { props: { label: 'Message actions', actions: [action()] } });
		await fireEvent.click(screen.getByRole('button', { name: 'Copy message' }));
		await vi.waitFor(() => expect(screen.getByRole('status').textContent).toBe('Copy message: done'));
		expect(document.querySelector('polyline')).toBeTruthy();
	});

	it('shows no tick when the copy is refused', async () => {
		render(HoverActions, { props: { label: 'Message actions', actions: [action({ run: () => Promise.reject(new Error('no')) })] } });
		await fireEvent.click(screen.getByRole('button', { name: 'Copy message' }));
		await Promise.resolve();
		expect(screen.getByRole('status').textContent).toBe('');
	});

	it('shows no tick for Edit or Delete, which are not copies', async () => {
		render(HoverActions, { props: { label: 'Row actions', actions: [action({ label: 'Delete', id: 'delete-conversation', icon: 'delete', danger: true })] } });
		await fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
		await Promise.resolve();
		expect(screen.getByRole('status').textContent).toBe('');
	});

	it('marks a danger action so it reads as one', () => {
		render(HoverActions, { props: { label: 'Row actions', actions: [action({ label: 'Delete', id: 'delete-conversation', icon: 'delete', danger: true })] } });
		expect(screen.getByRole('button', { name: 'Delete' }).classList.contains('danger')).toBe(true);
	});
});
