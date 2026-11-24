// @vitest-environment node
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { BearerToken } from './auth/bearer-token';
import { runWithIdentity } from './auth/identity';

// A plain function, not vi.fn: a failing vi.fn call is reported as an unhandled error.
const mocks = vi.hoisted(() => ({
	/** Cluster-wide Crew lists made with the dashboard's own account (where to look). */
	lists: 0,
	/** Cluster-wide Crew lists made with the caller's token (the fallback). */
	callerLists: 0,
	/** What each self-review asked, in order. */
	asked: [] as Record<string, string>[],
	crewNamespaces: [] as string[],
	allowed: new Set<string>(),
	listFails: false,
	callerListFails: false,
	reviewFails: false,
	namespaces: ['team-a', 'team-b'] as string[]
}));
vi.mock('./k8s/client.js', () => ({
	getCoreApi: () => ({}),
	getServiceAccountCoreApi: () => ({
		listNamespace: async () => ({ items: mocks.namespaces.map((name) => ({ metadata: { name } })) })
	}),
	getServiceAccountCustomObjectsApi: () => ({
		listClusterCustomObject: async () => {
			mocks.lists++;
			if (mocks.listFails) throw new Error('forbidden');
			return { items: mocks.crewNamespaces.map((namespace) => ({ metadata: { namespace, name: 'c' } })) };
		}
	}),
	getCustomObjectsApi: () => ({
		listClusterCustomObject: async () => {
			mocks.callerLists++;
			if (mocks.callerListFails) throw new Error('forbidden');
			return { items: mocks.crewNamespaces.map((namespace) => ({ metadata: { namespace, name: 'c' } })) };
		}
	}),
	getAuthorizationApi: () => ({
		createSelfSubjectAccessReview: async ({ body }: { body: { spec: { resourceAttributes: Record<string, string> } } }) => {
			const attrs = body.spec.resourceAttributes;
			mocks.asked.push(attrs);
			if (mocks.reviewFails) throw new Error('unauthorized');
			return { status: { allowed: mocks.allowed.has(attrs.namespace) } };
		}
	})
}));

import {
	callerNamespaces,
	resetScopeCache,
	guardNatsNamespace,
	keyNamespaceAllowed,
	messageSubjectAllowed,
	subjectFilterAllowed
} from './scope';

let callers = 0;
const asCaller = <T>(fn: () => T) =>
	runWithIdentity({ kind: 'caller', username: 'u', token: new BearerToken(`t-${++callers}`), ended: new AbortController().signal }, fn);
const SUBJECT = (ns: string) => `kubemoot.discuss.${ns}.crew.chan.thread`;

const reviewed = () => mocks.asked.map((a) => a.namespace);

/** The cluster has Crews in these namespaces, and the signed-in person may get them in `allowed`. */
function cluster(holding: string[], allowed: string[] = holding) {
	mocks.crewNamespaces = holding;
	mocks.allowed = new Set(allowed);
}

beforeEach(() => {
	mocks.lists = 0;
	mocks.callerLists = 0;
	mocks.asked = [];
	mocks.listFails = false;
	mocks.callerListFails = false;
	mocks.reviewFails = false;
	mocks.namespaces = ['team-a', 'team-b'];
	delete process.env.KUBEMOOT_DASHBOARD_NAMESPACE_SELECTOR;
	resetScopeCache();
});

