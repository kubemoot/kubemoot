import { describe, it, expect } from 'vitest';
import { get } from 'svelte/store';
import { handleMessage, sortedThreads } from './discussions';
import type { DiscussionMessage } from '$types/kubemoot';

// Covers the refactored handleMessage dispatch (applyThreadStart / dedup /
// applyUnknownThreadMessage). threadsMap is a module singleton, so each test uses
// a unique threadId to stay isolated.
let seq = 0;
function msg(over: Partial<DiscussionMessage>): DiscussionMessage {
	seq += 1;
	return {
		messageId: `m-${seq}`,
		threadId: 't',
		agentName: 'coordinator',
		messageType: 'thread_start',
		content: 'hello',
		channel: 'general',
		timestamp: '2026-01-01T00:00:00.000Z',
		...over
	};
}
const thread = (id: string) => get(sortedThreads).find((t) => t.threadId === id);

describe('discussions handleMessage', () => {
	it('sets crew on a thread created from thread_start (regression for the missing-crew bug)', () => {
		handleMessage(
			msg({ threadId: 'tc1', messageType: 'thread_start', metadata: { crew: 'homelab-pilot' } })
		);
		expect(thread('tc1')?.crew).toBe('homelab-pilot');
	});

	it('sets crew version on a thread created from thread_start (provenance)', () => {
		handleMessage(
			msg({
				threadId: 'tcv1',
				messageType: 'thread_start',
				metadata: { crew: 'homelab-pilot', crewVersion: '0.20.0' }
			})
		);
		expect(thread('tcv1')?.crew).toBe('homelab-pilot');
		expect(thread('tcv1')?.crewVersion).toBe('0.20.0');
	});

	it('carries crew version on a thread created from a non-thread_start message', () => {
		handleMessage(
			msg({ threadId: 'tcv2', messageType: 'agree', metadata: { crew: 'homelab-pilot', crewVersion: '0.20.0' } })
		);
		expect(thread('tcv2')?.crewVersion).toBe('0.20.0');
	});

	it('dedupes a repeated messageId', () => {
		const m = msg({ threadId: 'td1', messageType: 'thread_start' });
		handleMessage(m);
		handleMessage(m);
		expect(thread('td1')?.messages.length).toBe(1);
	});

	it('creates a thread from a non-thread_start message and carries crew', () => {
		handleMessage(
			msg({ threadId: 'tu1', messageType: 'agree', metadata: { crew: 'homelab-pilot-prose' } })
		);
		const t = thread('tu1');
		expect(t).toBeTruthy();
		expect(t?.crew).toBe('homelab-pilot-prose');
	});
});
