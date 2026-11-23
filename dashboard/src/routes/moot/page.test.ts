import { act, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { readFileSync } from 'node:fs';
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { handleMessage } from '#lib/stores/discussions.js';
import { page as mockPage, setPageData, setPageUrl } from '../../../vitest-mocks/app-state.svelte.js';
import type { DiscussionMessage } from '#lib/types/kubemoot.js';
import Page from './+page.svelte';

const T = '2026-11-21T22:24:06.000Z';
let seq = 0;
function msg(agentName: string, messageType: DiscussionMessage['messageType'], content: string, over: Partial<DiscussionMessage> = {}): DiscussionMessage {
	seq += 1;
	return { messageId: `m${seq}`, threadId: 'thread-a', agentName, messageType, content, channel: 'general', timestamp: T, ...over };
}

beforeAll(() => {
	// The default list shows the last 24 hours, so the clock is set to just after the fixed test threads.
	vi.useFakeTimers({ toFake: ['Date'] });
	vi.setSystemTime(new Date('2026-11-22T06:00:00Z'));
	vi.stubGlobal('EventSource', class { close() {} addEventListener() {} onmessage = null; onerror = null; onopen = null; });
	vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, json: async () => ({ items: [], channels: [], messages: [] }) }));
	const subject = 'kubemoot.discuss.crew-demo.demo.general.thread-a';
	const meta = { crew: 'demo', crewVersion: '1.0.0' };
	const all = [
		msg('human', 'thread_start', 'What are the two GPU temperatures?', { metadata: { ...meta, conversationId: 'c-1' } }),
		msg('coordinator', 'triage_result', 'triage'),
		msg('coordinator', 'subcommittee', 'Convened 1 agent'),
		msg('gpu-now', 'evaluating', 'Running tool-calling evaluation'),
		msg('gpu-now', 'agree', 'gpu-0: 33C'),
		msg('coordinator', 'synthesis', 'The two GPU temperatures are 33C and 36C.')
	];
	for (const m of all) handleMessage(m, subject);
});

describe('the Moot page', () => {
	it('lists the stored messages of the opened thread, signals and synthesis included', async () => {
		const { container } = render(Page);
		await waitFor(() => expect(container.querySelectorAll('.message-card').length).toBeGreaterThanOrEqual(4));
		expect(screen.getAllByText(/The two GPU temperatures are 33C and 36C/).length).toBeGreaterThan(0);
		expect(screen.getByText('gpu-0: 33C')).toBeTruthy();
		expect(screen.getByText('Convened 1 agent')).toBeTruthy();
		// The crew pill names the crew only, not namespace/crew.
		const pills = [...container.querySelectorAll('.crew-badge')].map((e) => e.textContent?.trim());
		expect(pills.length).toBeGreaterThan(0);
		expect(pills.every((t) => t === 'demo')).toBe(true);
	});

	it('does not make the message list a scroll box of its own', () => {
		// A list with flex: 1 and overflow-y: auto shrank to zero height under the span graph.
		const css = readFileSync('src/routes/moot/+page.svelte', 'utf8');
		const rule = /\.message-timeline \{[^}]*\}/.exec(css)?.[0] ?? '';
		expect(rule).not.toMatch(/overflow-y:\s*auto/);
		expect(rule).toMatch(/flex:\s*0 0 auto/);
	});
});

describe('following a live discussion', () => {
	it('shows the answer panel as working, then the answer, without a reload', async () => {
		const subject = 'kubemoot.discuss.crew-demo.demo.general.thread-live';
		const live = (agent: string, type: DiscussionMessage['messageType'], content: string, over: Partial<DiscussionMessage> = {}) =>
			msg(agent, type, content, { threadId: 'thread-live', timestamp: '2026-11-21T23:59:00.000Z', ...over });
		history.replaceState({}, '', '/dashboard/moot?thread=thread-live');
		handleMessage(live('human', 'thread_start', 'Live question?', { metadata: { crew: 'demo', conversationId: 'c-live' } }), subject);
		const { container } = render(Page);
		await waitFor(() => expect(screen.getByText(/still working/)).toBeTruthy());

		await act(() => handleMessage(live('gpu-now', 'agree', 'gpu-0: 33C'), subject));
		await waitFor(() => expect(screen.getByText('gpu-0: 33C')).toBeTruthy());
		expect(screen.getByText(/still working/)).toBeTruthy();

		await act(() => handleMessage(live('coordinator', 'synthesis', 'Final live answer.'), subject));
		await waitFor(() => expect(screen.queryByText(/still working/)).toBeNull());
		expect(container.querySelector('.answer-body')?.textContent).toContain('Final live answer.');
	});
});