describe('NATS reads of a signed-in caller', () => {
	it('see only the namespaces where the caller may get Crews, reviewed with their own token', async () => {
		cluster(['team-a', 'team-b'], ['team-a']);
		await asCaller(async () => {
			expect(await callerNamespaces()).toEqual(new Set(['team-a']));
			expect(messageSubjectAllowed(SUBJECT('team-a'))).toBe(true);
			expect(messageSubjectAllowed(SUBJECT('team-b'))).toBe(false);
			expect(keyNamespaceAllowed('team-a.agent')).toBe(true);
			expect(keyNamespaceAllowed('team-b.agent')).toBe(false);
			expect(await subjectFilterAllowed(SUBJECT('team-b'))).toBe(false);
			expect(await subjectFilterAllowed(SUBJECT('team-a'))).toBe(true);
			expect(await subjectFilterAllowed('kubemoot.discuss.>')).toBe(true);
			expect((await guardNatsNamespace('team-b'))?.status).toBe(404);
			expect(await guardNatsNamespace('team-a')).toBeNull();
		});
		expect(reviewed().sort((a, b) => a.localeCompare(b))).toEqual(['team-a', 'team-b']);
		expect(mocks.asked.every((a) => a.verb === 'get' && a.group === 'kubemoot.ai' && a.resource === 'crews')).toBe(true);
	});

	it('let a person with rights in one namespace only see that namespace, with no cluster-wide right', async () => {
		cluster(['pilot', 'other', 'third'], ['pilot']);
		await asCaller(async () => {
			expect(await callerNamespaces()).toEqual(new Set(['pilot']));
			expect(messageSubjectAllowed(SUBJECT('pilot'))).toBe(true);
			expect(messageSubjectAllowed(SUBJECT('other'))).toBe(false);
			expect(messageSubjectAllowed(SUBJECT('third'))).toBe(false);
		});
	});

	it('see every namespace for a person who may get Crews in all of them', async () => {
		cluster(['team-a', 'team-b']);
		await asCaller(async () => expect(await callerNamespaces()).toEqual(new Set(['team-a', 'team-b'])));
	});

	it('see nothing when no review is a yes', async () => {
		cluster(['team-a', 'team-b'], []);
		await asCaller(async () => {
			expect(await callerNamespaces()).toEqual(new Set());
			expect(messageSubjectAllowed(SUBJECT('team-a'))).toBe(false);
			expect((await guardNatsNamespace('team-a'))?.status).toBe(404);
		});
	});

	it('see nothing when the review cannot be made (fails closed)', async () => {
		cluster(['team-a', 'team-b']);
		mocks.reviewFails = true;
		await asCaller(async () => {
			expect(await callerNamespaces()).toEqual(new Set());
			expect(messageSubjectAllowed(SUBJECT('team-a'))).toBe(false);
		});
	});

	it('see nothing when neither the service account nor the caller may list Crews (fails closed)', async () => {
		cluster(['team-a'], ['team-a']);
		mocks.listFails = true;
		mocks.callerListFails = true;
		await asCaller(async () => {
			expect(await callerNamespaces()).toEqual(new Set());
			expect(messageSubjectAllowed(SUBJECT('team-a'))).toBe(false);
		});
	});

	it('fall back to the caller\'s own list when the service account may not list Crews, and still review each namespace', async () => {
		cluster(['team-a', 'team-b'], ['team-a']);
		mocks.listFails = true;
		await asCaller(async () => expect(await callerNamespaces()).toEqual(new Set(['team-a'])));
		expect(mocks.callerLists).toBe(1);
		expect(reviewed().sort((a, b) => a.localeCompare(b))).toEqual(['team-a', 'team-b']);
	});

	it('review only the namespaces the instance serves', async () => {
		process.env.KUBEMOOT_DASHBOARD_NAMESPACE_SELECTOR = 'kubemoot.ai/workshop-team=true';
		mocks.namespaces = ['team-a'];
		cluster(['team-a', 'team-b'], ['team-a', 'team-b']);
		await asCaller(async () => expect(await callerNamespaces()).toEqual(new Set(['team-a'])));
		expect(reviewed()).toEqual(['team-a']);
	});

	it('are not narrowed for an anonymous viewer or outside a request', async () => {
		expect(await callerNamespaces()).toBeNull();
		expect(messageSubjectAllowed(SUBJECT('anything'))).toBe(true);
		expect(await guardNatsNamespace('anything')).toBeNull();
		expect(mocks.lists).toBe(0);
		expect(reviewed()).toEqual([]);
	});

	it('are reviewed once per caller within the interval', async () => {
		cluster(['team-a']);
		await asCaller(async () => {
			await callerNamespaces();
			await callerNamespaces();
		});
		expect(mocks.lists).toBe(1);
		expect(reviewed()).toEqual(['team-a']);
	});

	it('are reviewed separately for each caller', async () => {
		cluster(['team-a'], ['team-a']);
		await asCaller(async () => expect(await callerNamespaces()).toEqual(new Set(['team-a'])));
		mocks.allowed = new Set();
		await asCaller(async () => expect(await callerNamespaces()).toEqual(new Set()));
	});
});
