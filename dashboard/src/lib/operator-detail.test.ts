import { describe, expect, it } from 'vitest';
import { formatUptime, kubemootCrds, leaderPod, operatorDeployment, operatorFacts, type OperatorDetail } from './operator-detail';

describe('operatorDeployment', () => {
	it('reads the version label, image, replicas, age and conditions', () => {
		const dep = {
			metadata: { name: 'kubemoot-operator', namespace: 'kubemoot', labels: { 'app.kubernetes.io/version': '0.356.0' }, creationTimestamp: new Date('2026-05-18T12:00:00Z') },
			spec: { replicas: 2, template: { spec: { containers: [{ image: 'ghcr.io/kubemoot/operator:0.356.0' }] } } },
			status: { readyReplicas: 1, conditions: [{ type: 'Available', status: 'True', reason: 'MinimumReplicasAvailable', lastTransitionTime: new Date('2026-11-22T00:40:45Z') }] }
		};
		expect(operatorDeployment(dep)).toMatchObject({
			name: 'kubemoot-operator', namespace: 'kubemoot', version: '0.356.0', image: 'ghcr.io/kubemoot/operator:0.356.0', desired: 2, ready: 1,
			createdAt: '2026-05-18T12:00:00.000Z',
			conditions: [{ type: 'Available', status: 'True', reason: 'MinimumReplicasAvailable', lastTransitionTime: '2026-11-22T00:40:45.000Z' }]
		});
		expect(operatorDeployment(dep).resource).toMatchObject({ metadata: { name: 'kubemoot-operator', creationTimestamp: '2026-05-18T12:00:00Z' }, status: { conditions: [{ lastTransitionTime: '2026-11-22T00:40:45Z' }] } });
	});

	it('writes a timestamp with a fraction of a second as it is, and leaves other values alone', () => {
		const dep = { metadata: { name: 'o', creationTimestamp: new Date('2026-05-18T12:00:00.250Z'), annotations: { n: 'x' } } };
		expect(operatorDeployment(dep).resource).toEqual({ metadata: { name: 'o', creationTimestamp: '2026-05-18T12:00:00.250Z', annotations: { n: 'x' } } });
	});

	it('says unknown, not blank, when the Deployment names no version or image', () => {
		expect(operatorDeployment({})).toMatchObject({ version: 'unknown', image: 'unknown', desired: 1, ready: 0, conditions: [] });
	});
});

describe('kubemootCrds', () => {
	const crd = (name: string, group: string, versions = [{ name: 'v1alpha1', served: true, storage: true }]) => ({ metadata: { name }, spec: { group, scope: 'Namespaced', names: { kind: name }, versions } });

	it('keeps only the kubemoot.ai group, sorted by name, each with its versions', () => {
		const out = kubemootCrds([crd('crews.kubemoot.ai', 'kubemoot.ai'), crd('certificates.cert-manager.io', 'cert-manager.io'), crd('agents.kubemoot.ai', 'kubemoot.ai', [{ name: 'v1alpha1', served: true, storage: false }, { name: 'v1beta1', served: false, storage: true }])]);
		expect(out.map((c) => c.name)).toEqual(['agents.kubemoot.ai', 'crews.kubemoot.ai']);
		expect(out[0].versions).toEqual([{ name: 'v1alpha1', served: true, storage: false }, { name: 'v1beta1', served: false, storage: true }]);
	});

	it('is empty, not an error, when the cluster has none', () => {
		expect(kubemootCrds([])).toEqual([]);
		expect(kubemootCrds([{}])).toEqual([]);
	});
});

describe('leaderPod', () => {
	it('names the operator pod that holds the lease, from "<pod>_<id>"', () => {
		const leases = [{ spec: { holderIdentity: 'other-controller-abc_1' } }, { spec: { holderIdentity: 'kubemoot-operator-67c464ccfb-zhp6r_00d07cee-aa33' } }];
		expect(leaderPod(leases, 'kubemoot-operator')).toBe('kubemoot-operator-67c464ccfb-zhp6r');
	});

	it('is empty when no lease is held by an operator pod', () => {
		expect(leaderPod([], 'kubemoot-operator')).toBe('');
		expect(leaderPod([{ spec: {} }, { spec: { holderIdentity: 'kubemoot-operatorx_1' } }], 'kubemoot-operator')).toBe('');
	});
});

describe('formatUptime', () => {
	it.each([
		[30_000, '30s'], [59_999, '59s'], [60_000, '1m'], [42 * 60_000, '42m'], [3_600_000, '1h 0m'], [5 * 3_600_000 + 12 * 60_000, '5h 12m'],
		[86_400_000, '1d 0h'], [188 * 86_400_000 + 4 * 3_600_000, '188d 4h']
	])('%d ms is %s', (ms, text) => expect(formatUptime(ms)).toBe(text));

	it('is empty for a time that is not a duration', () => {
		expect(formatUptime(-1)).toBe('');
		expect(formatUptime(Number.NaN)).toBe('');
	});
});

describe('operatorFacts', () => {
	const ok: OperatorDetail = {
		deployment: { available: true, data: { name: 'o', namespace: 'k', version: '1.2.3', image: 'i', desired: 1, ready: 1, conditions: [], resource: {} } },
		startedAt: { available: true, data: new Date(1_000_000).toISOString() },
		leader: { available: true, data: '' },
		crds: { available: true, data: [] }
	};

	it('says "0 Kubemoot CRDs" only for a read that found none', () => {
		expect(operatorFacts(ok, 1_000_000 + 90_000).map((f) => f.text)).toEqual(['Version 1.2.3', 'Up 1m', '0 Kubemoot CRDs']);
	});

	it('keeps any other reason whole', () => {
		const down = { available: false as const, reason: 'the operator pods could not be read' };
		expect(operatorFacts({ ...ok, startedAt: down }, 0)[1]).toEqual({ text: 'Uptime: the operator pods could not be read', read: false });
	});

	it('gives the reason, flagged as not read, for each part the account may not read', () => {
		const no = { available: false as const, reason: 'your account may not read x', denied: true };
		const facts = operatorFacts({ ...ok, deployment: no, startedAt: no, crds: no }, 0);
		expect(facts).toEqual([
			{ text: 'Version: your account may not read them', read: false },
			{ text: 'Uptime: your account may not read them', read: false },
			{ text: 'Kubemoot CRDs: your account may not read them', read: false }
		]);
	});
});