describe('the Moot list order', () => {
	it('lists each discussion once, newest activity first, with a date even when its start message has none', async () => {
		const subject = (id: string) => `kubemoot.discuss.crew-demo.demo.general.${id}`;
		const start = (id: string, at: string | undefined) => msg('human', 'thread_start', `Question ${id}?`, { threadId: id, timestamp: at, metadata: { crew: 'demo' } });
		handleMessage(start('ord-old', '2026-11-22T01:00:00.000Z'), subject('ord-old'));
		handleMessage(start('ord-new', '2026-11-22T05:00:00.000Z'), subject('ord-new'));
		handleMessage(start('ord-notime', undefined), subject('ord-notime'));
		handleMessage(msg('coordinator', 'synthesis', 'done', { threadId: 'ord-notime', timestamp: '2026-11-22T03:00:00.000Z' }), subject('ord-notime'));
		history.replaceState({}, '', '/dashboard/moot');
		const { container } = render(Page);
		await waitFor(() => expect(container.querySelectorAll('.card').length).toBeGreaterThan(2));
		const rows = [...container.querySelectorAll('.card')].map((c) => c.textContent ?? '');
		const at = (q: string) => rows.findIndex((r) => r.includes(q));
		expect(at('Question ord-new?')).toBeLessThan(at('Question ord-notime?'));
		expect(at('Question ord-notime?')).toBeLessThan(at('Question ord-old?'));
		expect(rows.filter((r) => r.includes('Question ord-notime?'))).toHaveLength(1);
		expect(rows[at('Question ord-notime?')]).toMatch(/Nov \d+, \d+:\d+:\d+/);
	});
});

describe('the hover icon bars', () => {
	const writeText = vi.fn();
	beforeEach(() => {
		writeText.mockReset().mockResolvedValue(undefined);
		Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
		setPageData({ user: { username: 'alice' } });
		history.replaceState({}, '', '/dashboard/moot?thread=thread-a');
	});

	it('puts a Copy bar on every message of an open discussion and copies that message\'s text', async () => {
		const { container } = render(Page);
		await waitFor(() => expect(container.querySelectorAll('.message-card').length).toBeGreaterThanOrEqual(4));
		const card = [...container.querySelectorAll<HTMLElement>('.message-card')].find((c) => c.textContent?.includes('gpu-0: 33C')) as HTMLElement;
		expect(card.classList.contains('has-hover-actions')).toBe(true);
		const buttons = card.querySelectorAll('[role="toolbar"] button');
		expect([...buttons].map((b) => b.getAttribute('aria-label'))).toEqual(['Copy message']);
		await fireEvent.click(buttons[0]);
		await waitFor(() => expect(writeText).toHaveBeenCalledWith('gpu-0: 33C'));
	});

	it('gives every list row Copy id and, to a signed-in person, Delete', async () => {
		const { container } = render(Page);
		await waitFor(() => expect(container.querySelectorAll('.row').length).toBeGreaterThan(0));
		const row = [...container.querySelectorAll<HTMLElement>('.row')].find((r) => r.textContent?.includes('What are the two GPU temperatures?')) as HTMLElement;
		expect([...row.querySelectorAll('[role="toolbar"] button')].map((b) => b.getAttribute('aria-label'))).toEqual(['Copy id', 'Delete conversation']);
		await fireEvent.click(row.querySelector('button[aria-label="Copy id"]') as HTMLElement);
		await waitFor(() => expect(writeText).toHaveBeenCalledWith('c-1: What are the two GPU temperatures?'));
	});

	it('leaves Delete off the rows of a visitor who is not signed in', async () => {
		setPageData({});
		const { container } = render(Page);
		await waitFor(() => expect(container.querySelectorAll('.row').length).toBeGreaterThan(0));
		expect(container.querySelector('button[aria-label="Delete conversation"]')).toBeNull();
		expect(container.querySelector('button[aria-label="Copy id"]')).toBeTruthy();
	});

	it('asks before deleting a row\'s conversation and deletes nothing until told', async () => {
		const fetchMock = vi.fn().mockResolvedValue({ ok: true, json: async () => ({ items: [], channels: [], messages: [] }) });
		vi.stubGlobal('fetch', fetchMock);
		const { container } = render(Page);
		await waitFor(() => expect(container.querySelectorAll('.row').length).toBeGreaterThan(0));
		const row = [...container.querySelectorAll<HTMLElement>('.row')].find((r) => r.textContent?.includes('What are the two GPU temperatures?')) as HTMLElement;
		await fireEvent.click(row.querySelector('button[aria-label="Delete conversation"]') as HTMLElement);
		expect(screen.getByRole('alertdialog').textContent).toContain('What are the two GPU temperatures?');
		expect(fetchMock.mock.calls.some(([, init]) => (init as RequestInit | undefined)?.method === 'DELETE')).toBe(false);
		await fireEvent.click(screen.getByRole('button', { name: 'Keep it' }));
		expect(screen.queryByRole('alertdialog')).toBeNull();
	});
});

