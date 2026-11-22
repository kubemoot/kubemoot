import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { handleMessage } from '#lib/stores/discussions.js';
import { crewDirectory } from '#lib/stores/crews.js';
import { namespace } from '#lib/stores/namespace.js';
import { page, setPageData, setPageUrl } from '../../../vitest-mocks/app-state.svelte';
import { get } from 'svelte/store';
import type { DiscussionMessage } from '#lib/types/kubemoot.js';
import Page from './+page.svelte';


function setPage(address: string, data: Record<string, unknown> = {}) {
	setPageUrl(address);
	setPageData(data);
}

let seq = 0;
function msg(threadId: string, agentName: string, messageType: DiscussionMessage['messageType'], content: string, at: string, over: Partial<DiscussionMessage> = {}): DiscussionMessage {
	seq += 1;
	return { messageId: `m${seq}`, threadId, agentName, messageType, content, channel: 'general', timestamp: `2026-11-21T${at}Z`, ...over };
}

/** A conversation the way the gateway opens it: the crew's coordinator starts the thread with the question. */
function conversation(threadId: string, question: string, answer: string, at: string) {
	const subject = `kubemoot.discuss.crew-demo.demo.general.${threadId}`;
	const meta = { crew: 'demo', conversationId: `c-${threadId}` };
	handleMessage(msg(threadId, 'demo-coordinator', 'thread_start', question, at, { metadata: meta }), subject);
	handleMessage(msg(threadId, 'demo-coordinator', 'synthesis', answer, at.replace(':00', ':09')), subject);
	handleMessage(msg(threadId, 'demo-coordinator', 'thread_close', '', at.replace(':00', ':10')), subject);
}

beforeAll(() => {
	vi.stubGlobal('EventSource', class { close() {} addEventListener() {} onmessage = null; onerror = null; onopen = null; });
	vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, json: async () => ({ items: [], conversations: [], messages: [] }) }));
	conversation('t-ns', 'Which namespace is the pod in?', 'It runs in the default namespace.\n\n1. first\n2. second', '10:00:00');
	conversation('t-smoke', 'Smoke check smoke-1: reply with one short sentence in the namespace', 'ok', '11:00:00');
	conversation('t-gpu', 'What are the two GPU temperatures?', 'They are 33C and 36C.', '12:00:00');
});

beforeEach(() => {
	namespace.set('');
	crewDirectory.set([{ namespace: 'crew-demo', crew: 'demo', displayName: 'Demo' }] as never);
	setPage('/dashboard/conversations', { user: { username: 'alice' } });
});

const titles = (container: HTMLElement) => [...container.querySelectorAll('.card .query')].map((e) => e.textContent?.trim());

describe('the Conversations list', () => {
	it('titles each conversation by its first question, never by the last answer', async () => {
		const { container } = render(Page);
		await waitFor(() => expect(titles(container)).toHaveLength(3));
		expect(titles(container)).toEqual(
			expect.arrayContaining(['Which namespace is the pod in?', 'What are the two GPU temperatures?'])
		);
		expect(titles(container).join(' ')).not.toMatch(/It runs in the default|They are 33C/);
	});

	it('drops the smoke checks for the search "namespace -smoke"', async () => {
		setPage('/dashboard/conversations?q=namespace+-smoke', { user: { username: 'alice' } });
		const { container } = render(Page);
		await waitFor(() => expect(titles(container)).toEqual(['Which namespace is the pod in?']));
	});

	it('hides only the smoke checks for "-smoke" alone', async () => {
		setPage('/dashboard/conversations?q=-smoke', { user: { username: 'alice' } });
		const { container } = render(Page);
		await waitFor(() => expect(titles(container)).toHaveLength(2));
		expect(titles(container).join(' ')).not.toMatch(/Smoke/);
	});

	it('finds a quoted phrase and says so when nothing matches', async () => {
		setPage('/dashboard/conversations?q=%22gpu+temperatures%22', { user: { username: 'alice' } });
		const first = render(Page);
		await waitFor(() => expect(titles(first.container)).toEqual(['What are the two GPU temperatures?']));
		first.unmount();
		setPage('/dashboard/conversations?q=zebra', { user: { username: 'alice' } });
		render(Page);
		expect(await screen.findByText('No conversations match this search.')).toBeTruthy();
	});

	it('has the search help as a ? tooltip, not a box of prose', () => {
		const { container } = render(Page);
		expect(container.querySelector('.help-btn')).toBeTruthy();
		expect(container.textContent).not.toContain('Words must all match');
	});
});

