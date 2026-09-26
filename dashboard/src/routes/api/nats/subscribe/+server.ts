import type { RequestHandler } from './$types';
import { getNatsConnection, sc } from '$lib/server/nats-client';

/**
 * SSE endpoint that subscribes to a NATS subject and streams messages to the browser.
 * This proxies NATS messages through the SvelteKit server, avoiding the need for
 * direct browser-to-NATS WebSocket connections (which fail due to mixed content
 * when the dashboard is served over HTTPS).
 *
 * Usage: GET /api/nats/subscribe?subject=kubemoot.>
 */
export const GET: RequestHandler = async ({ url }) => {
	const subject = url.searchParams.get('subject') || 'kubemoot.>';

	const stream = new ReadableStream({
		async start(controller) {
			let heartbeatInterval: ReturnType<typeof setInterval> | null = null;

			try {
				const nc = await getNatsConnection();
				const sub = nc.subscribe(subject);

				// Send initial connected event
				controller.enqueue(`data: ${JSON.stringify({ type: 'connected', subject })}\n\n`);

				// Send heartbeat every 30s to prevent proxy idle timeout
				heartbeatInterval = setInterval(() => {
					try {
						controller.enqueue(': heartbeat\n\n');
					} catch {
						if (heartbeatInterval) clearInterval(heartbeatInterval);
					}
				}, 30000);

				// Stream messages as SSE events
				(async () => {
					try {
						for await (const msg of sub) {
							const data = sc.decode(msg.data);
							const event = JSON.stringify({
								type: 'message',
								subject: msg.subject,
								data,
								timestamp: new Date().toISOString()
							});
							controller.enqueue(`data: ${event}\n\n`);
						}
					} catch {
						// Subscription ended (client disconnected)
					} finally {
						if (heartbeatInterval) clearInterval(heartbeatInterval);
					}
				})();

				// Clean up when client disconnects
				// Store cleanup references on the controller for cancel() to use
				(controller as any)._natsCleanup = () => {
					if (heartbeatInterval) clearInterval(heartbeatInterval);
					sub.unsubscribe();
				};
			} catch (err) {
				if (heartbeatInterval) clearInterval(heartbeatInterval);
				const errorMsg = err instanceof Error ? err.message : 'Failed to connect to NATS';
				controller.enqueue(`data: ${JSON.stringify({ type: 'error', error: errorMsg })}\n\n`);
				controller.close();
			}
		},
		cancel(controller) {
			// Client disconnected — run cleanup
			const cleanup = (controller as any)?._natsCleanup;
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
