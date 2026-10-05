import { getCoreApi } from './client.js';

export interface ConfigMapSummary {
	name: string;
	namespace: string;
	data?: Record<string, string>;
}

interface MaybeK8sError {
	code?: number;
	statusCode?: number;
	response?: { statusCode?: number; headers?: Record<string, string | string[] | undefined> };
	message?: string;
}

function isThrottled(err: unknown): boolean {
	const e = err as MaybeK8sError;
	const code = e?.code ?? e?.statusCode ?? e?.response?.statusCode;
	if (code === 429) return true;
	const msg = e?.message ?? '';
	return /\b429\b|TooManyRequests|storage is .re.initializing/i.test(msg);
}

function retryAfterMs(err: unknown, attempt: number): number {
	const headers = (err as MaybeK8sError)?.response?.headers;
	const raw = headers?.['retry-after'] ?? headers?.['Retry-After'];
	const headerVal = Array.isArray(raw) ? raw[0] : raw;
	const seconds = headerVal ? Number(headerVal) : Number.NaN;
	if (Number.isFinite(seconds) && seconds > 0) return Math.min(5000, seconds * 1000);
	return 500 * Math.pow(2, attempt);
}

/**
 * Read a ConfigMap by namespace + name. Honors Kubernetes 429 retry-after
 * (storage-reinitializing path) up to 3 attempts. Throws on permanent
 * failures (NotFound, RBAC denial, etc.) so callers can degrade.
 */
export async function readConfigMap(namespace: string, name: string): Promise<ConfigMapSummary> {
	const api = getCoreApi();
	let lastErr: unknown;
	for (let attempt = 0; attempt < 3; attempt++) {
		try {
			const result = await api.readNamespacedConfigMap({ namespace, name });
			return { name, namespace, data: result.data ?? {} };
		} catch (e) {
			if (!isThrottled(e) || attempt === 2) throw e;
			lastErr = e;
			await new Promise((r) => setTimeout(r, retryAfterMs(e, attempt)));
		}
	}
	throw lastErr;
}
