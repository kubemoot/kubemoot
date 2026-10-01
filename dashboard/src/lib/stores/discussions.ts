import { writable, derived } from 'svelte/store';
import { browser } from '$app/environment';
import { resolve } from '$app/paths';
import type { DiscussionMessage } from '$types/kubemoot.js';
import {
	DISCUSS_ALL,
	discussThreadFilter,
	parseDiscussSubject,
	tryCrewScope,
	type CrewScope,
	type DiscussSubject
} from '$lib/crewScope.js';

export interface Thread {
	threadId: string;
	channel: string;
	userQuery: string;
	startedBy: string;
	startedAt: string;
	messages: DiscussionMessage[];
	status: 'open' | 'synthesized' | 'closed';
	agreeCount: number;
	concernCount: number;
	blockCount: number;
	standAsideCount: number;
	advisoryCount: number;
	proposalCount: number;
	// Namespace and crew come from the message subject
	// (kubemoot.discuss.<ns>.<crew>.<channel>.<thread>); crew falls back to the
	// message metadata when the subject is unknown.
	namespace?: string;
	crew?: string;
	crewVersion?: string;
	// Legacy aliases
	contributorCount: number;
	declineCount: number;
}

// Module-level state - survives navigation
const threadsMap = writable<Map<string, Thread>>(new Map());
export const connected = writable(false);
export const streamReady = writable(false);

let eventSource: EventSource | undefined;
let initialized = false;
// Track last sequence for reconnection
let lastSeq = 0;
// Track deleted threads so messages don't resurrect them
const deletedThreadIds = new Set<string>();

const reopen = (t: Thread) => {
	t.status = 'open';
};
const countAgree = (t: Thread) => {
	t.agreeCount++;
	t.contributorCount++;
};
const countStandAside = (t: Thread) => {
	t.standAsideCount++;
	t.declineCount++;
};

// How each message type moves a thread's tallies and status. 'contribution' and
// 'decline' are legacy aliases for 'agree' and 'stand_aside'.
const THREAD_COUNT_UPDATES = new Map<string, (t: Thread) => void>(
	Object.entries({
		agree: countAgree,
		contribution: countAgree,
		concern: (t) => t.concernCount++,
		block: (t) => t.blockCount++,
		stand_aside: countStandAside,
		decline: countStandAside,
		advisory: (t) => t.advisoryCount++,
		proposal: (t) => t.proposalCount++,
		synthesis: (t) => {
			t.status = 'synthesized';
		},
		follow_up: reopen,
		reply: reopen,
		thread_close: (t: Thread) => {
			t.status = 'closed';
		}
	})
);

function updateThreadCounts(thread: Thread, msg: DiscussionMessage) {
	THREAD_COUNT_UPDATES.get(msg.messageType)?.(thread);
}

function emptyThreadCounts() {
	return {
		agreeCount: 0,
		concernCount: 0,
		blockCount: 0,
		standAsideCount: 0,
		advisoryCount: 0,
		proposalCount: 0,
		contributorCount: 0,
		declineCount: 0
	};
}

/** The namespace+crew a thread belongs to, or null when either is unknown. */
export function threadScope(thread: Pick<Thread, 'namespace' | 'crew'>): CrewScope | null {
	return tryCrewScope(thread.namespace, thread.crew);
}

function scopeFields(msg: DiscussionMessage, subject: DiscussSubject | null) {
	return {
		namespace: subject?.namespace,
		crew: msg.metadata?.crew ?? subject?.crew
	};
}

// Fills a thread's namespace and crew from a later message when the thread was
// created from one that did not carry them.
function fillScope(thread: Thread, subject: DiscussSubject | null) {
	if (!subject) return;
	thread.namespace ??= subject.namespace;
	thread.crew ??= subject.crew;
}