describe('an open conversation', () => {
	it('shows the question first, then the answer, with the list inside the bubble', async () => {
		const { container } = render(Page);
		await waitFor(() => expect(container.querySelectorAll('.card').length).toBe(3));
		const row = [...container.querySelectorAll<HTMLElement>('.card')].find((r) => r.textContent?.includes('namespace is the pod'));
		await fireEvent.click(row as HTMLElement);
		const bubbles = [...container.querySelectorAll('.bubble')];
		expect(bubbles.map((b) => b.className.replace('bubble ', '').trim().split(' ')[0])).toEqual(['you', 'crew']);
		expect(bubbles[0].textContent).toContain('Which namespace is the pod in?');
		expect(bubbles[1].querySelector('.markdown ol')).toBeTruthy();
	});

	it('has one actions menu and no kmctl box, text button or details toggle', async () => {
		const { container } = render(Page);
		await waitFor(() => expect(container.querySelectorAll('.card').length).toBe(3));
		await fireEvent.click(container.querySelector('.card') as HTMLElement);
		expect(screen.getAllByRole('button', { name: 'Conversation actions' })).toHaveLength(1);
		expect(container.textContent).not.toMatch(/kmctl|from a terminal/i);
		expect(screen.queryByText('Details')).toBeNull();
		expect(screen.queryByRole('link', { name: 'Discussion' })).toBeNull();
		expect(screen.queryByRole('link', { name: 'Download' })).toBeNull();
	});

	it('removes a deleted conversation from the list and shows what was removed', async () => {
		const report = { conversationId: 'c-t-gpu', threads: ['t-gpu'], deleted: { history: 1, discussionMessages: 3 }, kept: ['logs and backups'] };
		vi.stubGlobal('fetch', vi.fn(async (_url: string, init?: RequestInit) => (init?.method === 'DELETE' ? new Response(JSON.stringify(report)) : new Response(JSON.stringify({ conversations: [] })))));
		const { container } = render(Page);
		await waitFor(() => expect(container.querySelectorAll('.card').length).toBe(3));
		await fireEvent.click([...container.querySelectorAll<HTMLElement>('.card')].find((r) => r.textContent?.includes('GPU temperatures')) as HTMLElement);
		await fireEvent.click(screen.getByRole('button', { name: 'Conversation actions' }));
		await fireEvent.click(screen.getByRole('menuitem', { name: 'Delete conversation' }));
		await fireEvent.click(screen.getByRole('button', { name: 'Yes, delete' }));
		expect(await screen.findByText('Conversation deleted')).toBeTruthy();
		expect(screen.getByText('3 discussion messages')).toBeTruthy();
		expect(titles(container).join(' ')).not.toContain('GPU temperatures');
	});
});

describe('starting a conversation', () => {
	it('offers + New with a compact question field and no terminal hint', async () => {
		namespace.set('crew-demo');
		const { container } = render(Page);
		await fireEvent.click(screen.getByRole('button', { name: '+ New' }));
		expect(await screen.findByText('New conversation with Demo')).toBeTruthy();
		expect(screen.getByLabelText('Your question')).toBeTruthy();
		expect(container.textContent).not.toMatch(/kmctl|terminal/i);
	});

	it('asks which crew when none is selected', async () => {
		render(Page);
		await fireEvent.click(screen.getByRole('button', { name: '+ New' }));
		expect(await screen.findByLabelText('Crew to ask')).toBeTruthy();
	});

	it('shows no ask field and no + New to a visitor who is not signed in', () => {
		setPage('/dashboard/conversations', {});
		render(Page);
		expect(screen.queryByRole('button', { name: '+ New' })).toBeNull();
		expect(screen.queryByLabelText('Your question')).toBeNull();
	});
});

