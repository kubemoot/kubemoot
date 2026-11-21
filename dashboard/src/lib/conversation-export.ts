// The download of a crew's conversations: the request the dashboard sends and which
// conversation a thread belongs to. The zip is built by the operator (one
// implementation); nothing here reads a conversation or names the file. The browser
// takes the file name from the operator's Content-Disposition.

import type { CrewScope } from '#lib/crewScope.js';
import type { Thread } from '#lib/stores/discussions.js';

/** The most conversations one request may ask for; the operator refuses more. */
export const MAX_EXPORT_IDS = 50;

const VALID_ID = /^[A-Za-z0-9_-]{1,128}$/;
const VALID_ZONE = /^[A-Za-z0-9_+\-/]{1,64}$/;

/** True for an id the operator accepts: 1 to 128 letters, digits, `-` or `_`. */
export function isConversationId(id: unknown): id is string {
	return typeof id === 'string' && VALID_ID.test(id);
}

/** A time zone name in the shape of an IANA zone; anything else is dropped, and the operator writes UTC. */
export function cleanTimeZone(zone: string | null | undefined): string | undefined {
	return zone && VALID_ZONE.test(zone) ? zone : undefined;
}

/** The browser's time zone, so the export writes times as the person reads them. */
export function browserTimeZone(): string | undefined {
	try {
		return cleanTimeZone(Intl.DateTimeFormat().resolvedOptions().timeZone);
	} catch {
		return undefined;
	}
}

/** The conversation a thread belongs to, from its opening message; absent for an older thread. */
export function threadConversationId(thread: Pick<Thread, 'messages'>): string | undefined {
	for (const msg of thread.messages) {
		const id = msg.metadata?.conversationId;
		if (msg.messageType === 'thread_start' && isConversationId(id)) return id;
	}
	return undefined;
}

/** The distinct conversation ids of some threads, in the order first seen. */
export function conversationIds(threads: Iterable<Pick<Thread, 'messages'>>): string[] {
	const seen = new Set<string>();
	for (const t of threads) {
		const id = threadConversationId(t);
		if (id) seen.add(id);
	}
	return [...seen];
}

/**
 * The query string of an export request: one `id` per conversation, and the zone when known.
 * Throws when there is no id, too many, or one the operator would refuse.
 */
export function exportQuery(scope: CrewScope, ids: readonly string[], timeZone?: string): string {
	if (ids.length === 0 || ids.length > MAX_EXPORT_IDS || !ids.every((id) => isConversationId(id))) {
		throw new Error(`export needs 1 to ${MAX_EXPORT_IDS} valid conversation ids`);
	}
	const q = new URLSearchParams({ namespace: scope.namespace, crew: scope.crew });
	for (const id of ids) q.append('id', id);
	const zone = cleanTimeZone(timeZone);
	if (zone) q.set('timeZone', zone);
	return q.toString();
}

/** The link that downloads the conversations, or null when none can be asked for. */
export function exportHref(endpoint: string, scope: CrewScope | null, ids: readonly string[], timeZone?: string): string | null {
	if (!scope || ids.length === 0 || ids.length > MAX_EXPORT_IDS) return null;
	return `${endpoint}?${exportQuery(scope, ids, timeZone)}`;
}

/** The label of the download: one conversation, or how many. */
export function exportLabel(count: number): string {
	return count === 1 ? 'Download conversation' : `Download ${count} conversations`;
}
