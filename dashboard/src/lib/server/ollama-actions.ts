// Server-side helpers that talk to an Ollama ModelProvider directly,
// driving the model lifecycle the dashboard ModelProviderCard exposes.
//
// Three lifecycle actions:
//   - load:   POST /api/generate with a tiny prompt and num_predict=1.
//             Ollama loads the model into VRAM as a side effect.
//             Cached (on disk) → Loaded (in VRAM).
//   - unload: POST /api/generate with keep_alive=0. Ollama evicts the
//             model from VRAM immediately. Loaded → Cached. Reversible.
//   - delete: DELETE /api/delete. Ollama removes the model files from
//             disk. Loaded/Cached → Not present. Destructive: recovery
//             requires re-pulling the model (potentially many GB).
//
// The endpoint URL is taken from ModelProvider.spec.endpoint as-is. The
// helper appends the appropriate path per action.

import { getModelProvider } from '$lib/server/k8s';
import { trimTrailingSlashes } from '$lib/text-utils';

export type OllamaAction = 'load' | 'unload' | 'delete';

export interface OllamaActionResult {
	provider: string;
	model: string;
	action: OllamaAction;
	durationMs: number;
	loadDurationMs?: number;
}

const ACTION_TIMEOUT_MS = 90_000;

/**
 * Resolve a ModelProvider's HTTP endpoint to the Ollama base URL.
 * Returns null if the provider is not Ollama-typed or has no endpoint.
 */
export async function resolveOllamaEndpoint(
	namespace: string,
	providerName: string
): Promise<string | null> {
	const provider = await getModelProvider(namespace, providerName);
	if (provider.spec.type !== 'ollama') return null;
	const endpoint = provider.spec.endpoint;
	if (!endpoint) return null;
	return trimTrailingSlashes(endpoint);
}

/**
 * Build the JSON body for an Ollama action. Exported for test visibility.
 */
export function buildOllamaActionBody(model: string, action: OllamaAction): Record<string, unknown> {
	if (action === 'unload') {
		return { model, prompt: '', stream: false, keep_alive: 0 };
	}
	if (action === 'delete') {
		return { model };
	}
	return {
		model,
		prompt: 'warm',
		stream: false,
		options: { num_predict: 1 }
	};
}

/**
 * Map an action to the Ollama HTTP method + path. Exported for tests.
 */
export function ollamaRequestSpec(action: OllamaAction): { method: 'POST' | 'DELETE'; path: string } {
	if (action === 'delete') return { method: 'DELETE', path: '/api/delete' };
	return { method: 'POST', path: '/api/generate' };
}

/**
 * Execute a load/unload/delete against the Ollama endpoint. Throws on
 * non-2xx HTTP response with the upstream body included.
 */
export async function executeOllamaAction(
	endpoint: string,
	model: string,
	action: OllamaAction,
	fetchFn: typeof fetch = fetch
): Promise<OllamaActionResult> {
	const controller = new AbortController();
	const timer = setTimeout(() => controller.abort(), ACTION_TIMEOUT_MS);
	const start = Date.now();
	const { method, path } = ollamaRequestSpec(action);
	try {
		const res = await fetchFn(`${endpoint}${path}`, {
			method,
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify(buildOllamaActionBody(model, action)),
			signal: controller.signal
		});
		const text = await res.text();
		if (!res.ok) {
			throw new Error(`ollama ${action} ${model} → HTTP ${res.status}: ${text.slice(0, 200)}`);
		}
		const durationMs = Date.now() - start;
		const parsed = safeParseJson(text);
		const loadDurationMs = extractLoadDurationMs(parsed);
		return { provider: endpoint, model, action, durationMs, loadDurationMs };
	} finally {
		clearTimeout(timer);
	}
}

function safeParseJson(text: string): Record<string, unknown> | null {
	try {
		return JSON.parse(text) as Record<string, unknown>;
	} catch {
		return null;
	}
}

function extractLoadDurationMs(parsed: Record<string, unknown> | null): number | undefined {
	if (!parsed) return undefined;
	const raw = parsed.load_duration;
	if (typeof raw !== 'number') return undefined;
	return Math.round(raw / 1_000_000);
}
