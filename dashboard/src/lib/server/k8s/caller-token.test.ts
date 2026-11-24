// @vitest-environment node
import { mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import * as k8s from '@kubernetes/client-node';
import { beforeAll, describe, expect, it, vi } from 'vitest';
import { BearerToken } from '../auth/bearer-token';
import { runWithIdentity } from '../auth/identity';
import {
	getAuthorizationApi,
	getBatchApi,
	getCoreApi,
	getCustomObjectsApi,
	getKubeConfig,
	getServiceAccountCustomObjectsApi,
	serviceAccountKubeConfig
} from './client';
import { FORBIDDEN_MESSAGE, SIGNED_OUT_MESSAGE, kubeFailure } from './errors';

const SA_TOKEN = 'dashboard-service-account-token';
const CALLER_TOKEN = 'callers-own-token';

beforeAll(() => {
	const dir = mkdtempSync(join(tmpdir(), 'kubeconfig-'));
	const file = join(dir, 'config');
	writeFileSync(
		file,
		[
			'apiVersion: v1',
			'kind: Config',
			'current-context: sa',
			'clusters:',
			'- name: c',
			'  cluster: {server: "https://k8s.example.test:6443"}',
			'users:',
			'- name: sa',
			`  user: {token: ${SA_TOKEN}}`,
			'contexts:',
			'- name: sa',
			'  context: {cluster: c, user: sa}'
		].join('\n')
	);
	process.env.KUBECONFIG = file;
	// Outside a cluster the client library still "loads" an in-cluster config; make it fail as it would for a dev host.
	vi.spyOn(k8s.KubeConfig.prototype, 'loadFromCluster').mockImplementation(() => {
		throw new Error('not in a cluster');
	});
});

async function authorizationOf(kc: ReturnType<typeof getKubeConfig>): Promise<string> {
	const options: { headers?: Record<string, string> } = { headers: {} };
	await kc.applyToHTTPSOptions(options);
	return options.headers?.Authorization ?? '';
}

describe('Kubernetes calls of a signed-in request', () => {
	const asCaller = <T>(fn: () => T) =>
		runWithIdentity({ kind: 'caller', username: 'u', token: new BearerToken(CALLER_TOKEN), ended: new AbortController().signal }, fn);

	it('carry the caller token and never the dashboard service account token', async () => {
		const header = await asCaller(() => authorizationOf(getKubeConfig()));
		expect(header).toBe(`Bearer ${CALLER_TOKEN}`);
		expect(header).not.toContain(SA_TOKEN);
	});

	it('use the caller token for every API client', async () => {
		const made = vi.spyOn(k8s.KubeConfig.prototype, 'makeApiClient');
		await asCaller(async () => {
			getCoreApi();
			getCustomObjectsApi();
			getBatchApi();
			for (const result of made.mock.contexts) expect(await authorizationOf(result as never)).toBe(`Bearer ${CALLER_TOKEN}`);
		});
		expect(made).toHaveBeenCalledTimes(3);
		made.mockRestore();
	});

	it('review access with the caller token, and list with the service account even then', async () => {
		const made = vi.spyOn(k8s.KubeConfig.prototype, 'makeApiClient');
		await asCaller(async () => {
			getAuthorizationApi();
			getServiceAccountCustomObjectsApi();
			expect(await authorizationOf(made.mock.contexts[0] as never)).toBe(`Bearer ${CALLER_TOKEN}`);
			expect(await authorizationOf(made.mock.contexts[1] as never)).toBe(`Bearer ${SA_TOKEN}`);
		});
		expect(made).toHaveBeenCalledTimes(2);
		made.mockRestore();
	});

	it('keep the cluster of the dashboard configuration', () => {
		const server = asCaller(() => getKubeConfig().getCurrentCluster()?.server);
		expect(server).toBe(serviceAccountKubeConfig().getCurrentCluster()?.server);
	});

	it('use the service account only for an anonymous read-only viewer', async () => {
		const header = await runWithIdentity({ kind: 'anonymous' }, () => authorizationOf(getKubeConfig()));
		expect(header).toBe(`Bearer ${SA_TOKEN}`);
	});
});

describe('kubeFailure', () => {
	it('answers a refused call as 403 with a plain message, not a server error', async () => {
		const res = kubeFailure({ code: 403 }, 'Failed');
		expect(res.status).toBe(403);
		expect(await res.json()).toMatchObject({ denied: true, error: FORBIDDEN_MESSAGE });
	});

	it('asks for a new sign-in on a 401', async () => {
		const res = kubeFailure({ code: 401 }, 'Failed');
		expect(res.status).toBe(401);
		expect((await res.json()).error).toBe(SIGNED_OUT_MESSAGE);
	});

	it('keeps other failures as 500 with the error text', async () => {
		const res = kubeFailure(new Error('boom'), 'Failed');
		expect(res.status).toBe(500);
		expect((await res.json()).error).toBe('boom');
		expect((await kubeFailure('odd', 'Failed to list').json()).error).toBe('Failed to list');
	});

	it('reads a watch error carrying statusCode', () => {
		expect(kubeFailure({ statusCode: 403 }, 'x').status).toBe(403);
	});
});
