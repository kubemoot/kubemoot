import type { RequestHandler } from './$types';
import { getNatsConnection, sc } from '#lib/server/nats-client.js';
import { AckPolicy, DeliverPolicy, type JetStreamManager, type JsMsg } from 'nats';
import { guardStreamRead, messageSubjectAllowed } from '#lib/server/scope.js';
import { MAX_LIMIT, isLiteralSubject, parseParams, startPlan, type Params } from '#lib/server/history-read.js';
import { recordHistoryRead, serverTimingHeader, type HistoryTiming } from '#lib/server/history-timing.js';

interface HistoryMessage {
	subject: string;
	data: string;
	seq: number;
}

/** The fetched messages decoded to text, stopping before sequence `before`; a message that does not decode is skipped. */
async function decodeMessages(iter: AsyncIterable<JsMsg>, before: number) {
	const messages: HistoryMessage[] = [];
	for await (const msg of iter) {
		if (msg.seq >= before) break;
		try {
			if (!messageSubjectAllowed(msg.subject)) continue;
			messages.push({ subject: msg.subject, data: sc.decode(msg.data), seq: msg.seq });
		} catch {
			// skip malformed messages
		}
	}
	return messages;
}

async function readWindow(jsm: JetStreamManager, nc: Awaited<ReturnType<typeof getNatsConnection>>, p: Params, startSeq: number, policy: DeliverPolicy) {
	const ci = await jsm.consumers.add(p.stream, {
		ack_policy: AckPolicy.None,
		deliver_policy: policy,
		...(policy === DeliverPolicy.StartSequence ? { opt_start_seq: startSeq } : {}),
		filter_subject: p.subject,
		inactive_threshold: 30_000_000_000 // 30 seconds in nanoseconds
	});
	const setupDone = performance.now();
	try {
		// Ask for no more than the consumer holds: a fetch for more waits out its expiry.
		const want = Math.min(p.limit, ci.num_pending);
		if (want === 0) return { messages: [] as HistoryMessage[], setupDone, pending: 0 };
		const consumer = await nc.jetstream().consumers.get(p.stream, ci.name);
		const iter = await consumer.fetch({ max_messages: want, expires: 5000 });
		return { messages: await decodeMessages(iter, p.before), setupDone, pending: ci.num_pending };
	} finally {
		try {
			await jsm.consumers.delete(p.stream, ci.name);
		} catch {
			/* already cleaned up by inactivity */
		}
	}
}

/** Windows a page may skip when none of their messages is one this dashboard shows. */
const MAX_EMPTY_WINDOWS = 1000;

/**
 * Reads a window, and while a stream read comes back empty (a scoped dashboard drops other
 * namespaces' messages after the fetch) reads the next older, larger window, so a page
 * holds messages or says there is nothing older.
 */
async function readPage(jsm: JetStreamManager, nc: Awaited<ReturnType<typeof getNatsConnection>>, params: Params, firstSeq: number, lastSeq: number) {
	let p = params;
	let plan = startPlan(p, firstSeq, lastSeq);
	let read = await readWindow(jsm, nc, p, plan.startSeq, plan.policy);
	const setupDone = read.setupDone;
	for (let skipped = 0; read.messages.length === 0 && !isLiteralSubject(p.subject) && plan.startSeq > firstSeq && skipped < MAX_EMPTY_WINDOWS; skipped++) {
		p = { ...p, before: plan.startSeq, limit: MAX_LIMIT };
		plan = startPlan(p, firstSeq, lastSeq);
		read = await readWindow(jsm, nc, p, plan.startSeq, plan.policy);
	}
	return { ...read, setupDone, startSeq: plan.startSeq, limit: params.limit, ended: read.messages.length === 0 };
}

function respond(body: object, timing: Omit<HistoryTiming, 'bytes' | 'totalMs'>, started: number, status = 200): Response {
	const text = JSON.stringify(body);
	const full: HistoryTiming = { ...timing, bytes: Buffer.byteLength(text), totalMs: Math.round(performance.now() - started) };
	recordHistoryRead(full);
	return new Response(text, {
		status,
		headers: { 'Content-Type': 'application/json', 'Server-Timing': serverTimingHeader(full) }
	});
}

/**
 * REST endpoint that fetches historical messages from a NATS JetStream stream.
 * Unlike the SSE /subscribe endpoint which only shows live messages, this
 * replays stored messages from JetStream.
 *
 * Usage:
 *   GET /api/nats/history?stream=KUBEMOOT_DISCUSS&subject=kubemoot.discuss.>&limit=200
 *       the newest 200 sequence numbers of the stream
 *   ...&before=<seq>      the 200 before that sequence, to page back
 *   ...&subject=kubemoot.discuss.<ns>.<crew>.<channel>.<thread>
 *       every message of one thread, and nothing else
 *
 * A thread read returns up to `limit` messages and sets `truncated` when it holds more.
 * The answer carries `windowStart`, the sequence to pass as `before` for the next page
 * back, and `hasMore` when older messages exist. The Server-Timing header and one log
 * line report durations, size and count, never content.
 */
export const GET: RequestHandler = async ({ url }) => {
	const started = performance.now();
	const params = parseParams(url);
	const scope = isLiteralSubject(params.subject) ? 'thread' : 'stream';

	const denied = await guardStreamRead(params.stream, params.subject);
	if (denied) return denied;

	try {
		const nc = await getNatsConnection();
		const jsm = await nc.jetstreamManager();

		// Check stream exists and has messages
		let info;
		try {
			info = await jsm.streams.info(params.stream);
		} catch {
			return Response.json({ messages: [] });
		}

		const lastSeq = Number(info.state.last_seq);
		if (info.state.messages === 0) {
			return Response.json({ messages: [], lastSeq });
		}

		const firstSeq = Number(info.state.first_seq);
		const { messages, setupDone, pending, startSeq, ended } = await readPage(jsm, nc, params, firstSeq, lastSeq);
		const timing = {
			scope,
			messages: messages.length,
			setupMs: Math.round(setupDone - started),
			fetchMs: Math.round(performance.now() - setupDone)
		} as const;
		// An empty page never claims older messages: paging ends instead of looping on nothing.
		const hasMore = scope === 'stream' && startSeq > firstSeq && !ended;
		const truncated = scope === 'thread' && pending > params.limit;
		return respond({ messages, lastSeq, windowStart: startSeq, hasMore, truncated }, timing, started);
	} catch (e) {
		const error = e instanceof Error ? e.message : 'Failed to read history';
		return Response.json({ messages: [], error }, { status: 500 });
	}
};
