import { writable, derived, get } from 'svelte/store';
import { browser } from '$app/env';
import { resolve } from '$app/paths';
import type { DiscussionMessage } from '#lib/types/kubemoot.js';
import {
	DISCUSS_ALL,
	parseDiscussSubject,
	tryCrewScope,
	type CrewScope,
	type DiscussSubject
} from '#lib/crewScope.js';

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
	/** The NATS subject of the thread, which a thread-scoped history read needs. */
	subject?: string;
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

/** A new thread opened by its thread_start message. */
function threadFromStart(msg: DiscussionMessage, subject: DiscussSubject | null): Thread {
	return {
		threadId: msg.threadId,
		channel: msg.channel ?? '',
		...scopeFields(msg, subject),
		crewVersion: msg.metadata?.crewVersion,
		userQuery: msg.metadata?.userQuery || msg.content || '',
		startedBy: msg.agentName,
		startedAt: msg.timestamp ?? '',
		messages: [msg],
		status: 'open',
		...emptyThreadCounts()
	};
}

function applyThreadStart(
	threads: Map<string, Thread>,
	existing: Thread | undefined,
	msg: DiscussionMessage,
	subject: DiscussSubject | null
) {
	if (!existing) {
		threads.set(msg.threadId, threadFromStart(msg, subject));
		return;
	}
	// With ordered stream delivery this path rarely fires,
	// but kept for robustness (e.g., broadcast duplicates)
	const query = msg.metadata?.userQuery || msg.content;
	if (query && !existing.userQuery) {
		existing.userQuery = query;
		existing.startedBy = msg.agentName;
		existing.startedAt = msg.timestamp ?? '';
		existing.channel = msg.channel ?? '';
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
		startedAt: msg.timestamp ?? '',
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
 * Folds one discussion message into the thread map, in place. `subject` is the NATS
 * subject the message arrived on; its namespace and crew scope the thread. Returns
 * false when the message was already there.
 */
function foldMessage(threads: Map<string, Thread>, msg: DiscussionMessage, subject?: string): boolean {
	const parsed = parseDiscussSubject(subject);
	const existing = threads.get(msg.threadId);

	// Dedup by messageId
	if (existing && msg.messageId && existing.messages.some((m) => m.messageId === msg.messageId)) {
		return false;
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
	const folded = threads.get(msg.threadId);
	if (folded && subject) folded.subject ??= subject;
	return true;
}

/** Drops threads from the view, as after the conversation they belong to is deleted. */
export function forgetThreads(threadIds: readonly string[]) {
	threadsMap.update((threads) => {
		let changed = false;
		for (const id of threadIds) changed = threads.delete(id) || changed;
		return changed ? new Map(threads) : threads;
	});
}

/** Folds one live discussion message into the threads and notifies subscribers once. */
export function handleMessage(msg: DiscussionMessage, subject?: string) {
	threadsMap.update((threads) => (foldMessage(threads, msg, subject) ? new Map(threads) : threads));
}

/** A message as the history endpoint returns it. */
interface HistoryMessage {
	seq?: unknown;
	data?: unknown;
	subject?: string;
}

/** The message a history entry holds, or null when it is not a discussion message. */
function parseHistoryMessage(m: HistoryMessage): DiscussionMessage | null {
	try {
		const d = typeof m.data === 'string' ? JSON.parse(m.data) : m.data;
		return d?.threadId && d.messageType ? (d as DiscussionMessage) : null;
	} catch {
		return null;
	}
}

/** Folds a page of history into the threads with one notification, not one per message. */
export function foldHistory(messages: HistoryMessage[]) {
	threadsMap.update((threads) => {
		for (const m of messages) {
			if (typeof m.seq === 'number' && m.seq > lastSeq) lastSeq = m.seq;
			const msg = parseHistoryMessage(m);
			if (msg) foldMessage(threads, msg, m.subject);
		}
		return new Map(threads);
	});
}

/** What identifies a message: its id, else its time, sender and type. */
function messageKey(m: DiscussionMessage): string {
	return m.messageId ?? `${m.timestamp}|${m.agentName}|${m.messageType}`;
}

/** A message's time for ordering; one with no usable time sorts last, in arrival order. */
function timeRank(m: DiscussionMessage): number {
	const t = Date.parse(m.timestamp ?? '');
	return Number.isNaN(t) ? Number.MAX_SAFE_INTEGER : t;
}

/**
 * Replaces one thread with everything the stream holds for it, keeping any live message
 * that arrived while the read was in flight. Messages are ordered by time, ties by the
 * order they arrived.
 */
function mergeThread(threadId: string, fetched: HistoryMessage[]) {
	threadsMap.update((threads) => {
		const held = threads.get(threadId)?.messages ?? [];
		const heldSubject = threads.get(threadId)?.subject;
		const parsed = fetched.flatMap((m) => {
			const msg = parseHistoryMessage(m);
			return msg ? [{ msg, subject: m.subject }] : [];
		});
		const known = new Set(parsed.map((p) => messageKey(p.msg)));
		const all = [
			...parsed,
			...held.filter((m) => !known.has(messageKey(m))).map((msg) => ({ msg, subject: heldSubject }))
		].sort((a, b) => timeRank(a.msg) - timeRank(b.msg));
		threads.delete(threadId);
		for (const { msg, subject } of all) foldMessage(threads, msg, subject);
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

// How many of the newest sequence numbers one history page covers. The page needs only
// enough to list the newest threads; opening a thread reads that thread's own subject,
// and older threads load a page at a time when the list is scrolled to its end.
export const HISTORY_PAGE = 200;

/** True while an older page is being read. */
export const loadingOlder = writable(false);
/** True while the stream holds messages older than the oldest page loaded. */
export const hasOlder = writable(false);

// The sequence the oldest loaded page starts at: the next page back is read before it.
let windowStart = 0;

const historyUrl = (subject: string, extra: string) =>
	`${resolve('/api/nats/history')}?stream=KUBEMOOT_DISCUSS&subject=${encodeURIComponent(subject)}&${extra}`;

interface HistoryPage {
	messages: HistoryMessage[];
	lastSeq?: number;
	windowStart?: number;
	hasMore?: boolean;
	/** A thread read that stopped at the per-read maximum. */
	truncated?: boolean;
}

async function fetchHistory(url: string): Promise<HistoryPage | null> {
	try {
		const res = await fetch(url);
		return res.ok ? ((await res.json()) as HistoryPage) : null;
	} catch {
		return null;
	}
}

// Threads whose own subject was read, or is being read, so a thread is fetched once.
const threadReads = new Set<string>();

/** A thread started before the loaded pages has no start message: its title and counts are partial. */
function isPartial(thread: Thread): boolean {
	return !thread.messages.some((m) => m.messageType === 'thread_start');
}

/**
 * Reads everything the stream holds for one thread, by its subject, and merges it in. A no-op
 * for a thread already read, one that is complete, or one whose subject is not known.
 */
export async function ensureThreadLoaded(threadId: string): Promise<void> {
	const thread = get(threadsMap).get(threadId);
	if (!thread?.subject || !isPartial(thread) || threadReads.has(threadId)) return;
	threadReads.add(threadId);
	const page = await fetchHistory(historyUrl(thread.subject, 'limit=2000'));
	if (page) mergeThread(threadId, page.messages ?? []);
	if (page?.truncated) console.warn('discussions: a thread holds more messages than one read returns');
	else threadReads.delete(threadId);
}

const HYDRATE_CONCURRENCY = 4;

/** Reads the partial threads at the edge of a page, a few at a time. */
async function completePartialThreads(): Promise<void> {
	const ids = [...get(threadsMap).values()].filter(isPartial).map((t) => t.threadId);
	let next = 0;
	const worker = async () => {
		while (next < ids.length) await ensureThreadLoaded(ids[next++]);
	};
	await Promise.all(Array.from({ length: Math.min(HYDRATE_CONCURRENCY, ids.length) }, worker));
}

function noteWindow(page: HistoryPage) {
	if (typeof page.lastSeq === 'number' && page.lastSeq > lastSeq) lastSeq = page.lastSeq;
	if (typeof page.windowStart === 'number') windowStart = page.windowStart;
	hasOlder.set(page.hasMore === true);
}

// loadRecent pulls the newest HISTORY_PAGE sequence numbers, builds the recent thread
// map, and records the stream tip so the live tail starts there rather than replaying the
// entire stream on every page load.
async function loadRecent() {
	const page = await fetchHistory(historyUrl(DISCUSS_ALL, `limit=${HISTORY_PAGE}`));
	if (page) {
		foldHistory(page.messages ?? []);
		noteWindow(page);
	}
	streamReady.set(true);
	// A thread whose start is older than the page shows its answer as its title until its own
	// subject is read; the lists name a thread by its question, so read those now.
	void completePartialThreads();
}

/** Reads the page of history before the oldest one loaded; call when the thread list is scrolled to its end. */
export async function loadOlder(): Promise<void> {
	if (get(loadingOlder) || !get(hasOlder)) return;
	loadingOlder.set(true);
	try {
		const page = await fetchHistory(historyUrl(DISCUSS_ALL, `limit=${HISTORY_PAGE}&before=${windowStart}`));
		if (!page) return;
		foldHistory(page.messages ?? []);
		noteWindow(page);
		await completePartialThreads();
	} finally {
		loadingOlder.set(false);
	}
}

/** Call from onMount - bounded recent load, then the live tail. No-op if already initialized. */
export function initDiscussions() {
	if (!browser || initialized) return;
	initialized = true;
	loadRecent()
		.then(() => {
			startStream();
			void completePartialThreads();
		})
		.catch((err) => console.error('discussions: initial load/stream failed', err));
}

export const threads = {
	subscribe: threadsMap.subscribe
};

export const sortedThreads = derived(threadsMap, ($threads) =>
	Array.from($threads.values()).sort(
		(a, b) => new Date(b.startedAt).getTime() - new Date(a.startedAt).getTime()
	)
);
