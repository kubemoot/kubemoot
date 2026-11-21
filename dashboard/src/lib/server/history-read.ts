// How a history read is asked for and where it starts, apart from the NATS calls.
import { DeliverPolicy } from 'nats';
import { DISCUSS_ALL } from '#lib/crewScope.js';

export const MAX_LIMIT = 2000;

/** True for a subject filter that names one subject (a thread), not a pattern. */
export function isLiteralSubject(subject: string): boolean {
	return !subject.includes('*') && !subject.includes('>');
}

export interface Params {
	stream: string;
	subject: string;
	limit: number;
	/** Only messages with a sequence below this; Infinity for none. */
	before: number;
}

function numberParam(raw: string | null, fallback: number): number {
	const n = Number.parseInt(raw ?? '', 10);
	return Number.isFinite(n) && n > 0 ? n : fallback;
}

export function parseParams(url: URL): Params {
	return {
		stream: url.searchParams.get('stream') || 'KUBEMOOT_DISCUSS',
		subject: url.searchParams.get('subject') || DISCUSS_ALL,
		limit: Math.min(numberParam(url.searchParams.get('limit'), 500), MAX_LIMIT),
		before: numberParam(url.searchParams.get('before'), Number.POSITIVE_INFINITY)
	};
}

/**
 * Where a read starts. One thread (a literal subject) is read whole from the start of
 * the stream, since the server skips every other subject. A pattern reads the newest
 * `limit` sequence numbers below `before`, so the Discussions page stays bounded to
 * recent threads and can page back from the oldest sequence it holds.
 */
export function startPlan(p: Params, firstSeq: number, lastSeq: number): { policy: DeliverPolicy; startSeq: number } {
	if (isLiteralSubject(p.subject)) return { policy: DeliverPolicy.All, startSeq: firstSeq };
	const end = Math.min(lastSeq, p.before - 1);
	return { policy: DeliverPolicy.StartSequence, startSeq: Math.max(firstSeq, end - p.limit + 1) };
}

