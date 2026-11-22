import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { describe, expect, it, vi, type Mock } from 'vitest';
import ConversationDeleteDialog from './ConversationDeleteDialog.svelte';

type MockFetch = typeof fetch & Mock;
const reply = (status: number, body: unknown) => new Response(JSON.stringify(body), { status });
const report = { conversationId: 'c-1', threads: ['t-1'], deleted: { history: 2 }, kept: [] };
const scope = { namespace: 'crew-a', crew: 'pilot' };

function mount(answers: (() => Response)[]) {
	const queue = [...answers];
	const fetchFn = vi.fn(async () => (queue.length > 1 ? queue.shift()! : queue[0])()) as unknown as MockFetch;
	const onclose = vi.fn();
	const ondeleted = vi.fn();
	render(ConversationDeleteDialog, { props: { scope, conversationId: 'c-1', title: 'Which GPU is hotter?', onclose, ondeleted, fetchFn } });
	return { fetchFn, onclose, ondeleted };
}

const press = (name: string) => fireEvent.click(screen.getByRole('button', { name }));
const urls = (f: Mock) => (f.mock.calls as unknown as [string, RequestInit][]).map(([u, i]) => `${i?.method} ${u}`);

describe('ConversationDeleteDialog', () => {
	it('asks first and names the conversation, deleting nothing yet', () => {
		const { fetchFn } = mount([() => reply(200, report)]);
		const text = screen.getByRole('alertdialog').textContent ?? '';
		expect(text).toContain('Delete this conversation?');
		expect(text).toContain('The conversation "Which GPU is hotter?"');
		expect(fetchFn).not.toHaveBeenCalled();
	});

	it('deletes as the person through the crew gateway and hands the report to the page', async () => {
		const { fetchFn, ondeleted } = mount([() => reply(200, report)]);
		await press('Delete');
		await waitFor(() => expect(ondeleted).toHaveBeenCalledWith(report));
		expect(urls(fetchFn)).toEqual(['DELETE /api/gateway/crew-a/pilot/c-1']);
	});

	it('offers to stop a running conversation first, then deletes with force', async () => {
		const { fetchFn, ondeleted } = mount([() => reply(409, { error: 'still running' }), () => reply(200, report)]);
		await press('Delete');
		expect(await screen.findByText('This conversation is still running. Stop it and delete it?')).toBeTruthy();
		await press('Stop and delete');
		await waitFor(() => expect(ondeleted).toHaveBeenCalledWith(report));
		expect(urls(fetchFn)).toEqual(['DELETE /api/gateway/crew-a/pilot/c-1', 'DELETE /api/gateway/crew-a/pilot/c-1?force=true']);
	});

	it('asks for a reason for someone else\'s conversation and sends it once given', async () => {
		const { fetchFn, ondeleted } = mount([() => reply(400, { error: 'A reason is required to act on this conversation' }), () => reply(200, report)]);
		await press('Delete');
		const field = await screen.findByLabelText(/Say why you are deleting it/);
		expect(screen.getByRole<HTMLButtonElement>('button', { name: 'Delete' }).disabled).toBe(true);
		await fireEvent.input(field, { target: { value: 'asked by the owner' } });
		await press('Delete');
		await waitFor(() => expect(ondeleted).toHaveBeenCalled());
		expect(((fetchFn.mock.calls as unknown as [string, RequestInit][])[1][1].headers as Record<string, string>)['Kubemoot-Access-Reason']).toBe('asked by the owner');
	});

	it('says plainly that the account may not delete it on a 403, and keeps the conversation', async () => {
		const { ondeleted } = mount([() => reply(403, { error: 'You do not have permission to use this crew.' })]);
		await press('Delete');
		expect((await screen.findByRole('alert')).textContent).toContain('You do not have permission');
		expect(ondeleted).not.toHaveBeenCalled();
	});

	it('says plainly that it is gone on a 404', async () => {
		mount([() => reply(404, { error: 'This conversation no longer exists.' })]);
		await press('Delete');
		expect((await screen.findByRole('alert')).textContent).toBe('This conversation no longer exists.');
	});

	it('names the stores that could not be cleared and offers to try again', async () => {
		const partial = { conversationId: 'c-1', deleted: { history: 1 }, kept: [], failed: ['discussionMessages'] };
		mount([() => reply(500, partial)]);
		await press('Delete');
		expect((await screen.findByRole('alert')).textContent).toContain('discussion messages could not be cleared');
		expect(screen.getByRole('button', { name: 'Try again' })).toBeTruthy();
	});

	it('closes without deleting when the person keeps it', async () => {
		const { fetchFn, onclose } = mount([() => reply(200, report)]);
		await press('Keep it');
		expect(onclose).toHaveBeenCalledOnce();
		expect(fetchFn).not.toHaveBeenCalled();
	});
});
