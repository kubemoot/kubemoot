import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest';
import ConversationActions from './ConversationActions.svelte';

type MockFetch = typeof fetch & Mock;
const mockFetch = (impl?: (url: string, init?: RequestInit) => Promise<Response>): MockFetch => vi.fn(impl) as unknown as MockFetch;
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
const report = { conversationId: 'c-1', threads: ['t1'], deleted: { history: 1, discussionMessages: 4 }, kept: ['logs and backups'] };
const calls = (f: Mock) => f.mock.calls as unknown as [string, RequestInit][];

function props(fetchFn: MockFetch, over: Record<string, unknown> = {}) {
	return {
		scope: { namespace: 'ns', crew: 'pilot' },
		conversationId: 'c-1',
		question: 'What are the two GPU temperatures?',
		running: { running: false, paused: false },
		copyText: () => 'You: hi\n\nCrew: hello',
		showDetails: false,
		ontoggledetails: vi.fn(),
		downloadHref: '/export?id=c-1',
		discussionHref: '/moot?thread=t1',
		ondeleted: vi.fn(),
		fetchFn,
		...over
	};
}

const openMenu = () => fireEvent.click(screen.getByRole('button', { name: 'Conversation actions' }));
const choose = async (name: string | RegExp) => {
	await openMenu();
	await fireEvent.click(screen.getByRole('menuitem', { name }));
};

let clipboard: ReturnType<typeof vi.fn>;
beforeEach(() => {
	clipboard = vi.fn(async () => undefined);
	Object.defineProperty(navigator, 'clipboard', { value: { writeText: clipboard }, configurable: true });
});
afterEach(() => vi.restoreAllMocks());

describe('copy actions', () => {
	it('copies the conversation', async () => {
		render(ConversationActions, props(mockFetch()));
		await choose('Copy conversation');
		expect(clipboard).toHaveBeenCalledWith('You: hi\n\nCrew: hello');
		expect(await screen.findByText('Conversation copied.')).toBeTruthy();
	});

	it('copies the conversation id with the first question', async () => {
		render(ConversationActions, props(mockFetch()));
		await choose(/id with the first question/);
		expect(clipboard).toHaveBeenCalledWith('c-1: What are the two GPU temperatures?');
	});

	it('says so when the browser refuses to copy', async () => {
		clipboard.mockRejectedValue(new Error('denied'));
		render(ConversationActions, props(mockFetch()));
		await choose('Copy conversation');
		expect((await screen.findByRole('alert')).textContent).toContain('did not allow copying');
	});
});

