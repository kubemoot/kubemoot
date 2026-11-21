// A crew's share of the NATS stores, from subject-filtered stream info and bounded
// listings. Counts come from NATS; a stream's per-subject info carries no bytes, so
// bytes in a stream or KV bucket are the crew's share of the messages applied to the
// store's bytes and flagged as an estimate. Object stores list exact sizes.

/** Most subjects or objects read for one crew in one store before the page says "more than". */
export const CREW_ENTRY_CAP = 5000;

export interface CrewStoreUsage {
	store: string;
	messages: number;
	/** Distinct keys or subjects (objects for an object store). */
	keys: number;
	bytes: number;
	/** True when bytes are the message share of the store's bytes, not an exact size. */
	bytesEstimated: boolean;
	/** True when more than the cap exists: the numbers are a lower bound. */
	capped: boolean;
}

export interface SubjectPage {
	/** Subject to message count, as NATS answered for the crew's filter. */
	subjects: Record<string, number>;
	/** Distinct subjects NATS says match the filter, which may exceed what the page holds. */
	total: number;
}

/** Totals for one crew in one stream or KV bucket from its filtered subject page. */
export function usageFromSubjects(
	store: string,
	page: SubjectPage,
	streamMessages: number,
	streamBytes: number,
	cap: number = CREW_ENTRY_CAP
): CrewStoreUsage {
	const counts = Object.values(page.subjects);
	const read = counts.slice(0, cap);
	const messages = read.reduce((n, c) => n + c, 0);
	const capped = page.total > read.length;
	const share = streamMessages > 0 ? Math.min(1, messages / streamMessages) : 0;
	return {
		store,
		messages,
		keys: capped ? Math.max(page.total, read.length) : read.length,
		bytes: Math.round(streamBytes * share),
		bytesEstimated: true,
		capped
	};
}

export interface ObjectEntry {
	name: string;
	size: number;
}

/** Exact totals for one crew in an object store from its (already bounded) object listing. */
export function usageFromObjects(store: string, entries: ObjectEntry[], capped: boolean): CrewStoreUsage {
	return {
		store,
		messages: entries.length,
		keys: entries.length,
		bytes: entries.reduce((n, e) => n + e.size, 0),
		bytesEstimated: false,
		capped
	};
}

/** "1,234", or "more than 1,234" when the count is a lower bound. */
export function formatCapped(value: number, capped: boolean): string {
	const text = value.toLocaleString('en-US');
	return capped ? `more than ${text}` : text;
}

/** Subject filter for the stream that holds a KV key: the bucket's subject prefix plus the key pattern. */
export function kvSubjectFilter(bucket: string, keyPattern: string): string {
	return `$KV.${bucket}.${keyPattern}`;
}
