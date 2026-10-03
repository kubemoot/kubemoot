import type { RequestHandler } from './$types';
import { AckPolicy, DeliverPolicy, type ConsumerMessages, type JsMsg } from 'nats';
import { getNatsConnection, sc } from '$lib/server/nats-client';
import { relayToSse, sseResponse, type SseSink } from '$lib/server/sse';
import { DISCUSS_ALL } from '$lib/crewScope';
import { guardStreamRead, messageSubjectAllowed } from '$lib/server/scope';

/** Server-side backstop: NATS removes a consumer idle this long (nanoseconds). */
const CONSUMER_INACTIVE_NS = 60_000_000_000;

/** Ephemeral consumer config: everything on `subject`, or everything after `fromSeq`. */
function consumerConfig(subject: string, fromSeq: number): Record<string, unknown> {
	const config: Record<string, unknown> = {
		ack_policy: AckPolicy.None,
		deliver_policy: fromSeq > 0 ? DeliverPolicy.StartSequence : DeliverPolicy.All,
		filter_subject: subject,
		inactive_threshold: CONSUMER_INACTIVE_NS
	};
	if (fromSeq > 0) {
		config.opt_start_seq = fromSeq + 1; // resume AFTER the last seen sequence
	}
	return config;
}

/**
 * Opens an ephemeral JetStream consumer and relays it to the sink. Returns the
 * cleanup that stops the consumer and deletes it, so a browser disconnect frees
 * it at once instead of leaving it to the inactivity backstop.
 */
async function openConsumer(
	sink: SseSink,
	streamName: string,
	subject: string,
	fromSeq: number
): Promise<(() => Promise<void>) | void> {
	const nc = await getNatsConnection();
	const jsm = await nc.jetstreamManager();
	try {
		await jsm.streams.info(streamName);
	} catch {
		sink.data({ type: 'error', error: `Stream ${streamName} not found` });
		sink.close();
		return;
	}

	const info = await jsm.consumers.add(streamName, consumerConfig(subject, fromSeq));
	const deleteConsumer = async () => {
		if (!nc.isClosed()) await jsm.consumers.delete(streamName, info.name);
	};
	let messages: ConsumerMessages;
	try {
		const consumer = await nc.jetstream().consumers.get(streamName, info.name);
		messages = await consumer.consume();
	} catch (err) {
		await deleteConsumer().catch(() => undefined);
		throw err;
	}

	sink.data({ type: 'connected', stream: streamName, subject, fromSeq });
	void relayToSse(messages, sink, (msg: JsMsg) =>
		messageSubjectAllowed(msg.subject)
			? {
					type: 'message',
					seq: msg.seq,
					subject: msg.subject,
					data: sc.decode(msg.data),
					timestamp: new Date().toISOString()
				}
			: undefined
	);

	return async () => {
		await messages.close();
		await deleteConsumer();
	};
}

/**
 * Unified SSE endpoint backed by a JetStream consumer.
 * Replays ALL historical messages first (in stream sequence order),
 * then seamlessly transitions to delivering live messages.
 *
 * Usage: GET /api/nats/stream?stream=KUBEMOOT_DISCUSS&subject=kubemoot.discuss.>&from_seq=0
 *
 * The client tracks the last received `seq` and passes it as `from_seq`
 * on reconnection to resume without replaying already-seen messages.
 */
export const GET: RequestHandler = async ({ url }) => {
	const streamName = url.searchParams.get('stream') || 'KUBEMOOT_DISCUSS';
	const subject = url.searchParams.get('subject') || DISCUSS_ALL;
	const fromSeq = Number.parseInt(url.searchParams.get('from_seq') || '0', 10) || 0;

	const denied = await guardStreamRead(streamName, subject);
	if (denied) return denied;

	return sseResponse((sink) => openConsumer(sink, streamName, subject, fromSeq));
};
