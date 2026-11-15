import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { get } from 'svelte/store';
import NotSent from './NotSent.svelte';
import { unsentMessages } from '#lib/send-state.js';
import type { DiscussionMessage } from '#lib/types/kubemoot.js';

const message = { messageId: 'm1', threadId: 't', agentName: 'human', messageType: 'thread_start' } as DiscussionMessage;

describe('NotSent', () => {
	beforeEach(() => unsentMessages.set(new Map()));

	it('shows nothing for a message that was sent', () => {
		render(NotSent, { messageId: 'm1', publish: vi.fn() });
		expect(screen.queryByRole('alert')).toBeNull();
	});

	it('names the reason and sends the same message again on Retry', async () => {
		unsentMessages.set(new Map([['m1', { subject: 's', message, reason: 'Your session has ended.' }]]));
		const publish = vi.fn().mockResolvedValue(undefined);
		render(NotSent, { messageId: 'm1', publish });
		expect(screen.getByRole('alert').textContent).toContain('Not sent: Your session has ended.');
		await fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
		await waitFor(() => expect(get(unsentMessages).has('m1')).toBe(false));
		expect(publish).toHaveBeenCalledWith('s', message);
	});

	it('keeps the mark with the new reason when the retry fails too', async () => {
		unsentMessages.set(new Map([['m1', { subject: 's', message, reason: 'old' }]]));
		render(NotSent, { messageId: 'm1', publish: vi.fn().mockRejectedValue(new Error('still down')) });
		await fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
		await waitFor(() => expect(get(unsentMessages).get('m1')?.reason).toBe('Sending failed: still down'));
	});
});
