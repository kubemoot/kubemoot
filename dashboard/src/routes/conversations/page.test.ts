import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { get } from 'svelte/store';
import { beforeAll, describe, expect, it, vi } from 'vitest';
import { handleMessage } from '#lib/stores/discussions.js';
import { namespace } from '#lib/stores/index.js';
import type { DiscussionMessage } from '#lib/types/kubemoot.js';
import Page from './+page.svelte';

let seq = 0;
function msg(agentName: string, messageType: DiscussionMessage['messageType'], content: string): DiscussionMessage {
	seq += 1;
	return { messageId: `c${seq}`, threadId: 'conv-a', agentName, messageType, content, channel: 'general', timestamp: '2026-11-21T22:24:06.000Z', metadata: { crew: 'demo', conversationId: 'c-1' } };
}

beforeAll(() => {
	vi.stubGlobal('EventSource', class { close() {} addEventListener() {} onmessage = null; onerror = null; onopen = null; });
	vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, json: async () => ({ items: [], messages: [] }) }));
	const subject = 'kubemoot.discuss.crew-demo.demo.general.conv-a';
	handleMessage(msg('human', 'thread_start', 'What is up?'), subject);
	handleMessage(msg('coordinator', 'synthesis', 'All good.'), subject);
});

describe('conversations page with All crews selected', () => {
	it('opens a conversation, shows it, and sets the top-bar crew to its crew', async () => {
		namespace.set('');
		render(Page);
		const row = await waitFor(() => screen.getByRole('button', { name: /What is up/ }));
		await fireEvent.click(row);
		expect(get(namespace)).toBe('crew-demo');
		await waitFor(() => expect(screen.getByText('All good.')).toBeTruthy());
		expect(screen.queryByText(/Select a crew in the top bar/)).toBeNull();
	});

	it('says to select a conversation, not a crew, while conversations are listed', async () => {
		namespace.set('');
		render(Page);
		await waitFor(() => expect(screen.getByText('Select a conversation, or start one from a terminal.')).toBeTruthy());
	});
});
