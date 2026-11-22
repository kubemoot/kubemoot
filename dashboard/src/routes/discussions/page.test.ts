import { render, screen, waitFor } from '@testing-library/svelte';
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

describe('discussions page', () => {
	it('lists the stored messages of the opened thread, signals and synthesis included', async () => {
		const { container } = render(Page);
		await waitFor(() => expect(container.querySelectorAll('.message-card').length).toBeGreaterThanOrEqual(4));
		expect(screen.getAllByText(/The two GPU temperatures are 33C and 36C/).length).toBeGreaterThan(0);
		expect(screen.getByText('gpu-0: 33C')).toBeTruthy();
		expect(screen.getByText('Convened 1 agent')).toBeTruthy();
	});

	it('does not make the message list a scroll box of its own', () => {
		// A list with flex: 1 and overflow-y: auto shrank to zero height under the span graph.
		const css = readFileSync('src/routes/discussions/+page.svelte', 'utf8');
		const rule = /\.message-timeline \{[^}]*\}/.exec(css)?.[0] ?? '';
		expect(rule).not.toMatch(/overflow-y:\s*auto/);
		expect(rule).toMatch(/flex:\s*0 0 auto/);
	});
});
