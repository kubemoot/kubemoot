import { act, render, screen, waitFor } from '@testing-library/svelte';
import { readFileSync } from 'node:fs';
import { beforeAll, describe, expect, it, vi } from 'vitest';
import { handleMessage } from '#lib/stores/discussions.js';
import type { DiscussionMessage } from '#lib/types/kubemoot.js';
import Page from './+page.svelte';

const T = '2026-11-21T22:24:06.000Z';
let seq = 0;
function msg(agentName: string, messageType: DiscussionMessage['messageType'], content: string, over: Partial<DiscussionMessage> = {}): DiscussionMessage {
	seq += 1;
	return { messageId: `m${seq}`, threadId: 'thread-a', agentName, messageType, content, channel: 'general', timestamp: T, ...over };
}

beforeAll(() => {
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
