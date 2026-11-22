// @vitest-environment node
import { beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({ readDeployment: vi.fn(), listPods: vi.fn(), listLeases: vi.fn(), listCrds: vi.fn() }));
vi.mock('./client.js', () => ({
	getAppsApi: () => ({ readNamespacedDeployment: mocks.readDeployment }),
	getCoreApi: () => ({ listNamespacedPod: mocks.listPods }),
	getCoordinationApi: () => ({ listNamespacedLease: mocks.listLeases }),
	getApiextensionsApi: () => ({ listCustomResourceDefinition: mocks.listCrds })
}));

import { readOperatorDetail } from './operator';

const deployment = { metadata: { name: 'kubemoot-operator', namespace: 'kubemoot', labels: { 'app.kubernetes.io/version': '0.356.0' } }, spec: { selector: { matchLabels: { 'control-plane': 'controller-manager' } } }, status: { readyReplicas: 1 } };
const crd = { metadata: { name: 'crews.kubemoot.ai' }, spec: { group: 'kubemoot.ai', scope: 'Namespaced', names: { kind: 'Crew' }, versions: [{ name: 'v1alpha1', served: true, storage: true }] } };

beforeEach(() => {
	for (const m of Object.values(mocks)) m.mockReset();
	mocks.readDeployment.mockResolvedValue(deployment);
	mocks.listPods.mockResolvedValue({ items: [{ status: { phase: 'Running', startTime: '2026-11-22T00:40:00Z' } }] });
	mocks.listLeases.mockResolvedValue({ items: [{ spec: { holderIdentity: 'kubemoot-operator-abc-xyz_1' } }] });
	mocks.listCrds.mockResolvedValue({ items: [crd, { metadata: { name: 'x.other.io' }, spec: { group: 'other.io' } }] });
});

describe('readOperatorDetail', () => {
	it('reads the version, uptime, leader and the Kubemoot CRDs', async () => {
		const d = await readOperatorDetail();
		expect(d.deployment).toMatchObject({ available: true, data: { version: '0.356.0' } });
		expect(d.startedAt).toEqual({ available: true, data: '2026-11-22T00:40:00.000Z' });
		expect(d.leader).toEqual({ available: true, data: 'kubemoot-operator-abc-xyz' });
		expect(d.crds).toEqual({ available: true, data: [{ name: 'crews.kubemoot.ai', kind: 'Crew', scope: 'Namespaced', versions: [{ name: 'v1alpha1', served: true, storage: true }] }] });
		expect(mocks.listPods).toHaveBeenCalledWith({ namespace: 'kubemoot', labelSelector: 'control-plane=controller-manager' });
	});

	it('says plainly what a refusal blocks and keeps the other readings', async () => {
		mocks.listCrds.mockRejectedValue({ code: 403 });
		mocks.listLeases.mockRejectedValue({ code: 403 });
		const d = await readOperatorDetail();
		expect(d.crds).toEqual({ available: false, reason: 'your account may not read the custom resource definitions', denied: true });
		expect(d.leader).toEqual({ available: false, reason: 'your account may not read the operator leader lease', denied: true });
		expect(d.deployment.available).toBe(true);
		expect(d.startedAt.available).toBe(true);
	});

	it('marks a refusal as denied and any other failure as not, so a tile can say which', async () => {
		mocks.listCrds.mockRejectedValue({ code: 403 });
		mocks.listLeases.mockRejectedValue(new Error('boom'));
		const d = await readOperatorDetail();
		expect(d.crds).toMatchObject({ denied: true });
		expect(d.leader).not.toHaveProperty('denied');
	});

	it('asks the person to sign in again when the token is no longer accepted', async () => {
		mocks.listCrds.mockRejectedValue({ code: 401 });
		const d = await readOperatorDetail();
		expect(d.crds).toMatchObject({ available: false });
		expect((d.crds as { reason: string }).reason).toContain('Sign in again');
	});

	it('does not look for pods without the Deployment, and says why', async () => {
		mocks.readDeployment.mockRejectedValue({ code: 403 });
		const d = await readOperatorDetail();
		expect(d.deployment).toEqual({ available: false, reason: 'your account may not read the operator Deployment', denied: true });
		expect(d.startedAt.available).toBe(false);
		expect(mocks.listPods).not.toHaveBeenCalled();
	});

	it('says the Deployment is missing when the API says 404, and that a read failed for any other error', async () => {
		mocks.readDeployment.mockRejectedValue({ code: 404 });
		expect((await readOperatorDetail()).deployment).toEqual({ available: false, reason: 'the operator Deployment kubemoot-operator is not in the kubemoot namespace' });
		mocks.readDeployment.mockRejectedValue(new Error('boom'));
		mocks.listCrds.mockRejectedValue(new Error('boom'));
		const d = await readOperatorDetail();
		expect(d.deployment).toEqual({ available: false, reason: 'the operator Deployment could not be read' });
		expect(d.crds).toEqual({ available: false, reason: 'the custom resource definitions could not be read' });
	});

	it('reports no running pod as an empty start, not as a failure', async () => {
		mocks.listPods.mockResolvedValue({ items: [{ status: { phase: 'Pending' } }] });
		expect((await readOperatorDetail()).startedAt).toEqual({ available: true, data: '' });
	});
});