describe('the page heading', () => {
	it('says what the page is in one muted line', () => {
		render(Page);
		expect(screen.getByRole('heading', { name: 'Conversations' })).toBeTruthy();
		expect(screen.getByText("Your questions and the crew's answers.")).toBeTruthy();
	});
});

describe('a conversation with a follow-up', () => {
	it('is one entry in the list, and opens with both questions and both answers in order', async () => {
		const meta = { crew: 'demo', conversationId: 'c-multi' };
		const subject = (id: string) => `kubemoot.discuss.crew-demo.demo.general.${id}`;
		handleMessage(msg('t-m1', 'demo-coordinator', 'thread_start', 'Zebra first question?', '13:00:00', { metadata: meta }), subject('t-m1'));
		handleMessage(msg('t-m1', 'demo-coordinator', 'synthesis', 'Zebra first answer.', '13:00:20'), subject('t-m1'));
		handleMessage(msg('t-m1', 'demo-coordinator', 'thread_close', '', '13:00:21'), subject('t-m1'));
		handleMessage(msg('t-m2', 'demo-coordinator', 'thread_start', 'Zebra follow-up?', '13:01:00', { metadata: meta }), subject('t-m2'));
		handleMessage(msg('t-m2', 'demo-coordinator', 'synthesis', 'Zebra second answer.', '13:01:20'), subject('t-m2'));
		handleMessage(msg('t-m2', 'demo-coordinator', 'thread_close', '', '13:01:21'), subject('t-m2'));
		setPage('/dashboard/conversations?q=zebra', { user: { username: 'alice' } });
		const { container } = render(Page);
		await waitFor(() => expect(titles(container)).toEqual(['Zebra first question?']));
		await fireEvent.click(container.querySelector('.card') as HTMLElement);
		const bubbles = [...container.querySelectorAll('.bubble')].map((b) => b.textContent?.replaceAll(/\s+/g, ' ').trim());
		expect(bubbles).toHaveLength(4);
		expect(bubbles[0]).toContain('Zebra first question?');
		expect(bubbles[1]).toContain('Zebra first answer.');
		expect(bubbles[2]).toContain('Zebra follow-up?');
		expect(bubbles[3]).toContain('Zebra second answer.');
	});
});


describe('the open conversation and the top-bar crew', () => {
	it('leaves the top-bar crew exactly as the person set it, and writes the conversation into the address', async () => {
		namespace.set('crew-demo');
		const { container } = render(Page);
		await waitFor(() => expect(container.querySelectorAll('.card').length).toBeGreaterThan(2));
		await fireEvent.click([...container.querySelectorAll<HTMLElement>('.card')].find((c) => c.textContent?.includes('namespace is the pod')) as HTMLElement);
		expect(get(namespace)).toBe('crew-demo');
		expect(page.url.search).toContain('conversation=c-t-ns');
	});

	it('opens the conversation a link names, whatever crew the selector shows', async () => {
		namespace.set('some-other-crew');
		setPage('/dashboard/conversations?conversation=c-t-ns', { user: { username: 'alice' } });
		const { container } = render(Page);
		await waitFor(() => expect(container.querySelectorAll('.bubble').length).toBe(2));
		expect(container.querySelector('.bubble.you')?.textContent).toContain('Which namespace is the pod in?');
		expect(get(namespace)).toBe('some-other-crew');
	});

	it('names the crew on each list item and in the header of the open conversation', async () => {
		namespace.set('');
		setPage('/dashboard/conversations?conversation=c-t-ns', { user: { username: 'alice' } });
		const { container } = render(Page);
		await waitFor(() => expect(container.querySelectorAll('.bubble').length).toBe(2));
		const badges = [...container.querySelectorAll('.crew-badge')].map((b) => b.textContent?.trim());
		expect(badges.length).toBeGreaterThan(3);
		expect(badges.every((b) => b === 'Demo')).toBe(true);
	});

	it('does not change the top-bar crew when a new conversation is started with a crew chosen on the page', async () => {
		namespace.set('');
		setPage('/dashboard/conversations', { user: { username: 'alice' } });
		render(Page);
		await fireEvent.click(screen.getByRole('button', { name: '+ New' }));
		await fireEvent.change(await screen.findByLabelText('Crew to ask'), { target: { value: 'crew-demo' } });
		expect(await screen.findByText('New conversation with Demo')).toBeTruthy();
		expect(get(namespace)).toBe('');
	});
});

