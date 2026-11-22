import { describe, expect, it, vi } from 'vitest';
import { deleteTarget, rowActions, type RowThread } from './row-actions';

const thread = (over: Partial<RowThread> = {}): RowThread =>
	({
		threadId: 't-1',
		namespace: 'crew-a',
		crew: 'pilot',
		messages: [{ messageId: 'm1', threadId: 't-1', agentName: 'coordinator', messageType: 'thread_start', content: 'q', channel: 'general', timestamp: 't', metadata: { conversationId: 'c-1' } }],
		...over
	}) as unknown as RowThread;

describe('deleteTarget', () => {
	it('names the crew and the conversation of a thread that has both', () => {
		expect(deleteTarget(thread())).toEqual({ id: 'c-1', scope: { namespace: 'crew-a', crew: 'pilot' } });
	});

	it('is null for an older thread with no conversation id', () => {
		expect(deleteTarget(thread({ messages: [] }))).toBeNull();
	});

	it('is null when the crew is unknown', () => {
		expect(deleteTarget(thread({ crew: undefined }))).toBeNull();
	});
});

describe('rowActions', () => {
	it('offers Copy id, then Delete when the person may delete and the thread can be', () => {
		const actions = rowActions(thread(), 'Which GPU?', { canDelete: true, ondelete: vi.fn() });
		expect(actions.map((a) => a.label)).toEqual(['Copy id', 'Delete conversation']);
		expect(actions[1].danger).toBe(true);
	});

	it('offers only Copy id when the person may not delete', () => {
		expect(rowActions(thread(), 'q', { canDelete: false, ondelete: vi.fn() }).map((a) => a.label)).toEqual(['Copy id']);
	});

	it('offers only Copy id for a thread that cannot be deleted', () => {
		expect(rowActions(thread({ messages: [] }), 'q', { canDelete: true, ondelete: vi.fn() }).map((a) => a.label)).toEqual(['Copy id']);
	});

	it('copies the conversation id with the question, or the thread id when there is none', async () => {
		const writeText = vi.fn().mockResolvedValue(undefined);
		Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
		await rowActions(thread(), 'Which GPU?', { canDelete: false, ondelete: vi.fn() })[0].run();
		expect(writeText).toHaveBeenLastCalledWith('c-1: Which GPU?');
		await rowActions(thread({ messages: [] }), 'Which GPU?', { canDelete: false, ondelete: vi.fn() })[0].run();
		expect(writeText).toHaveBeenLastCalledWith('t-1: Which GPU?');
	});

	it('runs the delete callback from the Delete icon', async () => {
		const ondelete = vi.fn();
		await rowActions(thread(), 'q', { canDelete: true, ondelete })[1].run();
		expect(ondelete).toHaveBeenCalledOnce();
	});
});
