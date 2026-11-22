// @vitest-environment node
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { BearerToken } from './auth/bearer-token';
import { runWithIdentity } from './auth/identity';

// A plain function, not vi.fn: a failing vi.fn call is reported as an unhandled error.
const mocks = vi.hoisted(() => ({
	calls: 0,
	impl: async (): Promise<unknown> => ({ items: [] })
}));
vi.mock('./k8s/client.js', () => ({ getServiceAccountCoreApi: () => ({}), getCoreApi: () => ({}) }));
vi.mock('./k8s/kubemoot-crds.js', () => ({
	listCrews: () => {
		mocks.calls++;
		return mocks.impl();
	}
}));

import {
	callerNamespaces,
	guardNatsNamespace,
	keyNamespaceAllowed,
	messageSubjectAllowed,
	subjectFilterAllowed
} from './scope';

const crews = (...namespaces: string[]) => ({ items: namespaces.map((namespace) => ({ metadata: { namespace, name: 'c' } })) });
let callers = 0;
const asCaller = <T>(fn: () => T) =>
	runWithIdentity({ kind: 'caller', username: 'u', token: new BearerToken(`t-${++callers}`), ended: new AbortController().signal }, fn);
const SUBJECT = (ns: string) => `kubemoot.discuss.${ns}.crew.chan.thread`;

beforeEach(() => {
	mocks.calls = 0;
});

describe('NATS reads of a signed-in caller', () => {
	it('see only the namespaces whose Crews the caller may read, asked with their own token', async () => {
		mocks.impl = async () => crews('team-a');
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
	});

	it('see nothing when the caller may not list Crews (fails closed)', async () => {
		mocks.impl = async () => {
			throw new Error('forbidden');
		};
		await asCaller(async () => {
			expect(await callerNamespaces()).toEqual(new Set());
			expect(messageSubjectAllowed(SUBJECT('team-a'))).toBe(false);
			expect((await guardNatsNamespace('team-a'))?.status).toBe(404);
		});
	});

	it('are not narrowed for an anonymous viewer or outside a request', async () => {
		expect(await callerNamespaces()).toBeNull();
		expect(messageSubjectAllowed(SUBJECT('anything'))).toBe(true);
		expect(await guardNatsNamespace('anything')).toBeNull();
		expect(mocks.calls).toBe(0);
	});

	it('are asked once per caller within the interval', async () => {
		mocks.impl = async () => crews('team-a');
		await asCaller(async () => {
			await callerNamespaces();
			await callerNamespaces();
		});
		expect(mocks.calls).toBe(1);
	});
});