function applyThreadStart(
	threads: Map<string, Thread>,
	existing: Thread | undefined,
	msg: DiscussionMessage,
	subject: DiscussSubject | null
) {
	if (!existing) {
		threads.set(msg.threadId, {
			threadId: msg.threadId,
			channel: msg.channel,
			...scopeFields(msg, subject),
			crewVersion: msg.metadata?.crewVersion,
			userQuery: msg.metadata?.userQuery || msg.content,
			startedBy: msg.agentName,
			startedAt: msg.timestamp,
			messages: [msg],
			status: 'open',
			...emptyThreadCounts()
		});
		return;
	}
	// With ordered stream delivery this path rarely fires,
	// but kept for robustness (e.g., broadcast duplicates)
	const query = msg.metadata?.userQuery || msg.content;
	if (query && !existing.userQuery) {
		existing.userQuery = query;
		existing.startedBy = msg.agentName;
		existing.startedAt = msg.timestamp;
		existing.channel = msg.channel;
	}
	if (!existing.messages.some((m) => m.messageType === 'thread_start')) {
		existing.messages.unshift(msg);
	}
	fillScope(existing, subject);
	threads.set(msg.threadId, { ...existing });
}

function applyUnknownThreadMessage(
	threads: Map<string, Thread>,
	msg: DiscussionMessage,
	subject: DiscussSubject | null
) {
	const thread: Thread = {
		threadId: msg.threadId,
		channel: msg.channel && msg.channel !== 'broadcast' ? msg.channel : 'general',
		userQuery: msg.content || '',
		startedBy: msg.agentName,
		startedAt: msg.timestamp,
		...scopeFields(msg, subject),
		crewVersion: msg.metadata?.crewVersion,
		messages: [msg],
		status: 'open',
		...emptyThreadCounts()
	};
	updateThreadCounts(thread, msg);
	threads.set(msg.threadId, thread);
}

/**
 * Folds one discussion message into the thread map. `subject` is the NATS subject
 * the message arrived on (or was published to); its namespace and crew scope the
 * thread.
 */
export function handleMessage(msg: DiscussionMessage, subject?: string) {
	if (deletedThreadIds.has(msg.threadId)) return;
	const parsed = parseDiscussSubject(subject);

	threadsMap.update((threads) => {
		const existing = threads.get(msg.threadId);

		// Dedup by messageId
		if (existing && msg.messageId) {
			if (existing.messages.some((m) => m.messageId === msg.messageId)) {
				return threads;
			}
		}

		if (msg.messageType === 'thread_start') {
			applyThreadStart(threads, existing, msg, parsed);
		} else if (existing) {
			const updated = { ...existing, messages: [...existing.messages, msg] };
			fillScope(updated, parsed);
			updateThreadCounts(updated, msg);
			threads.set(msg.threadId, updated);
		} else {
			// Message arrived for unknown thread (rare with ordered delivery)
			applyUnknownThreadMessage(threads, msg, parsed);
		}

		return new Map(threads);
	});
}

/**
 * Single SSE connection backed by a JetStream ordered consumer.
 * Replays all historical messages in stream order, then seamlessly
 * transitions to live messages. No separate history load needed.
 */
function startStream() {
	if (eventSource) return;

	// Start the live tail from the stream tip recorded by the preceding bounded
	// history load (loadRecent sets lastSeq). On a from_seq at/near the tip this
	// replays ~nothing and goes live - it does NOT re-replay the whole stream, which
	// is what froze the page once suite runs filled it with thousands of threads.
	const streamUrl = `${resolve('/api/nats/stream')}?stream=KUBEMOOT_DISCUSS&subject=${encodeURIComponent(DISCUSS_ALL)}&from_seq=${lastSeq}`;
	eventSource = new EventSource(streamUrl);

	eventSource.onopen = () => {
		connected.set(true);
	};

	eventSource.onmessage = (event) => {
		try {
			const envelope = JSON.parse(event.data);

			if (envelope.type === 'connected') {
				streamReady.set(true);
				return;
			}

			if (envelope.type !== 'message' || !envelope.data) return;

			// Track sequence for in-session reconnection (not persisted across refreshes)
			if (envelope.seq && envelope.seq > lastSeq) {
				lastSeq = envelope.seq;
			}

			const data = typeof envelope.data === 'string' ? JSON.parse(envelope.data) : envelope.data;
			if (!data.threadId || !data.messageType) return;
			handleMessage(data as DiscussionMessage, envelope.subject);
		} catch {
			// Ignore parse errors
		}
	};

	eventSource.onerror = () => {
		connected.set(false);
		// EventSource auto-reconnects; from_seq ensures we resume correctly
	};
}

