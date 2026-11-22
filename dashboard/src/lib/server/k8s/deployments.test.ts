// @vitest-environment node
import { beforeEach, describe, expect, it, vi } from 'vitest';

const readNamespacedDeployment = vi.fn();
vi.mock('./client.js', () => ({ getAppsApi: () => ({ readNamespacedDeployment }) }));

const { readAgentReplicas } = await import('./deployments.js');

beforeEach(() => {
	readNamespacedDeployment.mockReset();
});

describe('readAgentReplicas', () => {
	it('reads the Deployment named for the agent and reports its target and ready counts', async () => {
		readNamespacedDeployment.mockResolvedValue({ spec: { replicas: 2 }, status: { readyReplicas: 1 } });
		expect(await readAgentReplicas('crew-a', 'meta')).toEqual({ desired: 2, ready: 1 });
		expect(readNamespacedDeployment).toHaveBeenCalledWith({ namespace: 'crew-a', name: 'meta' });
	});

	it('reports an idle scale-to-zero agent as 0 of 0, with no ready count set at all', async () => {
		readNamespacedDeployment.mockResolvedValue({ spec: { replicas: 0 }, status: {} });
		expect(await readAgentReplicas('crew-a', 'meta')).toEqual({ desired: 0, ready: 0 });
	});

	it('lets the API server refusal through for the route to answer', async () => {
		readNamespacedDeployment.mockRejectedValue(Object.assign(new Error('forbidden'), { code: 403 }));
		await expect(readAgentReplicas('crew-a', 'meta')).rejects.toMatchObject({ code: 403 });
	});
});
