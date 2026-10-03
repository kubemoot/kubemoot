import { json, error } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { executeOllamaAction, resolveOllamaEndpoint } from '$lib/server/ollama-actions';
import { guardNamespace } from '$lib/server/scope';

export const POST: RequestHandler = async ({ params, url, request }) => {
	const namespace = url.searchParams.get('namespace') || 'kubemoot';
	const denied = await guardNamespace(namespace, 'infra');
	if (denied) return denied;
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
		const result = await executeOllamaAction(endpoint, model, 'load');
		return json(result);
	} catch (e) {
		const message = e instanceof Error ? e.message : 'load failed';
		throw error(502, message);
	}
};
