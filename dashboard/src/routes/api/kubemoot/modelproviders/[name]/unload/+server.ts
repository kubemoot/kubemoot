import { json, error } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { executeOllamaAction, resolveOllamaEndpoint } from '$lib/server/ollama-actions';

export const POST: RequestHandler = async ({ params, url, request }) => {
	const namespace = url.searchParams.get('namespace') || 'kubemoot';
	const { name } = params;

	const body = await request.json().catch(() => ({}));
	const model = typeof body?.model === 'string' ? body.model.trim() : '';
	if (!model) {
		throw error(400, 'request body must include { model: string }');
	}

	const endpoint = await resolveOllamaEndpoint(namespace, name);
	if (!endpoint) {
		throw error(400, `ModelProvider ${name} is not an Ollama-typed provider with an endpoint`);
	}

	try {
		const result = await executeOllamaAction(endpoint, model, 'unload');
		return json(result);
	} catch (e) {
		const message = e instanceof Error ? e.message : 'unload failed';
		throw error(502, message);
	}
};