describe('opening a thread from a link', () => {
	const subjectOf = (id: string) => `kubemoot.discuss.crew-demo.demo.general.${id}`;
	const start = (id: string, question: string, at: string) =>
		msg('human', 'thread_start', question, { threadId: id, timestamp: at, metadata: { crew: 'demo', conversationId: `c-${id}` } });
	const historyPage = (messages: DiscussionMessage[], id: string) => ({
		messages: messages.map((m, i) => ({ seq: i + 1, subject: subjectOf(id), data: JSON.stringify(m) }))
	});

	beforeEach(() => {
		setPageData({});
		setPageUrl('/dashboard/moot');
	});

	it('keeps the thread the address names selected and in the address', async () => {
		handleMessage(start('open-a', 'Question open-a?', '2026-11-22T04:00:00.000Z'), subjectOf('open-a'));
		handleMessage(start('open-b', 'Question open-b?', '2026-11-22T05:00:00.000Z'), subjectOf('open-b'));
		history.replaceState({}, '', '/dashboard/moot?thread=open-a');
		const { container } = render(Page);
		await waitFor(() => expect(container.querySelector('.thread-query-full')?.textContent).toContain('Question open-a?'));
		expect(new URLSearchParams(mockPage.url.search).get('thread')).toBe('open-a');
	});

	it('reads a thread older than the loaded history by its subject, and selects it', async () => {
		const old = [
			start('open-old', 'An old question?', '2026-11-01T04:00:00.000Z'),
			msg('coordinator', 'synthesis', 'An old answer.', { threadId: 'open-old', timestamp: '2026-11-01T04:01:00.000Z' })
		];
		const fetchMock = vi.fn(async (url: string) =>
			url.includes(encodeURIComponent(subjectOf('open-old')))
				? { ok: true, json: async () => historyPage(old, 'open-old') }
				: { ok: true, json: async () => ({ items: [], channels: [], messages: [] }) }
		);
		vi.stubGlobal('fetch', fetchMock);
		history.replaceState({}, '', `/dashboard/moot?thread=open-old&subject=${encodeURIComponent(subjectOf('open-old'))}`);
		const { container } = render(Page);
		await waitFor(() => expect(container.querySelector('.thread-query-full')?.textContent).toContain('An old question?'));
		expect(screen.getAllByText(/An old answer/).length).toBeGreaterThan(0);
		expect(new URLSearchParams(mockPage.url.search).get('thread')).toBe('open-old');
	});

	it('shows a thread outside the filter with a note, and leaves the filter alone until Show all', async () => {
		// Older than the default last 24 hours.
		handleMessage(start('open-out', 'Out of range question?', '2026-11-10T04:00:00.000Z'), subjectOf('open-out'));
		history.replaceState({}, '', '/dashboard/moot?thread=open-out');
		const { container } = render(Page);
		await waitFor(() => expect(container.querySelector('.thread-query-full')?.textContent).toContain('Out of range question?'));
		expect(screen.getByText('Outside the current filter')).toBeTruthy();
		expect(mockPage.url.search).not.toContain('range=');
		await fireEvent.click(screen.getByRole('button', { name: 'Show all' }));
		await waitFor(() => expect(screen.queryByText('Outside the current filter')).toBeNull());
		expect(new URLSearchParams(mockPage.url.search).get('thread')).toBe('open-out');
	});

	it('falls back to the first thread when the requested one does not exist', async () => {
		history.replaceState({}, '', '/dashboard/moot?thread=no-such-thread');
		const { container } = render(Page);
		await waitFor(() => expect(container.querySelector('.thread-query-full')).toBeTruthy());
		expect(container.querySelector('.thread-query-full')?.textContent).not.toBe('');
		expect(screen.queryByText('Outside the current filter')).toBeNull();
	});

	it('links a thread of a conversation to it, and a thread with none to nothing', async () => {
		handleMessage(start('open-conv', 'Has a conversation?', '2026-11-22T05:30:00.000Z'), subjectOf('open-conv'));
		handleMessage(msg('human', 'thread_start', 'A fitness thread?', { threadId: 'open-fit', timestamp: '2026-11-22T05:31:00.000Z', metadata: { crew: 'demo' } }), subjectOf('open-fit'));
		history.replaceState({}, '', '/dashboard/moot?thread=open-conv');
		const { container, unmount } = render(Page);
		await waitFor(() => expect(container.querySelector('.thread-query-full')?.textContent).toContain('Has a conversation?'));
		await fireEvent.click(screen.getByRole('button', { name: 'Actions' }));
		const href = screen.getByRole('menuitem', { name: 'View associated conversation' }).getAttribute('href') ?? '';
		const query = new URLSearchParams(href.split('?')[1]);
		expect(query.get('conversation')).toBe('c-open-conv');
		expect(query.get('thread')).toBe('open-conv');
		expect(query.get('subject')).toBe(subjectOf('open-conv'));
		unmount();

		history.replaceState({}, '', '/dashboard/moot?thread=open-fit');
		const second = render(Page);
		await waitFor(() => expect(second.container.querySelector('.thread-query-full')?.textContent).toContain('A fitness thread?'));
		await fireEvent.click(screen.getByRole('button', { name: 'Actions' }));
		expect(screen.queryByRole('menuitem', { name: 'View associated conversation' })).toBeNull();
	});
});