describe('stop, pause and resume', () => {
	it('shows none of them for a finished conversation', async () => {
		render(ConversationActions, props(mockFetch()));
		await openMenu();
		for (const name of ['Stop', 'Pause', 'Resume']) expect(screen.queryByRole('menuitem', { name })).toBeNull();
	});

	it('stops a running conversation and says so', async () => {
		const fetchFn = mockFetch(async () => json({ action: 'stop', outcome: 'stopped', message: 'Stopped.' }));
		const oncontrolled = vi.fn();
		render(ConversationActions, props(fetchFn, { running: { running: true, paused: false }, oncontrolled }));
		await choose('Stop');
		await waitFor(() => expect(calls(fetchFn)[0][0]).toBe('/api/gateway/ns/pilot/c-1/stop'));
		expect(await screen.findByText('Stopped. The crew abandoned the turn.')).toBeTruthy();
		expect(screen.queryByText(/stops at its next step/)).toBeNull();
		expect(oncontrolled).toHaveBeenCalled();
	});

	it('says a queued question was cancelled, and offers Cancel instead of Stop and Pause', async () => {
		const fetchFn = mockFetch(async () => json({ action: 'stop', outcome: 'cancelled' }));
		render(ConversationActions, props(fetchFn, { running: { running: true, paused: false, queuedOnly: true } }));
		await openMenu();
		expect(screen.queryByRole('menuitem', { name: 'Stop' })).toBeNull();
		expect(screen.queryByRole('menuitem', { name: 'Pause' })).toBeNull();
		await fireEvent.click(screen.getByRole('menuitem', { name: 'Cancel' }));
		expect(await screen.findByText(/Cancelled. The question was still queued/)).toBeTruthy();
	});

	it('says there was nothing to stop', async () => {
		const fetchFn = mockFetch(async () => json({ action: 'stop', outcome: 'nothing_to_stop' }));
		render(ConversationActions, props(fetchFn, { running: { running: true, paused: false } }));
		await choose('Stop');
		expect(await screen.findByText(/Nothing to stop/)).toBeTruthy();
	});

	it('does not claim a stop the crew did not confirm', async () => {
		const fetchFn = mockFetch(async () => json({ action: 'stop', outcome: 'unconfirmed', message: 'x' }, 202));
		render(ConversationActions, props(fetchFn, { running: { running: true, paused: false } }));
		await choose('Stop');
		expect((await screen.findByRole('alert')).textContent).toContain('did not confirm the stop');
		expect(screen.queryByText(/Stopped\./)).toBeNull();
	});

	it('tells the person to try again when the crew has just taken the question', async () => {
		const fetchFn = mockFetch(async () => json({ error: 'The crew has just taken the question and its discussion has not begun' }, 409));
		render(ConversationActions, props(fetchFn, { running: { running: true, paused: false } }));
		await choose('Stop');
		expect((await screen.findByRole('alert')).textContent).toContain('Try again in a moment');
	});

	it('pauses a running conversation and resumes a paused one', async () => {
		const fetchFn = mockFetch(async () => json({}));
		const view = render(ConversationActions, props(fetchFn, { running: { running: true, paused: false } }));
		await choose('Pause');
		await waitFor(() => expect(calls(fetchFn)[0][0]).toMatch(/\/pause$/));
		view.unmount();
		render(ConversationActions, props(fetchFn, { running: { running: true, paused: true } }));
		await choose('Resume');
		await waitFor(() => expect(calls(fetchFn)[1][0]).toMatch(/\/resume$/));
	});

	it('shows a refusal in words', async () => {
		const fetchFn = mockFetch(async () => json({ error: 'the conversation has no running discussion' }, 409));
		render(ConversationActions, props(fetchFn, { running: { running: true, paused: false } }));
		await choose('Pause');
		expect((await screen.findByRole('alert')).textContent).toContain('no running discussion');
	});
});

describe('delete', () => {
	it('asks in the page first, never with the browser dialog, and does nothing on No', async () => {
		const confirm = vi.spyOn(window, 'confirm');
		const fetchFn = mockFetch(async () => json(report));
		render(ConversationActions, props(fetchFn));
		await choose('Delete conversation');
		expect(screen.getByText(/Delete this conversation/)).toBeTruthy();
		await fireEvent.click(screen.getByRole('button', { name: 'Keep it' }));
		expect(screen.queryByRole('alertdialog')).toBeNull();
		expect(fetchFn).not.toHaveBeenCalled();
		expect(confirm).not.toHaveBeenCalled();
	});

	it('deletes on Yes and hands the erasure report to the page', async () => {
		const fetchFn = mockFetch(async () => json(report));
		const p = props(fetchFn);
		render(ConversationActions, p);
		await choose('Delete conversation');
		await fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
		await waitFor(() => expect(p.ondeleted).toHaveBeenCalledWith(report));
		expect(calls(fetchFn)[0][1].method).toBe('DELETE');
		expect(calls(fetchFn)[0][0]).toBe('/api/gateway/ns/pilot/c-1');
	});

	it('offers Stop and delete for a running conversation, and deletes with force', async () => {
		const fetchFn = mockFetch()
			.mockResolvedValueOnce(json({ error: 'the conversation has a running discussion' }, 409))
			.mockResolvedValueOnce(json(report));
		const p = props(fetchFn);
		render(ConversationActions, p);
		await choose('Delete conversation');
		await fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
		expect(await screen.findByText('This conversation is still running. Stop it and delete it?')).toBeTruthy();
		await fireEvent.click(screen.getByRole('button', { name: 'Stop and delete' }));
		await waitFor(() => expect(p.ondeleted).toHaveBeenCalledWith(report));
		expect(calls(fetchFn)[1][0]).toBe('/api/gateway/ns/pilot/c-1?force=true');
	});

	it('shows the stores that could not be cleared and offers to repeat the delete', async () => {
		const fetchFn = mockFetch()
			.mockResolvedValueOnce(json({ ...report, failed: ['workspace'] }, 500))
			.mockResolvedValueOnce(json(report));
		const p = props(fetchFn);
		render(ConversationActions, p);
		await choose('Delete conversation');
		await fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
		expect((await screen.findByRole('alert')).textContent).toContain('workspace could not be cleared');
		expect(p.ondeleted).not.toHaveBeenCalled();
		await fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
		await waitFor(() => expect(p.ondeleted).toHaveBeenCalledWith(report));
	});
});

