import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const { listNamespace } = vi.hoisted(() => ({ listNamespace: vi.fn() }));
vi.mock('./k8s/client.js', () => ({ getCoreApi: () => ({ listNamespace }) }));

import {
	SCOPE_MAX_STALE_MS,
	SCOPE_TTL_MS,
	guardKeyNamespace,
	guardNamespace,
	guardNamespaceOrAll,
	keyNamespaceAllowed,
	kvBucketAllowed,
	messageSubjectAllowed,
	namespaceAllowed,
	namespaceAllowedNow,
	objectAccepter,
	resetScopeCache,
	scopeItems,
	scopeKindFor,
	scopeList,
	scopedNamespaces,
	streamAllowed,
	subjectFilterAllowed,
	subjectNamespace,
	subjectTargetAllowed
} from './scope';

const SELECTOR = 'kubemoot.ai/workshop-team=true';

function namespaces(...names: string[]) {
	return { items: names.map((name) => ({ metadata: { name } })) };
}

function list(...namespacesOfItems: string[]) {
	return {
		apiVersion: 'kubemoot.ai/v1alpha1',
		kind: 'AgentList',
		metadata: {},
		items: namespacesOfItems.map((namespace, i) => ({ metadata: { name: `a${i}`, namespace } }))
	};
}

async function status(response: Response | null) {
	return response === null ? null : response.status;
}

beforeEach(() => {
	resetScopeCache();
	listNamespace.mockReset();
	listNamespace.mockResolvedValue(namespaces('team-a', 'team-b'));
});

afterEach(() => {
	vi.unstubAllEnvs();
	vi.useRealTimers();
});

describe('not scoped (selector empty)', () => {
	it('passes everything through and never reads the cluster', async () => {
		expect(await scopedNamespaces()).toBeNull();
		expect(await namespaceAllowed('anything')).toBe(true);
		expect(namespaceAllowedNow('anything')).toBe(true);
		expect(await guardNamespace('anything')).toBeNull();
		expect(await guardNamespaceOrAll('')).toBeNull();
		expect(await guardKeyNamespace({ namespace: 'x' })).toBeNull();
		const all = list('team-a', 'other');
		expect(await scopeList('', async () => all)).toBe(all);
		expect(await scopeItems([1, 2], () => 'x')).toEqual([1, 2]);
		expect(await subjectFilterAllowed('kubemoot.>')).toBe(true);
		expect(await subjectTargetAllowed('anything')).toBe(true);
		expect(messageSubjectAllowed('kubemoot.other.thing')).toBe(true);
		expect(keyNamespaceAllowed('x.y')).toBe(true);
		expect(streamAllowed('ANY')).toBe(true);
		expect(kvBucketAllowed('any_bucket')).toBe(true);
		expect(objectAccepter()({ metadata: { namespace: 'x' } })).toBe(true);
		expect(listNamespace).not.toHaveBeenCalled();
	});
});

