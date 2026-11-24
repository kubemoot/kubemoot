// @vitest-environment node
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { BearerToken } from './auth/bearer-token';
import { runWithIdentity } from './auth/identity';

// A plain function, not vi.fn: a failing vi.fn call is reported as an unhandled error.
const mocks = vi.hoisted(() => ({
	/** Cluster-wide Crew lists made with the dashboard's own account. */
	lists: 0,
	/** What each self-review asked, in order. */
	asked: [] as Record<string, string>[],
	/** Namespaces of the agent heartbeat keys on the bus. */
	heartbeatNamespaces: [] as string[],
	/** Namespaces of the Crews the service account lists, when it may. */
	crewNamespaces: [] as string[],
	allowed: new Set<string>(),
	listFails: false,
	busFails: false,
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
	getAuthorizationApi: () => ({
		createSelfSubjectAccessReview: async ({ body }: { body: { spec: { resourceAttributes: Record<string, string> } } }) => {
			const attrs = body.spec.resourceAttributes;
			mocks.asked.push(attrs);
			if (mocks.reviewFails) throw new Error('unauthorized');
			return { status: { allowed: mocks.allowed.has(attrs.namespace) } };
		}
	})
}));
vi.mock('./nats-client.js', () => ({
	getNatsConnection: async () => {
		if (mocks.busFails) throw new Error('nats down');
		return { jetstream: () => ({ views: { kv: async () => ({}) } }) };
	}
}));
vi.mock('./kv-snapshot.js', () => ({
	readKvSnapshot: async (_kv: unknown, pick: (e: { key: string; operation: string }) => unknown) => ({
		values: Object.fromEntries(mocks.heartbeatNamespaces.map((ns, i) => [`k${i}`, pick({ key: `${ns}.agent-${i}`, operation: 'PUT' })])),
		complete: true
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

/** Agents run in these namespaces, and the signed-in person may get Crews in `allowed`. */
function cluster(holding: string[], allowed: string[] = holding) {
	mocks.heartbeatNamespaces = holding;
	mocks.allowed = new Set(allowed);
}

beforeEach(() => {
	mocks.lists = 0;
	mocks.asked = [];
	mocks.listFails = false;
	mocks.busFails = false;
	mocks.reviewFails = false;
	mocks.crewNamespaces = [];
	mocks.namespaces = ['team-a', 'team-b'];
	delete process.env.KUBEMOOT_DASHBOARD_NAMESPACE_SELECTOR;
	delete process.env.KUBEMOOT_DASHBOARD_CREW_NAMESPACE_DISCOVERY;
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

	it('see nothing when the bus cannot be read (fails closed)', async () => {
		cluster(['team-a'], ['team-a']);
		mocks.busFails = true;
		await asCaller(async () => {
			expect(await callerNamespaces()).toEqual(new Set());
			expect(messageSubjectAllowed(SUBJECT('team-a'))).toBe(false);
		});
	});

	it('never list Crews with the service account unless the chart granted it', async () => {
		cluster(['team-a']);
		mocks.crewNamespaces = ['team-b'];
		await asCaller(async () => expect(await callerNamespaces()).toEqual(new Set(['team-a'])));
		expect(mocks.lists).toBe(0);
	});

	it('add the namespaces of Crews when the chart lets the service account list them', async () => {
		process.env.KUBEMOOT_DASHBOARD_CREW_NAMESPACE_DISCOVERY = 'true';
		cluster(['team-a'], ['team-a', 'team-b']);
		mocks.crewNamespaces = ['team-b'];
		await asCaller(async () => expect(await callerNamespaces()).toEqual(new Set(['team-a', 'team-b'])));
		expect(mocks.lists).toBe(1);
	});

	it('keep the bus namespaces when the optional list fails', async () => {
		process.env.KUBEMOOT_DASHBOARD_CREW_NAMESPACE_DISCOVERY = 'true';
		cluster(['team-a']);
		mocks.listFails = true;
		await asCaller(async () => expect(await callerNamespaces()).toEqual(new Set(['team-a'])));
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
		expect(reviewed()).toEqual(['team-a']);
	});

	it('are reviewed separately for each caller', async () => {
		cluster(['team-a'], ['team-a']);
		await asCaller(async () => expect(await callerNamespaces()).toEqual(new Set(['team-a'])));
		mocks.allowed = new Set();
		await asCaller(async () => expect(await callerNamespaces()).toEqual(new Set()));
	});
});
