import type { RequestHandler } from './$types';
import {
	listModelProviders,
	listModels,
	listEmbeddingModels,
	listMCPServers,
	listMCPGateways,
	listRAGSources,
	listAgents
} from '$lib/server/k8s';

const POLL_INTERVAL = 5000; // 5 seconds

export const GET: RequestHandler = async ({ url }) => {
	const namespace = url.searchParams.get('namespace') || 'kubemoot';

	const stream = new ReadableStream({
		async start(controller) {
			const encoder = new TextEncoder();

			const sendEvent = (event: string, data: unknown) => {
				controller.enqueue(encoder.encode(`event: ${event}\n`));
				controller.enqueue(encoder.encode(`data: ${JSON.stringify(data)}\n\n`));
			};

			const fetchAndSend = async () => {
				try {
					// Fetch all resources in parallel
					const [
						providers,
						models,
						embeddings,
						mcpServers,
						mcpGateways,
						ragSources,
						agents
					] = await Promise.all([
						listModelProviders(namespace).catch(() => ({ items: [] })),
						listModels(namespace).catch(() => ({ items: [] })),
						listEmbeddingModels(namespace).catch(() => ({ items: [] })),
						listMCPServers(namespace).catch(() => ({ items: [] })),
						listMCPGateways(namespace).catch(() => ({ items: [] })),
						listRAGSources(namespace).catch(() => ({ items: [] })),
						listAgents(namespace).catch(() => ({ items: [] }))
					]);

					sendEvent('update', {
						timestamp: new Date().toISOString(),
						namespace,
						resources: {
							modelproviders: providers.items,
							models: models.items,
							embeddingmodels: embeddings.items,
							mcpservers: mcpServers.items,
							mcpgateways: mcpGateways.items,
							ragsources: ragSources.items,
							agents: agents.items
						}
					});
				} catch (error) {
					sendEvent('error', {
						timestamp: new Date().toISOString(),
						message: error instanceof Error ? error.message : 'Unknown error'
					});
				}
			};

			// Send initial data
			await fetchAndSend();

			// Set up polling
			const intervalId = setInterval(() => void fetchAndSend(), POLL_INTERVAL);

			// Send heartbeat to keep connection alive
			const heartbeatId = setInterval(() => {
				sendEvent('heartbeat', { timestamp: new Date().toISOString() });
			}, 30000);

			// Clean up on close
			return () => {
				clearInterval(intervalId);
				clearInterval(heartbeatId);
			};
		}
	});

	return new Response(stream, {
		headers: {
			'Content-Type': 'text/event-stream',
			'Cache-Control': 'no-cache',
			'Connection': 'keep-alive'
		}
	});
};