describe('the list order', () => {
	it('lists conversations newest first, each once', async () => {
		const { container } = render(Page);
		await waitFor(() => expect(container.querySelectorAll('.card').length).toBeGreaterThan(2));
		const known = titles(container).filter((t) => /namespace is the pod|Smoke check smoke-1/.test(t ?? ''));
		expect(known).toEqual(['Smoke check smoke-1: reply with one short sentence in the namespace', 'Which namespace is the pod in?']);
		expect(new Set(titles(container)).size).toBe(titles(container).length);
	});

	it('is the same on every load', async () => {
		const first = render(Page);
		await waitFor(() => expect(first.container.querySelectorAll('.card').length).toBeGreaterThan(2));
		const before = titles(first.container);
		first.unmount();
		const second = render(Page);
		await waitFor(() => expect(second.container.querySelectorAll('.card').length).toBe(before.length));
		expect(titles(second.container)).toEqual(before);
	});
});

describe('asking from + New', () => {
	const question = 'Is the build green right now?';

	function stubGateway() {
		vi.stubGlobal(
			'fetch',
			vi.fn(async (url: string) => {
				if (url.endsWith('/ask')) return new Response(JSON.stringify({ conversationId: 'c-asked', requestedAt: new Date().toISOString() }));
				if (url.includes('/stream')) return new Response('data: {"type":"phase","agent":"coordinator"}\n\n');
				return new Response(JSON.stringify({ items: [], conversations: [], messages: [] }));
			})
		);
	}

	it('lists the question first at once, opens it, and puts it in the address', async () => {
		stubGateway();
		namespace.set('crew-demo');
		const { container } = render(Page);
		await waitFor(() => expect(container.querySelectorAll('.card').length).toBeGreaterThan(2));
		await fireEvent.click(screen.getByRole('button', { name: '+ New' }));
		await fireEvent.input(await screen.findByLabelText('Your question'), { target: { value: question } });
		await fireEvent.click(screen.getByRole('button', { name: 'Ask' }));

		await waitFor(() => expect(page.url.search).toContain('conversation=c-asked'));
		expect(titles(container)[0]).toBe(question);
		expect(titles(container).filter((t) => t === question)).toHaveLength(1);
		expect(container.querySelector('.card.selected .query')?.textContent).toContain(question);
		expect(container.querySelector('.bubble.you')?.textContent).toContain(question);
		expect(container.querySelector('.waiting')?.textContent).toContain('working on an answer');
	});

	it('keeps it listed once and open when the crew thread arrives on the bus', async () => {
		stubGateway();
		namespace.set('crew-demo');
		const { container } = render(Page);
		await waitFor(() => expect(container.querySelectorAll('.card').length).toBeGreaterThan(2));
		await fireEvent.click(screen.getByRole('button', { name: '+ New' }));
		await fireEvent.input(await screen.findByLabelText('Your question'), { target: { value: 'Second new question?' } });
		await fireEvent.click(screen.getByRole('button', { name: 'Ask' }));
		await waitFor(() => expect(page.url.search).toContain('conversation=c-asked'));

		const subject = 'kubemoot.discuss.crew-demo.demo.general.t-asked';
		handleMessage(msg('t-asked', 'demo-coordinator', 'thread_start', 'Second new question?', '23:59:00', { timestamp: new Date().toISOString(), metadata: { crew: 'demo', conversationId: 'c-asked' } }), subject);
		handleMessage(msg('t-asked', 'demo-coordinator', 'synthesis', 'It is green.', '23:59:30', { timestamp: new Date().toISOString() }), subject);
		await waitFor(() => expect(container.querySelector('.bubble.crew')?.textContent).toContain('It is green.'));
		expect(titles(container).filter((t) => t === 'Second new question?')).toHaveLength(1);
		expect(titles(container)[0]).toBe('Second new question?');
		expect(container.querySelector('.card.selected')).toBeTruthy();
	});
});

