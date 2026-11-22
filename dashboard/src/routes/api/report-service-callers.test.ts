// Every dashboard fetch to the operator report service sends the service account token,
// and a refusal from the operator reaches the viewer with its reason, not as silence.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({ listNamespace: vi.fn(), fetch: vi.fn(), token: vi.fn() }));

vi.mock('#lib/server/k8s/client.js', () => ({ getCoreApi: () => ({ listNamespace: mocks.listNamespace }), getServiceAccountCoreApi: () => ({ listNamespace: mocks.listNamespace }) }));
vi.mock('#lib/server/service-account-token.js', () => ({ serviceAccountToken: mocks.token }));
vi.mock('#lib/server/operator-report.js', async (original) => ({
	...(await original<typeof import('#lib/server/operator-report.js')>()),
	operatorReportBase: () => 'http://operator.test:8082'
}));

import { resetScopeCache } from '#lib/server/scope.js';
import { GET as componentStatuses } from './kubemoot/componentstatuses/+server';
import { GET as suiteReport } from './kubemoot/crewfitnesssuites/[namespace]/[name]/artifact/+server';

type Call = (event: unknown) => Response | Promise<Response>;
const AUTH = { headers: { Authorization: 'Bearer sa-token' } };

const callers: Record<string, { call: () => Promise<Response>; url: string }> = {
	'component status': { call: async () => (componentStatuses as unknown as Call)({}), url: 'http://operator.test:8082/componentstatuses' },
	'fitness report': {
		call: async () => (suiteReport as unknown as Call)({ params: { namespace: 'pilot', name: 'nightly' } }),
		url: 'http://operator.test:8082/report/pilot/nightly'
	}
};

beforeEach(() => {
	resetScopeCache();
	mocks.listNamespace.mockReset();
	mocks.fetch.mockReset();
	mocks.token.mockReset();
	mocks.token.mockReturnValue('sa-token');
	vi.stubGlobal('fetch', mocks.fetch);
});

afterEach(() => {
	vi.unstubAllEnvs();
	vi.unstubAllGlobals();
});

describe.each(Object.entries(callers))('the %s caller', (_name, { call, url }) => {
	it('sends the service account token', async () => {
		mocks.fetch.mockResolvedValue(new Response('[]'));
		await call();
		expect(mocks.fetch).toHaveBeenCalledWith(url, AUTH);
	});

	it('sends no Authorization header when it has no token', async () => {
		mocks.token.mockReturnValue('');
		mocks.fetch.mockResolvedValue(new Response('[]'));
		await call();
		expect(mocks.fetch).toHaveBeenCalledWith(url, { headers: {} });
	});

	it('passes the 401 reason on instead of failing silently', async () => {
		mocks.fetch.mockResolvedValue(new Response('a valid bearer token is required', { status: 401 }));
		const err = await call().catch((e) => e);
		expect(JSON.stringify(err.body ?? err)).toContain('a valid bearer token is required');
	});
});
