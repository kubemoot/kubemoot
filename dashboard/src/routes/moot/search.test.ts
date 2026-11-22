import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { handleMessage } from '#lib/stores/discussions.js';
import { namespace } from '#lib/stores/namespace.js';
import { setPageData, setPageUrl } from '../../../vitest-mocks/app-state.svelte';
import type { DiscussionMessage } from '#lib/types/kubemoot.js';
import Page from './+page.svelte';


function setPage(address: string, data: Record<string, unknown> = {}) {
	setPageUrl(address);
	setPageData(data);
}

let seq = 0;
function start(threadId: string, question: string, at: string) {
	seq += 1;
	const m: DiscussionMessage = {
		messageId: `m${seq}`, threadId, agentName: 'demo-coordinator', messageType: 'thread_start', content: question,
		channel: 'general', timestamp: `2026-11-21T${at}Z`, metadata: { crew: 'demo', conversationId: `c-${threadId}` }
	};
	handleMessage(m, `kubemoot.discuss.crew-demo.demo.general.${threadId}`);
}

beforeAll(() => {
	// The default list shows the last 24 hours, so the clock is set to just after the fixed test threads.
	vi.useFakeTimers({ toFake: ['Date'] });
	vi.setSystemTime(new Date('2026-11-21T20:00:00Z'));
	vi.stubGlobal('EventSource', class { close() {} addEventListener() {} onmessage = null; onerror = null; onopen = null; });
	vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, json: async () => ({ items: [], channels: [], messages: [] }) }));
	start('t-ns', 'Which namespace is the pod in?', '10:00:00');
	start('t-smoke', 'Smoke check smoke-1: reply in the namespace', '11:00:00');
	start('t-gpu', 'What are the two GPU temperatures?', '12:00:00');
});

beforeEach(() => {
	namespace.set('');
	history.replaceState({}, '', '/dashboard/moot');
});

const queries = (container: HTMLElement) => [...container.querySelectorAll('.card .query')].map((e) => e.textContent?.trim());

describe('the Moot page', () => {
	it('is named the Moot, with no subtitle line; its (i) help says it is read-only', async () => {
		setPage('/dashboard/moot');
		render(Page);
		expect(screen.getByRole('heading', { name: 'The Moot' })).toBeTruthy();
		expect(screen.queryByText(/The agents deliberating/)).toBeNull();
		await fireEvent.click(screen.getByRole('button', { name: 'About The Moot' }));
		expect(screen.getByRole('dialog').textContent).toContain('This page is read-only.');
		expect(screen.queryByText('Agent Discussions')).toBeNull();
	});

	it('lists every discussion with no search', async () => {
		setPage('/dashboard/moot');
		const { container } = render(Page);
		await waitFor(() => expect(queries(container)).toHaveLength(3));
	});

	it('drops the smoke checks for "namespace -smoke", the same search as Conversations', async () => {
		setPage('/dashboard/moot?q=namespace+-smoke');
		const { container } = render(Page);
		await waitFor(() => expect(queries(container)).toEqual(['Which namespace is the pod in?']));
	});

	it('says when nothing matches, and has the search help as an (i) that opens on click', async () => {
		setPage('/dashboard/moot?q=zebra');
		const { container } = render(Page);
		expect(await screen.findByText('No discussions match this search.')).toBeTruthy();
		await fireEvent.click(screen.getByRole('button', { name: 'About search' }));
		expect(screen.getByRole('dialog').textContent).toContain('Words must all match');
		expect(container.querySelector('.help-btn')).toBeNull();
	});
});