// How many of the most recent discussion messages to load on open. Bounded so the
// page stays responsive even when suite runs have filled the stream with thousands
// of judge-discussion threads. Older threads aren't loaded up front (a future
// "load older" can page back); the namespace selector still narrows the view.
const RECENT_LIMIT = 1000;

function processHistoryMessage(m: { seq?: unknown; data?: unknown; subject?: string }) {
	if (typeof m.seq === 'number' && m.seq > lastSeq) lastSeq = m.seq;
	try {
		const d = typeof m.data === 'string' ? JSON.parse(m.data) : m.data;
		if (d && d.threadId && d.messageType) handleMessage(d as DiscussionMessage, m.subject);
	} catch {
		/* skip malformed */
	}
}

// loadRecent pulls the newest RECENT_LIMIT messages (the history endpoint delivers
// newest-first), builds the recent thread map, and records the stream tip so
// the live tail starts there rather than replaying the entire stream on every
// page load.
async function loadRecent() {
	try {
		const subject = encodeURIComponent(DISCUSS_ALL);
		const res = await fetch(
			`${resolve('/api/nats/history')}?stream=KUBEMOOT_DISCUSS&subject=${subject}&limit=${RECENT_LIMIT}`
		);
		if (res.ok) {
			const data = await res.json();
			for (const m of data.messages ?? []) {
				processHistoryMessage(m);
			}
			if (typeof data.lastSeq === 'number' && data.lastSeq > lastSeq) lastSeq = data.lastSeq;
		}
	} catch {
		/* fall through - the live stream still connects below */
	}
	streamReady.set(true);
}

/** Call from onMount - bounded recent load, then the live tail. No-op if already initialized. */
export function initDiscussions() {
	if (!browser || initialized) return;
	initialized = true;
	loadRecent()
		.then(startStream)
		.catch((err) => console.error('discussions: initial load/stream failed', err));
}

export async function removeThread(threadId: string) {
	deletedThreadIds.add(threadId);
	let scope: CrewScope | null = null;
	threadsMap.update((threads) => {
		const existing = threads.get(threadId);
		if (existing) scope = threadScope(existing);
		threads.delete(threadId);
		return new Map(threads);
	});

	// Purge messages from NATS JetStream so they don't come back on reload
	try {
		await fetch(resolve('/api/nats/purge'), {
			method: 'POST',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify({
				stream: 'KUBEMOOT_DISCUSS',
				filter: discussThreadFilter(threadId, scope)
			})
		});
	} catch (e) {
		console.error('Failed to purge thread from NATS:', e);
	}
}

export const threads = {
	subscribe: threadsMap.subscribe
};

export const sortedThreads = derived(threadsMap, ($threads) =>
	Array.from($threads.values()).sort(
		(a, b) => new Date(b.startedAt).getTime() - new Date(a.startedAt).getTime()
	)
);

// Pinned threads - persisted in NATS KV bucket kubemoot_pinned_threads
export const pinnedThreadIds = writable<Set<string>>(new Set());

export async function loadPinnedThreads() {
	try {
		const res = await fetch(resolve('/api/discussions/pin'));
		if (!res.ok) return;
		const { pinned } = await res.json();
		pinnedThreadIds.set(new Set(Object.keys(pinned ?? {})));
	} catch (e) {
		console.error('Failed to load pinned threads:', e);
	}
}

export async function pinThread(threadId: string) {
	try {
		await fetch(resolve('/api/discussions/pin'), {
			method: 'POST',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify({ threadId })
		});
		pinnedThreadIds.update((s) => new Set([...s, threadId]));
	} catch (e) {
		console.error('Failed to pin thread:', e);
	}
}

export async function unpinThread(threadId: string) {
	try {
		await fetch(resolve('/api/discussions/pin'), {
			method: 'DELETE',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify({ threadId })
		});
		pinnedThreadIds.update((s) => {
			const next = new Set(s);
			next.delete(threadId);
			return next;
		});
	} catch (e) {
		console.error('Failed to unpin thread:', e);
	}
}
