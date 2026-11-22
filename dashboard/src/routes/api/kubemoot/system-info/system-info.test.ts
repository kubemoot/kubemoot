import { describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
	config: (): Promise<unknown> => Promise.resolve({})
}));

vi.mock('#lib/server/k8s/index.js', () => ({
	getKubemootConfig: () => mocks.config(),
	getKubeConfig: () => ({
		makeApiClient: () => ({
			readNamespacedDeployment: async () => ({}),
			listNamespacedPod: async () => ({ items: [] })
		})
	})
}));
vi.mock('#lib/server/mode.js', () => ({ infraNamespace: () => 'kubemoot-system' }));
vi.mock('@kubernetes/client-node', () => {
	class KubeConfig {
		loadFromCluster() {}
		loadFromDefault() {}
		makeApiClient() {
			return {
				readNamespacedDeployment: async () => ({}),
				listNamespacedPod: async () => ({ items: [] })
			};
		}
	}
	return { KubeConfig, AppsV1Api: class {}, CoreV1Api: class {} };
});

import { GET } from './+server';

const get = async () =>
	(await (GET as unknown as (e: unknown) => Promise<Response>)({})).json();

describe('GET /api/kubemoot/system-info release', () => {
	it('carries the release from the KubemootConfig status', async () => {
		mocks.config = () => Promise.resolve({ status: { release: '0.0.0-test' } });
		expect((await get()).release).toBe('0.0.0-test');
	});

	it('is empty while the status is not written yet', async () => {
		mocks.config = () => Promise.resolve({ spec: {} });
		expect((await get()).release).toBe('');
	});

	it('is empty for an old operator that reports no release', async () => {
		mocks.config = () => Promise.resolve({ status: { ready: true } });
		expect((await get()).release).toBe('');
	});

	it('is empty, and the rest still answers, when the KubemootConfig cannot be read', async () => {
		mocks.config = () => Promise.reject(new Error('not found'));
		const info = await get();
		expect(info.release).toBe('');
		expect(info.operatorVersion).toBe('unknown');
	});
});
