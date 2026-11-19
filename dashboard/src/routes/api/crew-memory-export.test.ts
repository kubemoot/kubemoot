// The crew memory export route relays the operator's OMPI export, after the namespace scope.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({ listNamespace: vi.fn(), fetch: vi.fn(), token: vi.fn() }));

vi.mock('#lib/server/k8s/client.js', () => ({ getCoreApi: () => ({ listNamespace: mocks.listNamespace }) }));
vi.mock('#lib/server/service-account-token.js', () => ({ serviceAccountToken: mocks.token }));
vi.mock('#lib/server/operator-report.js', async (original) => ({
	...(await original<typeof import('#lib/server/operator-report.js')>()),
	operatorReportBase: () => 'http://operator.test:8082'
}));

import { resetScopeCache } from '#lib/server/scope.js';
import { GET } from './kubemoot/crew-memory/export/+server';

type Call = (event: unknown) => Response | Promise<Response>;
const AUTH = { headers: { Authorization: 'Bearer sa-token' } };
const get = (query: string) => (GET as unknown as Call)({ url: new URL(`https://dashboard/api/kubemoot/crew-memory/export?${query}`) });

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

describe('crew memory export route', () => {
	it('relays the operator zip with its download name', async () => {
		mocks.fetch.mockResolvedValue(
			new Response('PK-zip', { headers: { 'Content-Type': 'application/zip', 'Content-Disposition': 'attachment; filename=c-memory-ompi.zip' } })
		);
		const res = await get('namespace=pilot&crew=c');
		expect(res.status).toBe(200);
		expect(res.headers.get('Content-Type')).toBe('application/zip');
		expect(res.headers.get('Content-Disposition')).toContain('c-memory-ompi.zip');
		expect(res.headers.get('Cache-Control')).toBe('no-store');
		expect(await res.text()).toBe('PK-zip');
		expect(mocks.fetch).toHaveBeenCalledWith('http://operator.test:8082/memory-export/pilot/c?', AUTH);
	});

	it('sends the dashboard service account token to the operator as a bearer', async () => {
		mocks.fetch.mockImplementation(async () => new Response('PK'));
		await get('namespace=pilot&crew=c');
		expect(mocks.fetch.mock.calls[0][1]).toEqual({ headers: { Authorization: 'Bearer sa-token' } });
	});

	it('sends no Authorization header when the pod has no token, leaving the operator to refuse', async () => {
		mocks.token.mockReturnValue('');
		mocks.fetch.mockImplementation(async () => new Response('PK'));
		await get('namespace=pilot&crew=c');
		expect(mocks.fetch.mock.calls[0][1]).toEqual({ headers: {} });
	});

	it('forwards only the report flag', async () => {
		mocks.fetch.mockImplementation(async () => new Response('{}'));
		await get('namespace=pilot&crew=c&report=true&evil=1');
		expect(mocks.fetch).toHaveBeenCalledWith('http://operator.test:8082/memory-export/pilot/c?report=true', AUTH);
	});

	it.each(['allowUnscreened=true', 'allowUnscreened=false', 'allowUnscreened='])(
		'refuses %s and never asks the operator: the dashboard offers only the screened export',
		async (flag) => {
			const res = await get(`namespace=pilot&crew=c&${flag}`);
			expect(res.status).toBe(400);
			expect((await res.json()).error).toContain('kmctl');
			expect(mocks.fetch).not.toHaveBeenCalled();
		}
	);

	it('passes on the operator refusal of a caller it does not accept', async () => {
		mocks.fetch.mockResolvedValue(new Response('a valid bearer token is required', { status: 401 }));
		const res = await get('namespace=pilot&crew=c');
		expect(res.status).toBe(502);
		expect((await res.json()).error).toContain('bearer token');
	});

	it.each(['', 'namespace=pilot', 'crew=c', 'namespace=Bad_NS&crew=c', 'namespace=pilot&crew=a.b', 'namespace=pilot&crew=../x'])(
		'rejects an invalid scope (%s) without asking the operator',
		async (query) => {
			const res = await get(query);
			expect(res.status).toBe(400);
			expect(mocks.fetch).not.toHaveBeenCalled();
		}
	);

	it('answers 502 with the operator message when the export fails', async () => {
		mocks.fetch.mockResolvedValue(new Response('crew memory is not reachable: down', { status: 502 }));
		const res = await get('namespace=pilot&crew=c');
		expect(res.status).toBe(502);
		expect((await res.json()).error).toContain('not reachable');
	});

	it('answers 502 when the operator cannot be reached', async () => {
		mocks.fetch.mockRejectedValue(new Error('ECONNREFUSED'));
		const res = await get('namespace=pilot&crew=c');
		expect(res.status).toBe(502);
		expect((await res.json()).error).toContain('ECONNREFUSED');
	});

	describe('with a namespace selector', () => {
		beforeEach(() => {
			vi.stubEnv('KUBEMOOT_DASHBOARD_NAMESPACE_SELECTOR', 'kubemoot.ai/workshop-team=true');
			mocks.listNamespace.mockResolvedValue({ items: [{ metadata: { name: 'team-a' } }] });
		});

		it('answers 404 for a crew outside the scope and never asks the operator', async () => {
			const res = await get('namespace=kubemoot&crew=c');
			expect(res.status).toBe(404);
			expect(mocks.fetch).not.toHaveBeenCalled();
		});

		it('serves a crew inside the scope', async () => {
			mocks.fetch.mockResolvedValue(new Response('PK', { headers: { 'Content-Type': 'application/zip' } }));
			expect((await get('namespace=team-a&crew=c')).status).toBe(200);
		});

		it('answers 503 when the scope cannot be resolved', async () => {
			mocks.listNamespace.mockRejectedValue(new Error('apiserver down'));
			expect((await get('namespace=team-a&crew=c')).status).toBe(503);
		});
	});
});
