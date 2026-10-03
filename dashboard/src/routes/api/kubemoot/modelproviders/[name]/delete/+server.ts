import { json, error } from '@sveltejs/kit';
import type { RequestHandler } from './$types';
import { executeOllamaAction, resolveOllamaEndpoint } from '$lib/server/ollama-actions';
import { guardNamespace } from '$lib/server/scope';

// The model to delete from the request body; a missing model or confirmation is a 400.
async function confirmedModel(request: Request): Promise<string> {
	const body = await request.json().catch(() => ({}));
	const model = typeof body?.model === 'string' ? body.model.trim() : '';
	if (!model) {
		throw error(400, 'request body must include { model: string }');
	}
	if (body?.confirm !== true) {
		throw error(400, 'destructive action requires { confirm: true }');
	}
	return model;
}

export const POST: RequestHandler = async ({ params, url, request }) => {
	const namespace = url.searchParams.get('namespace') || 'kubemoot';
	const denied = await guardNamespace(namespace, 'infra');
	if (denied) return denied;
	const { name } = params;

	const model = await confirmedModel(request);

	const endpoint = await resolveOllamaEndpoint(namespace, name);
	if (!endpoint) {
		throw error(400, `ModelProvider ${name} is not an Ollama-typed provider with an endpoint`);
	}

	try {
		const result = await executeOllamaAction(endpoint, model, 'delete');
		return json(result);
	} catch (e) {
		const message = e instanceof Error ? e.message : 'delete failed';
		throw error(502, message);
	}
};
