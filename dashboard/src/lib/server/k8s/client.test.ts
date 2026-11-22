import { afterEach, describe, expect, it, vi } from 'vitest';

const loadFromCluster = vi.fn();
const loadFromDefault = vi.fn();

vi.mock('@kubernetes/client-node', () => ({
	KubeConfig: class {
		loadFromCluster = loadFromCluster;
		loadFromDefault = loadFromDefault;
	}
}));

async function freshClient() {
	vi.resetModules();
	return import('./client.js');
}

describe('which Kubernetes configuration the dashboard loads', () => {
	afterEach(() => {
		vi.unstubAllEnvs();
		loadFromCluster.mockReset();
		loadFromDefault.mockReset();
	});

	it('uses the in-cluster configuration when running in a pod', async () => {
		vi.stubEnv('KUBERNETES_SERVICE_HOST', 'kubernetes.default.svc');
		(await freshClient()).getKubeConfig();
		expect(loadFromCluster).toHaveBeenCalledOnce();
		expect(loadFromDefault).not.toHaveBeenCalled();
	});

	it('uses the default kubeconfig outside a cluster', async () => {
		vi.stubEnv('KUBERNETES_SERVICE_HOST', '');
		(await freshClient()).getKubeConfig();
		expect(loadFromDefault).toHaveBeenCalledOnce();
		expect(loadFromCluster).not.toHaveBeenCalled();
	});

	it('fails plainly when no configuration can be loaded', async () => {
		vi.stubEnv('KUBERNETES_SERVICE_HOST', '');
		loadFromDefault.mockImplementation(() => {
			throw new Error('no kubeconfig');
		});
		const client = await freshClient();
		expect(() => client.getKubeConfig()).toThrow('Unable to load Kubernetes configuration');
	});
});