describe('acting on someone else\'s conversation', () => {
	const needReason = () =>
		json({ error: "The crew's gateway answered 400. (acting on another caller's conversations needs a reason of 1 to 200 characters in the Kubemoot-Access-Reason header)" }, 400);

	it('asks for a reason, will not continue without one, and sends it with the action', async () => {
		const fetchFn = mockFetch().mockResolvedValueOnce(needReason()).mockResolvedValueOnce(json({ action: 'stop', outcome: 'stopped' }));
		render(ConversationActions, props(fetchFn, { running: { running: true, paused: false } }));
		await choose('Stop');
		const field = await screen.findByLabelText(/Say why you are acting/);
		const go = screen.getByRole<HTMLButtonElement>('button', { name: 'Continue' });
		expect(go.disabled).toBe(true);
		await fireEvent.input(field, { target: { value: '   ' } });
		expect(go.disabled).toBe(true);
		await fireEvent.input(field, { target: { value: 'user reported a stuck run' } });
		expect(go.disabled).toBe(false);
		await fireEvent.click(go);
		await waitFor(() => expect(calls(fetchFn)).toHaveLength(2));
		expect(calls(fetchFn)[1][1].headers).toEqual({ 'Kubemoot-Access-Reason': 'user reported a stuck run' });
		expect(await screen.findByText(/Stopped\./)).toBeTruthy();
	});

	it('limits the reason to 200 characters', async () => {
		const fetchFn = mockFetch().mockResolvedValueOnce(needReason());
		render(ConversationActions, props(fetchFn));
		await choose('Delete conversation');
		await fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
		const field = await screen.findByLabelText<HTMLInputElement>(/Say why you are deleting/);
		expect(field.maxLength).toBe(200);
	});

	it('shows the dashboard\'s words for a conversation that is not theirs', async () => {
		const fetchFn = mockFetch(async () => json({ error: 'Not found, or not yours: you can only see conversations you started.' }, 404));
		render(ConversationActions, props(fetchFn));
		await choose('Delete conversation');
		await fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
		expect((await screen.findByRole('alert')).textContent).toContain('not yours');
	});
});

describe('the menu items that need no server', () => {
	it('toggles the answer details and links to the Moot and the download', async () => {
		const p = props(mockFetch());
		render(ConversationActions, p);
		await openMenu();
		expect(screen.getByRole('menuitem', { name: 'View associated moot' }).getAttribute('href')).toBe('/moot?thread=t1');
		expect(screen.getByRole('menuitem', { name: 'Download conversation' }).getAttribute('href')).toBe('/export?id=c-1');
		await fireEvent.click(screen.getByRole('menuitemcheckbox', { name: /Show answer details/ }));
		expect(p.ontoggledetails).toHaveBeenCalled();
	});
});