const COPIED = 'You: Which namespace is the pod in?\n\nCrew: It runs in the default namespace.\n\n1. first\n2. second';

describe('copying the open conversation', () => {
	it('puts the conversation text on the clipboard from the real menu item', async () => {
		const writeText = vi.fn(async () => undefined);
		Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
		const { container } = render(Page);
		await waitFor(() => expect(container.querySelectorAll('.card').length).toBeGreaterThan(2));
		await fireEvent.click([...container.querySelectorAll<HTMLElement>('.card')].find((c) => c.textContent?.includes('namespace is the pod')) as HTMLElement);
		await fireEvent.click(screen.getByRole('button', { name: 'Conversation actions' }));
		await fireEvent.click(screen.getByRole('menuitem', { name: 'Copy conversation' }));
		expect(writeText).toHaveBeenCalledWith(COPIED);
		expect(await screen.findByText('Conversation copied.')).toBeTruthy();
	});

	it('puts the same text on the clipboard from the header button', async () => {
		const writeText = vi.fn(async () => undefined);
		Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
		const { container } = render(Page);
		await waitFor(() => expect(container.querySelectorAll('.card').length).toBeGreaterThan(2));
		await fireEvent.click([...container.querySelectorAll<HTMLElement>('.card')].find((c) => c.textContent?.includes('namespace is the pod')) as HTMLElement);
		await fireEvent.click(screen.getByRole('button', { name: 'Copy conversation' }));
		expect(writeText).toHaveBeenCalledWith(COPIED);
	});
});

describe('a stand-in for a question just asked', () => {
	it('is dropped once the bus shows the thread, so it cannot linger as a ghost', async () => {
		vi.stubGlobal('fetch', vi.fn(async (url: string) => {
			if (url.endsWith('/ask')) return new Response(JSON.stringify({ conversationId: 'c-ghost', requestedAt: new Date().toISOString() }));
			if (url.includes('/stream')) return new Response('data: {"type":"done"}\n\n');
			return new Response(JSON.stringify({ conversations: [] }));
		}));
		namespace.set('crew-demo');
		const { container } = render(Page);
		await waitFor(() => expect(container.querySelectorAll('.card').length).toBeGreaterThan(2));
		await fireEvent.click(screen.getByRole('button', { name: '+ New' }));
		await fireEvent.input(await screen.findByLabelText('Your question'), { target: { value: 'Ghost question?' } });
		await fireEvent.click(screen.getByRole('button', { name: 'Ask' }));
		await waitFor(() => expect(titles(container)[0]).toBe('Ghost question?'));
		const subject = 'kubemoot.discuss.crew-demo.demo.general.t-ghost';
		handleMessage(msg('t-ghost', 'demo-coordinator', 'thread_start', 'Ghost question?', '23:59:00', { timestamp: new Date().toISOString(), metadata: { crew: 'demo', conversationId: 'c-ghost' } }), subject);
		await waitFor(() => expect(container.querySelector('.card.selected')).toBeTruthy());
		expect(titles(container).filter((t) => t === 'Ghost question?')).toHaveLength(1);
	});
});
