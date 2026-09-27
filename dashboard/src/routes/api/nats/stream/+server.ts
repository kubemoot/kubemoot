import type { RequestHandler } from './$types';
import { getNatsConnection, sc } from '$lib/server/nats-client';
import { AckPolicy, DeliverPolicy } from 'nats';
import { DISCUSS_ALL } from '$lib/crewScope';

/**
 * Unified SSE endpoint backed by a JetStream ordered consumer.
 * Replays ALL historical messages first (in stream sequence order),
 * then seamlessly transitions to delivering live messages.
 *
 * One data path, guaranteed causal ordering, zero race conditions.
 *
 * Usage: GET /api/nats/stream?stream=KUBEMOOT_DISCUSS&subject=kubemoot.discuss.>&from_seq=0
 *
 * The client tracks the last received `seq` and passes it as `from_seq`
 * on reconnection to resume without replaying already-seen messages.
 */
export const GET: RequestHandler = async ({ url }) => {
	const streamName = url.searchParams.get('stream') || 'KUBEMOOT_DISCUSS';
	const subject = url.searchParams.get('subject') || DISCUSS_ALL;
	const fromSeq = parseInt(url.searchParams.get('from_seq') || '0');

	const stream = new ReadableStream({
		async start(controller) {
			let heartbeatInterval: ReturnType<typeof setInterval> | null = null;
			let consumerName: string | null = null;
			let nc_ref: Awaited<ReturnType<typeof getNatsConnection>> | null = null;

			try {
				const nc = await getNatsConnection();
				nc_ref = nc;
				const jsm = await nc.jetstreamManager();
				const js = nc.jetstream();

				// Verify stream exists
				try {
					await jsm.streams.info(streamName);
				} catch {
					controller.enqueue(
						`data: ${JSON.stringify({ type: 'error', error: `Stream ${streamName} not found` })}\n\n`
					);
					controller.close();
					return;
				}

				// Create ephemeral consumer with ordered delivery
				const deliverPolicy =
					fromSeq > 0 ? DeliverPolicy.StartSequence : DeliverPolicy.All;

				const consumerConfig: Record<string, unknown> = {
					ack_policy: AckPolicy.None,
					deliver_policy: deliverPolicy,
					filter_subject: subject,
					inactive_threshold: 60_000_000_000 // 60s inactivity cleanup
				};

				if (fromSeq > 0) {
					consumerConfig.opt_start_seq = fromSeq + 1; // Resume AFTER last seen
				}

				const ci = await jsm.consumers.add(streamName, consumerConfig);
				consumerName = ci.name;

				const consumer = await js.consumers.get(streamName, ci.name);

				// Send connected event with stream metadata
				controller.enqueue(
					`data: ${JSON.stringify({ type: 'connected', stream: streamName, subject, fromSeq })}\n\n`
				);

				// Heartbeat every 30s
				heartbeatInterval = setInterval(() => {
					try {
						controller.enqueue(': heartbeat\n\n');
					} catch {
						if (heartbeatInterval) clearInterval(heartbeatInterval);
					}
				}, 30000);

				// Consume messages — history first, then live (JetStream handles transition)
				(async () => {
					try {
						const iter = await consumer.consume();
						for await (const msg of iter) {
							try {
								const data = sc.decode(msg.data);
								const event = JSON.stringify({
									type: 'message',
									seq: msg.seq,
									subject: msg.subject,
									data,
									timestamp: new Date().toISOString()
								});
								controller.enqueue(`data: ${event}\n\n`);
							} catch {
								// Skip malformed messages
							}
						}
					} catch {
						// Consumer closed (client disconnected or stream deleted)
					} finally {
						if (heartbeatInterval) clearInterval(heartbeatInterval);
					}
				})();

				// Store cleanup for cancel()
				(controller as any)._streamCleanup = async () => {
					if (heartbeatInterval) clearInterval(heartbeatInterval);
					// Delete ephemeral consumer on disconnect
					if (consumerName && nc_ref && !nc_ref.isClosed()) {
						try {
							const m = await nc_ref.jetstreamManager();
							await m.consumers.delete(streamName, consumerName);
						} catch {
							/* already cleaned up */
						}
					}
				};
			} catch (err) {
				if (heartbeatInterval) clearInterval(heartbeatInterval);
				const errorMsg = err instanceof Error ? err.message : 'Failed to connect to NATS';
				controller.enqueue(
					`data: ${JSON.stringify({ type: 'error', error: errorMsg })}\n\n`
				);
				controller.close();
			}
		},
		cancel(controller) {
			const cleanup = (controller as any)?._streamCleanup;
			if (typeof cleanup === 'function') cleanup();
		}
	});

	return new Response(stream, {
		headers: {
			'Content-Type': 'text/event-stream',
			'Cache-Control': 'no-cache',
			Connection: 'keep-alive'
		}
	});
};
