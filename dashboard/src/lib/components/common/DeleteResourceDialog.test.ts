import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import DeleteResourceDialog from './DeleteResourceDialog.svelte';

const answer = (status: number, body: unknown = {}) => vi.fn().mockResolvedValue(new Response(JSON.stringify(body), { status }));

function mount(fetchFn: typeof fetch, kind: 'crew' | 'suite' = 'suite') {
	const onclose = vi.fn();
	const ondeleted = vi.fn();
	render(DeleteResourceDialog, {
		props: {
			kind,
			namespace: 'crew-a',
			name: 'gate-run',
			question: 'Delete the fitness run gate-run?',
			goes: ['The run gate-run and its iteration results', 'Its XLSX report'],
			onclose,
			ondeleted,
			fetchFn
		}
	});
	return { onclose, ondeleted };
}

describe('DeleteResourceDialog', () => {
	it('asks first, names what goes, and puts the focus on the dialog, not on a button', () => {
		mount(answer(200));
		const dialog = screen.getByRole('alertdialog');
		expect(dialog.textContent).toContain('Delete the fitness run gate-run?');
		expect(dialog.textContent).toContain('The run gate-run and its iteration results');
		expect(dialog.textContent).toContain('Its XLSX report');
		expect(dialog.textContent).toContain('This cannot be undone.');
		expect(document.activeElement).toBe(dialog);
	});

	it('deletes nothing until Delete is pressed', () => {
		const fetchFn = answer(200);
		mount(fetchFn);
		expect(fetchFn).not.toHaveBeenCalled();
	});

	it('deletes as a DELETE request and tells the page when it is done', async () => {
		const fetchFn = answer(200, { deleted: true });
		const { ondeleted, onclose } = mount(fetchFn);
		await fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
		await waitFor(() => expect(ondeleted).toHaveBeenCalledOnce());
		expect(fetchFn).toHaveBeenCalledWith('/api/kubemoot/crewfitnesssuites/crew-a/gate-run', { method: 'DELETE' });
		expect(onclose).not.toHaveBeenCalled();
	});

	it('says plainly that the account may not delete it on a 403, and keeps the resource', async () => {
		const { ondeleted } = mount(answer(403, { error: 'You do not have permission to delete this fitness run. Your Kubernetes account is not allowed to; ask the cluster administrator for access.' }));
		await fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
		const alert = await screen.findByRole('alert');
		expect(alert.textContent).toContain('You do not have permission to delete this fitness run');
		expect(ondeleted).not.toHaveBeenCalled();
		expect(screen.getByRole('button', { name: 'Close' })).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Try again' })).toBeTruthy();
	});

	it('says it no longer exists on a 404', async () => {
		mount(answer(404, { error: 'This fitness run no longer exists.' }));
		await fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
		expect((await screen.findByRole('alert')).textContent).toBe('This fitness run no longer exists.');
	});

	it('uses the crew address for a crew', async () => {
		const fetchFn = answer(200, { deleted: true });
		mount(fetchFn, 'crew');
		await fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
		await waitFor(() => expect(fetchFn).toHaveBeenCalledWith('/api/kubemoot/crews/gate-run?namespace=crew-a', { method: 'DELETE' }));
	});

	it('closes on Keep it, on Escape and on a click outside the dialog', async () => {
		const first = mount(answer(200));
		await fireEvent.click(screen.getByRole('button', { name: 'Keep it' }));
		expect(first.onclose).toHaveBeenCalledOnce();
		await fireEvent.keyDown(window, { key: 'Escape' });
		expect(first.onclose).toHaveBeenCalledTimes(2);
		const backdrop = document.querySelector('.backdrop') as HTMLElement;
		await fireEvent.pointerDown(backdrop);
		expect(first.onclose).toHaveBeenCalledTimes(3);
		await fireEvent.pointerDown(screen.getByRole('alertdialog'));
		expect(first.onclose).toHaveBeenCalledTimes(3);
	});

	it('sends one delete when Delete is pressed twice', async () => {
		let release: (r: Response) => void = () => undefined;
		const fetchFn = vi.fn().mockReturnValue(new Promise<Response>((r) => (release = r)));
		mount(fetchFn);
		const button = screen.getByRole('button', { name: 'Delete' });
		await fireEvent.click(button);
		await fireEvent.click(button);
		expect(fetchFn).toHaveBeenCalledOnce();
		release(new Response('{}', { status: 200 }));
	});
});
