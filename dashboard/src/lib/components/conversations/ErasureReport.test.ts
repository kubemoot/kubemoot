import { fireEvent, render, screen } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import ErasureReport from './ErasureReport.svelte';

const report = {
	conversationId: 'c-1',
	threads: ['t1'],
	deleted: { history: 1, owner: 1, discussionMessages: 42, workspaceObjects: 0 },
	kept: ['KUBEMOOT_REQUEST: a question still queued expires with the stream', 'logs and backups: erasing from NATS does not erase them']
};

describe('ErasureReport', () => {
	it('lists what was removed per store, and what was not', () => {
		render(ErasureReport, { report, question: 'What are the two GPU temperatures?', ondismiss: vi.fn() });
		expect(screen.getByText('Conversation deleted')).toBeTruthy();
		expect(screen.getByText('What are the two GPU temperatures?')).toBeTruthy();
		expect(screen.getByText('42 discussion messages')).toBeTruthy();
		expect(screen.getByText('1 conversation history')).toBeTruthy();
		expect(screen.queryByText(/workspace files/)).toBeNull();
		expect(screen.getByText(/logs and backups/)).toBeTruthy();
	});

	it('says so when nothing was left, and closes', async () => {
		const ondismiss = vi.fn();
		render(ErasureReport, { report: { conversationId: 'c', deleted: {}, kept: [] }, question: '', ondismiss });
		expect(screen.getByText('Nothing was left in any store.')).toBeTruthy();
		await fireEvent.click(screen.getByRole('button', { name: 'Close' }));
		expect(ondismiss).toHaveBeenCalled();
	});
});
