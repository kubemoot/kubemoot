import { beforeEach, describe, expect, it, vi } from 'vitest';

const readAgentReplicas = vi.fn();
const guardNamespace = vi.fn();
vi.mock('#lib/server/k8s/index.js', async () => {
	const errors = await vi.importActual<typeof import('#lib/server/k8s/errors.js')>('#lib/server/k8s/errors.js');
	return { kubeFailure: errors.kubeFailure, kubeStatus: errors.kubeStatus, readAgentReplicas };
});
vi.mock('#lib/server/scope.js', () => ({ guardNamespace }));

const { GET } = await import('./+server.js');

const call = (name: string, query = '?namespace=crew-a') =>
	GET({ params: { name }, url: new URL(`https://dash.test/api/kubemoot/agents/${name}/replicas${query}`) } as Parameters<typeof GET>[0]);

beforeEach(() => {
	readAgentReplicas.mockReset();
	guardNamespace.mockReset().mockResolvedValue(null);
});

describe('GET /api/kubemoot/agents/{name}/replicas', () => {
	it('answers the Deployment counts, so an idle scale-to-zero agent is 0 of 0 and a running one 1 of 1', async () => {
		readAgentReplicas.mockResolvedValue({ desired: 1, ready: 1 });
		const res = await call('meta');
		expect(readAgentReplicas).toHaveBeenCalledWith('crew-a', 'meta');
		expect(await res.json()).toEqual({ desired: 1, ready: 1 });
	});

	it('answers 404 when the agent has no Deployment', async () => {
		readAgentReplicas.mockRejectedValue(Object.assign(new Error('not found'), { code: 404 }));
		expect((await call('meta')).status).toBe(404);
	});

	it('answers 403 plainly when the person may not read Deployments', async () => {
		readAgentReplicas.mockRejectedValue(Object.assign(new Error('forbidden'), { code: 403 }));
		const res = await call('meta');
		expect(res.status).toBe(403);
		expect((await res.json()).denied).toBe(true);
	});

	it('refuses a name that is not a Kubernetes name and reads nothing', async () => {
		expect((await call('../x')).status).toBe(400);
		expect(readAgentReplicas).not.toHaveBeenCalled();
	});

	it('reads nothing for a namespace outside the dashboard scope', async () => {
		guardNamespace.mockResolvedValue(new Response('{"error":"nf"}', { status: 404 }));
		expect((await call('meta', '?namespace=other')).status).toBe(404);
		expect(readAgentReplicas).not.toHaveBeenCalled();
	});
});
