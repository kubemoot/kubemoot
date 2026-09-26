import { json } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { getNatsConnection, sc } from '$lib/server/nats-client';
import { AckPolicy, DeliverPolicy } from 'nats';

/**
 * REST endpoint that fetches historical messages from a NATS JetStream stream.
 * Unlike the SSE /subscribe endpoint which only shows live messages, this
 * replays stored messages from JetStream.
 *
 * Usage: GET /api/nats/history?stream=KUBEMOOT_DISCUSS&subject=kubemoot.discuss.>&limit=500
 */
export const GET: RequestHandler = async ({ url }) => {
	const stream = url.searchParams.get('stream') || 'KUBEMOOT_DISCUSS';
	const subject = url.searchParams.get('subject') || 'kubemoot.discuss.>';
	const limit = Math.min(parseInt(url.searchParams.get('limit') || '500'), 2000);

	try {
		const nc = await getNatsConnection();
		const jsm = await nc.jetstreamManager();
		const js = nc.jetstream();

		// Check stream exists and has messages
		let info;
		try {
			info = await jsm.streams.info(stream);
		} catch {
			return json({ messages: [] });
		}

		const lastSeq = Number(info.state.last_seq);
		if (info.state.messages === 0) {
			return json({ messages: [], lastSeq });
		}

		// Deliver the NEWEST `limit` messages, not the oldest: start the ephemeral
		// consumer near the stream tip (last_seq - limit + 1). This keeps the
		// Discussions page bounded to the most recent threads instead of replaying
		// the whole stream from sequence 1.
		const startSeq = Math.max(Number(info.state.first_seq), lastSeq - limit + 1);
		const ci = await jsm.consumers.add(stream, {
			ack_policy: AckPolicy.None,
			deliver_policy: DeliverPolicy.StartSequence,
			opt_start_seq: startSeq,
			filter_subject: subject,
			inactive_threshold: 30_000_000_000 // 30 seconds in nanoseconds
		});

		const consumer = await js.consumers.get(stream, ci.name);
		const iter = await consumer.fetch({ max_messages: limit, expires: 5000 });

		const messages: Array<{ subject: string; data: string; seq: number }> = [];
		for await (const msg of iter) {
			try {
				const data = sc.decode(msg.data);
				messages.push({
					subject: msg.subject,
					data,
					seq: msg.seq
				});
			} catch {
				// skip malformed messages
			}
		}

		// Cleanup ephemeral consumer
		try {
			await jsm.consumers.delete(stream, ci.name);
		} catch {
			/* already cleaned up by inactivity */
		}

		return json({ messages, lastSeq });
	} catch (e) {
		const error = e instanceof Error ? e.message : 'Failed to read history';
		return json({ messages: [], error }, { status: 500 });
	}
};
