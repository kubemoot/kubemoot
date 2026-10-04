import { describe, it, expect } from 'vitest';
import { get } from 'svelte/store';
import { handleMessage, sortedThreads, threadScope } from './discussions';
import type { DiscussionMessage } from '#lib/types/kubemoot.js';

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

	it('scopes a thread by the namespace and crew in its subject', () => {
		handleMessage(
			msg({ threadId: 'ns1', messageType: 'thread_start' }),
			'kubemoot.discuss.team-alpha.homelab-pilot.general.ns1'
		);
		expect(thread('ns1')?.namespace).toBe('team-alpha');
		expect(thread('ns1')?.crew).toBe('homelab-pilot');
		expect(threadScope(thread('ns1')!)).toEqual({ namespace: 'team-alpha', crew: 'homelab-pilot' });
	});

	it('keeps same-named crews in two namespaces apart', () => {
		handleMessage(
			msg({ threadId: 'nsA', messageType: 'agree' }),
			'kubemoot.discuss.team-alpha.homelab-pilot.general.nsA'
		);
		handleMessage(
			msg({ threadId: 'nsB', messageType: 'agree' }),
			'kubemoot.discuss.team-beta.homelab-pilot.general.nsB'
		);
		expect(thread('nsA')?.namespace).toBe('team-alpha');
		expect(thread('nsB')?.namespace).toBe('team-beta');
	});

	it('fills the scope from a later message when the first had no subject', () => {
		handleMessage(msg({ threadId: 'nsF', messageType: 'thread_start' }));
		expect(threadScope(thread('nsF')!)).toBeNull();
		handleMessage(
			msg({ threadId: 'nsF', messageType: 'agree' }),
			'kubemoot.discuss.team-beta.homelab-pilot.general.nsF'
		);
		expect(threadScope(thread('nsF')!)).toEqual({ namespace: 'team-beta', crew: 'homelab-pilot' });
	});

	it('ignores a pre-namespace subject for scoping', () => {
		handleMessage(
			msg({ threadId: 'nsOld', messageType: 'thread_start' }),
			'kubemoot.discuss.homelab-pilot.general.nsOld'
		);
		expect(thread('nsOld')).toBeTruthy();
		expect(thread('nsOld')?.namespace).toBeUndefined();
	});
});