describe('scoped', () => {
	beforeEach(() => {
		vi.stubEnv('KUBEMOOT_DASHBOARD_NAMESPACE_SELECTOR', SELECTOR);
	});

	it('resolves the selector against the live namespaces', async () => {
		expect(await scopedNamespaces()).toEqual(new Set(['team-a', 'team-b']));
		expect(listNamespace).toHaveBeenCalledWith({ labelSelector: SELECTOR });
	});

	it('caches for the interval, then refreshes (a namespace gained the label)', async () => {
		vi.useFakeTimers();
		await scopedNamespaces();
		await scopedNamespaces();
		expect(listNamespace).toHaveBeenCalledTimes(1);

		listNamespace.mockResolvedValue(namespaces('team-a', 'team-b', 'team-c'));
		vi.advanceTimersByTime(SCOPE_TTL_MS + 1);
		expect((await scopedNamespaces())?.has('team-c')).toBe(true);
		expect(listNamespace).toHaveBeenCalledTimes(2);
	});

	it('shares one lookup between concurrent callers', async () => {
		await Promise.all([scopedNamespaces(), scopedNamespaces(), namespaceAllowed('team-a')]);
		expect(listNamespace).toHaveBeenCalledTimes(1);
	});

	it('refreshes when the selector changes', async () => {
		await scopedNamespaces();
		vi.stubEnv('KUBEMOOT_DASHBOARD_NAMESPACE_SELECTOR', 'other=label');
		listNamespace.mockResolvedValue(namespaces('only-one'));
		expect(await scopedNamespaces()).toEqual(new Set(['only-one']));
	});

	it('keeps the last good set when a refresh fails', async () => {
		vi.useFakeTimers();
		await scopedNamespaces();
		vi.advanceTimersByTime(SCOPE_TTL_MS + 1);
		listNamespace.mockRejectedValue(new Error('apiserver down'));
		expect(await scopedNamespaces()).toEqual(new Set(['team-a', 'team-b']));
	});

	it('fails closed once a failing refresh has kept the old set too long', async () => {
		vi.useFakeTimers();
		await scopedNamespaces();
		listNamespace.mockRejectedValue(new Error('apiserver down'));
		vi.advanceTimersByTime(SCOPE_MAX_STALE_MS + 1);
		await expect(scopedNamespaces()).rejects.toThrow('apiserver down');
		expect(namespaceAllowedNow('team-a')).toBe(false);
	});

	it('fails closed when nothing was ever resolved', async () => {
		listNamespace.mockRejectedValue(new Error('forbidden'));
		await expect(scopedNamespaces()).rejects.toThrow('forbidden');
		expect(await status(await guardNamespace('team-a'))).toBe(503);
		expect(await status(await guardNamespaceOrAll(''))).toBe(503);
		expect(await subjectFilterAllowed('kubemoot.discuss.team-a.>')).toBe(false);
		expect(namespaceAllowedNow('team-a')).toBe(false);
	});

	it('a namespace with no match yields an empty scope, not all namespaces', async () => {
		listNamespace.mockResolvedValue(namespaces());
		expect(await namespaceAllowed('team-a')).toBe(false);
		expect((await scopeList('', async () => list('team-a'))).items).toEqual([]);
	});

	describe('guards', () => {
		it('allows an in-scope namespace and answers 404 for any other', async () => {
			expect(await guardNamespace('team-a')).toBeNull();
			expect(await status(await guardNamespace('kube-system'))).toBe(404);
			expect(await status(await guardNamespace('kubemoot'))).toBe(404);
			expect(await status(await guardNamespace(''))).toBe(404);
			expect(await status(await guardNamespace(null))).toBe(404);
		});

		it('infra kinds also allow the operator namespace', async () => {
			expect(await guardNamespace('kubemoot', 'infra')).toBeNull();
			expect(await guardNamespace('team-b', 'infra')).toBeNull();
			expect(await status(await guardNamespace('kube-system', 'infra'))).toBe(404);
		});

		it('the operator namespace follows KUBEMOOT_DASHBOARD_INFRA_NAMESPACE', async () => {
			vi.stubEnv('KUBEMOOT_DASHBOARD_INFRA_NAMESPACE', 'moot-system');
			expect(await guardNamespace('moot-system', 'infra')).toBeNull();
			expect(await status(await guardNamespace('kubemoot', 'infra'))).toBe(404);
		});

		it('the all-namespaces form only needs the scope resolved', async () => {
			expect(await guardNamespaceOrAll('')).toBeNull();
			expect(await guardNamespaceOrAll('team-a')).toBeNull();
			expect(await status(await guardNamespaceOrAll('kube-system'))).toBe(404);
		});

		it('guards a parsed key by its namespace, and rejects a key that did not parse', async () => {
			expect(await guardKeyNamespace({ namespace: 'team-a' })).toBeNull();
			expect(await status(await guardKeyNamespace({ namespace: 'team-z' }))).toBe(404);
			expect(await status(await guardKeyNamespace(null))).toBe(404);
		});
	});

	describe('scopeList', () => {
		it('filters a cluster-wide list item by item', async () => {
			const result = await scopeList('', async () => list('team-a', 'kubemoot', 'team-b', 'other'));
			expect(result.items.map((i) => i.metadata.namespace)).toEqual(['team-a', 'team-b']);
		});

		it('returns an empty list for a namespace outside the scope without listing it', async () => {
			const fetcher = vi.fn(async () => list('kubemoot'));
			expect((await scopeList('kubemoot', fetcher)).items).toEqual([]);
			expect(fetcher).not.toHaveBeenCalled();
		});

		it('lists an in-scope namespace', async () => {
			const fetcher = vi.fn(async () => list('team-a'));
			expect((await scopeList('team-a', fetcher)).items).toHaveLength(1);
			expect(fetcher).toHaveBeenCalledWith('team-a');
		});

		it('keeps operator-namespace items for infra kinds only', async () => {
			const rows = async () => list('kubemoot', 'team-a', 'other');
			expect((await scopeList('', rows, 'infra')).items.map((i) => i.metadata.namespace)).toEqual([
				'kubemoot',
				'team-a'
			]);
			expect((await scopeList('', rows)).items.map((i) => i.metadata.namespace)).toEqual(['team-a']);
		});

		it('drops items with no namespace', async () => {
			const result = await scopeList('', async () => ({ ...list(), items: [{ metadata: {} }] }));
			expect(result.items).toEqual([]);
		});
	});

	it('scopeItems filters by the namespace the caller reads', async () => {
		const items = [{ ns: 'team-a' }, { ns: 'nope' }];
		expect(await scopeItems(items, (i) => i.ns)).toEqual([{ ns: 'team-a' }]);
	});

	it('scopeKindFor marks the infrastructure kinds', () => {
		for (const p of ['modelproviders', 'models', 'embeddingmodels', 'mcpcatalogs', 'mcpqualitypolicies']) {
			expect(scopeKindFor(p)).toBe('infra');
		}
		for (const p of ['agents', 'crews', 'mcpservers', 'crewfitnesssuites', 'mcpserverreports']) {
			expect(scopeKindFor(p)).toBe('namespaced');
		}
	});

	it('objectAccepter drops watched objects outside the scope', async () => {
		await scopedNamespaces();
		const accept = objectAccepter();
		expect(accept({ metadata: { namespace: 'team-a' } })).toBe(true);
		expect(accept({ metadata: { namespace: 'other' } })).toBe(false);
		expect(accept({ metadata: {} })).toBe(false);
		expect(accept(null)).toBe(false);
		expect(objectAccepter('infra')({ metadata: { namespace: 'kubemoot' } })).toBe(true);
	});

	describe('NATS subjects', () => {
		it('reads the namespace token of discussion and chat subjects only', () => {
			expect(subjectNamespace('kubemoot.discuss.team-a.crew.general.t1')).toBe('team-a');
			expect(subjectNamespace('kubemoot.chat.team-a.admin')).toBe('team-a');
			expect(subjectNamespace('kubemoot.discuss.>')).toBe('>');
			expect(subjectNamespace('kubemoot.discuss')).toBeNull();
			expect(subjectNamespace('kubemoot.quality.team-a.x')).toBeNull();
			expect(subjectNamespace('team-a.crew')).toBeNull();
		});

		it.each([
			['kubemoot.discuss.>', true],
			['kubemoot.chat.>', true],
			['kubemoot.discuss.*.*.*.t1', true],
			['kubemoot.discuss.team-a.>', true],
			['kubemoot.chat.team-b.admin', true],
			['kubemoot.discuss.kubemoot.>', false],
			['kubemoot.chat.other.admin', false],
			['kubemoot.>', false],
			['>', false],
			['kubemoot.quality.>', false],
			['kubemoot.operator.>', false],
			['$KV.kubemoot_crew_memory.>', false],
			['kubemoot.discuss', false]
		])('subject filter %s -> %s', async (subject, allowed) => {
			expect(await subjectFilterAllowed(subject)).toBe(allowed);
		});

		it.each([
			['kubemoot.chat.team-a.admin', true],
			['kubemoot.discuss.team-a.crew.general.t1', true],
			['kubemoot.discuss.*.*.*.t1', false],
			['kubemoot.discuss.>', false],
			['kubemoot.chat.other.admin', false],
			['kubemoot.operator.x', false]
		])('write target %s -> %s', async (subject, allowed) => {
			expect(await subjectTargetAllowed(subject)).toBe(allowed);
		});

		it('filters concrete messages by the namespace in their subject', async () => {
			await scopedNamespaces();
			expect(messageSubjectAllowed('kubemoot.discuss.team-a.crew.general.t1')).toBe(true);
			expect(messageSubjectAllowed('kubemoot.discuss.kubemoot.crew.general.t1')).toBe(false);
			expect(messageSubjectAllowed('kubemoot.chat.team-b.admin')).toBe(true);
			expect(messageSubjectAllowed('kubemoot.chat.other.admin')).toBe(false);
			expect(messageSubjectAllowed('kubemoot.quality.team-a.x')).toBe(false);
			expect(messageSubjectAllowed('not-a-subject')).toBe(false);
		});

		it('filters KV keys by their leading namespace', async () => {
			await scopedNamespaces();
			expect(keyNamespaceAllowed('team-a.agent')).toBe(true);
			expect(keyNamespaceAllowed('kubemoot.agent')).toBe(false);
			expect(keyNamespaceAllowed('.x')).toBe(false);
		});

		it('serves only the discussion stream and the agent-state bucket', () => {
			expect(streamAllowed('KUBEMOOT_DISCUSS')).toBe(true);
			expect(streamAllowed('KV_kubemoot_crew_memory')).toBe(false);
			expect(kvBucketAllowed('kubemoot_agent_state')).toBe(true);
			expect(kvBucketAllowed('kubemoot_crew_memory')).toBe(false);
			expect(kvBucketAllowed('made_up_bucket')).toBe(false);
		});
	});
});

describe('read-only without a selector', () => {
	it('serves only the allowlisted KV bucket, because opening an unknown bucket creates it', () => {
		vi.stubEnv('KUBEMOOT_DASHBOARD_READ_ONLY', 'true');
		expect(kvBucketAllowed('kubemoot_agent_state')).toBe(true);
		expect(kvBucketAllowed('made_up_bucket')).toBe(false);
		expect(streamAllowed('ANY')).toBe(true);
	});
});
