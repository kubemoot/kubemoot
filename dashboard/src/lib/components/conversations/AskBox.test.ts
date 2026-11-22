import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { describe, expect, it, vi, type Mock } from 'vitest';
import AskBox from './AskBox.svelte';

type MockFetch = typeof fetch & Mock;
const mockFetch = (impl?: (url: string, init?: RequestInit) => Promise<Response>): MockFetch => vi.fn(impl) as unknown as MockFetch;
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
const calls = (f: Mock) => f.mock.calls as unknown as [string, RequestInit][];
const scope = { namespace: 'ns', crew: 'pilot' };
const stream = () => new Response('data: {"type":"done"}\n\n');

const type = (text: string) => fireEvent.input(screen.getByLabelText('Your question'), { target: { value: text } });

describe('AskBox', () => {
	it('starts a conversation: posts the question, then reports its id', async () => {
		const fetchFn = mockFetch(async (url: string) => (url.endsWith('/ask') ? json({ conversationId: 'c-9', requestedAt: 't' }) : stream()));
		const onAsked = vi.fn();
		render(AskBox, { scope, fetchFn, onAsked });
		await type('What are the two GPU temperatures?');
		await fireEvent.click(screen.getByRole('button', { name: 'Ask' }));
		await waitFor(() => expect(onAsked).toHaveBeenCalledWith('c-9', 'What are the two GPU temperatures?', 't'));
		const body = JSON.parse(calls(fetchFn)[0][1].body as string);
		expect(body.message).toBe('What are the two GPU temperatures?');
		expect(body.conversationId).toBeUndefined();
	});

	it('follows up in the open conversation', async () => {
		const fetchFn = mockFetch(async (url: string) => (url.endsWith('/ask') ? json({ conversationId: 'c-1' }) : stream()));
		render(AskBox, { scope, fetchFn, conversationId: 'c-1' });
		await type('Which one is hotter?');
		await fireEvent.click(screen.getByRole('button', { name: 'Send reply' }));
		await waitFor(() => expect(fetchFn).toHaveBeenCalled());
		expect(JSON.parse(calls(fetchFn)[0][1].body as string)).toMatchObject({ message: 'Which one is hotter?', conversationId: 'c-1' });
	});

	it('sends on Enter, and keeps Shift+Enter for a new line', async () => {
		const fetchFn = mockFetch(async (url: string) => (url.endsWith('/ask') ? json({ conversationId: 'c-1' }) : stream()));
		render(AskBox, { scope, fetchFn });
		await type('hello');
		await fireEvent.keyDown(screen.getByLabelText('Your question'), { key: 'Enter', shiftKey: true });
		expect(fetchFn).not.toHaveBeenCalled();
		await fireEvent.keyDown(screen.getByLabelText('Your question'), { key: 'Enter' });
		await waitFor(() => expect(fetchFn).toHaveBeenCalled());
	});

	it('sends nothing for a blank question and shows a refusal in words', async () => {
		const fetchFn = mockFetch(async () => json({ error: 'You do not have permission to use this crew.' }, 403));
		render(AskBox, { scope, fetchFn });
		expect(screen.getByRole<HTMLButtonElement>('button', { name: 'Ask' }).disabled).toBe(true);
		await type('hello');
		await fireEvent.click(screen.getByRole('button', { name: 'Ask' }));
		expect((await screen.findByRole('alert')).textContent).toContain('permission');
	});

	it('asks for a crew when none is selected, with no field and no kmctl text', () => {
		render(AskBox, { scope: null, fetchFn: mockFetch() });
		expect(screen.getByText(/Select a crew in the top bar/)).toBeTruthy();
		expect(screen.queryByLabelText('Your question')).toBeNull();
		expect(document.body.textContent).not.toContain('kmctl');
	});
});
