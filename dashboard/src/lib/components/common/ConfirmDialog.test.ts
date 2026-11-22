import { fireEvent, render, screen } from '@testing-library/svelte';
import { createRawSnippet } from 'svelte';
import { describe, expect, it, vi } from 'vitest';
import ConfirmDialog from './ConfirmDialog.svelte';

function mount(over: Record<string, unknown> = {}) {
	const onconfirm = vi.fn();
	const onclose = vi.fn();
	render(ConfirmDialog, { props: { question: 'Delete it?', goes: ['The thing'], onconfirm, onclose, ...over } });
	return { onconfirm, onclose };
}

describe('ConfirmDialog', () => {
	it('is a modal alert dialog named by its question, holding the focus itself', () => {
		mount();
		const dialog = screen.getByRole('alertdialog', { name: 'Delete it?' });
		expect(dialog.getAttribute('aria-modal')).toBe('true');
		expect(document.activeElement).toBe(dialog);
	});

	it('keeps Tab inside the dialog, wrapping from the last button to the first and back', async () => {
		mount();
		const keep = screen.getByRole('button', { name: 'Keep it' });
		const del = screen.getByRole('button', { name: 'Delete' });
		del.focus();
		await fireEvent.keyDown(del, { key: 'Tab' });
		expect(document.activeElement).toBe(keep);
		await fireEvent.keyDown(keep, { key: 'Tab', shiftKey: true });
		expect(document.activeElement).toBe(del);
	});

	it('pulls a Tab from outside the dialog back in', async () => {
		mount();
		const outside = document.createElement('button');
		document.body.append(outside);
		outside.focus();
		await fireEvent.keyDown(window, { key: 'Tab' });
		expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Keep it' }));
		outside.remove();
	});

	it('gives the focus back to the control that opened it when it goes', () => {
		const opener = document.createElement('button');
		document.body.append(opener);
		opener.focus();
		const view = render(ConfirmDialog, { props: { question: 'Delete it?', goes: [], onconfirm: vi.fn(), onclose: vi.fn() } });
		expect(document.activeElement).not.toBe(opener);
		view.unmount();
		expect(document.activeElement).toBe(opener);
		opener.remove();
	});

	it('cannot be dismissed or pressed again while the delete is under way', async () => {
		const { onclose, onconfirm } = mount({ busy: true });
		await fireEvent.keyDown(window, { key: 'Escape' });
		await fireEvent.pointerDown(document.querySelector('.backdrop') as HTMLElement);
		expect(onclose).not.toHaveBeenCalled();
		expect(screen.getByRole<HTMLButtonElement>('button', { name: 'Keep it' }).disabled).toBe(true);
		const working = screen.getByRole<HTMLButtonElement>('button', { name: 'Deleting...' });
		expect(working.disabled).toBe(true);
		await fireEvent.click(working);
		expect(onconfirm).not.toHaveBeenCalled();
	});

	it('says Try again and Close after a refusal, and shows the refusal', () => {
		mount({ refusal: 'You do not have permission.' });
		expect(screen.getByRole('alert').textContent).toBe('You do not have permission.');
		expect(screen.getByRole('button', { name: 'Try again' })).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Close' })).toBeTruthy();
	});

	it('uses the confirm words it is given, and keeps the button off until it can confirm', () => {
		mount({ confirmLabel: 'Stop and delete', canConfirm: false });
		expect(screen.getByRole<HTMLButtonElement>('button', { name: 'Stop and delete' }).disabled).toBe(true);
	});

	it('shows what the question needs besides the list', () => {
		const extra = createRawSnippet(() => ({ render: () => '<label>Reason<input /></label>' }));
		mount({ extra });
		expect(screen.getByLabelText('Reason')).toBeTruthy();
	});
});
