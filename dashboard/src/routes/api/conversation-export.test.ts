// The conversation export route relays the operator's zip, after the namespace scope.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({ listNamespace: vi.fn(), fetch: vi.fn(), token: vi.fn() }));

vi.mock('#lib/server/k8s/client.js', () => ({ getCoreApi: () => ({ listNamespace: mocks.listNamespace }) }));
vi.mock('#lib/server/service-account-token.js', () => ({ serviceAccountToken: mocks.token }));
vi.mock('#lib/server/operator-report.js', async (original) => ({
	...(await original<typeof import('#lib/server/operator-report.js')>()),
	operatorReportBase: () => 'http://operator.test:8082'
}));

import { resetScopeCache } from '#lib/server/scope.js';
import { GET } from './kubemoot/conversation-export/+server';

type Call = (event: unknown) => Response | Promise<Response>;
const AUTH = { headers: { Authorization: 'Bearer sa-token' } };
const get = (query: string) =>
	(GET as unknown as Call)({ url: new URL(`https://dashboard/api/kubemoot/conversation-export?${query}`) });
const zip = (name: string) =>
	new Response('PK-zip', { headers: { 'Content-Type': 'application/zip', 'Content-Disposition': `attachment; filename=${name}` } });

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

describe('conversation export route', () => {
	it('relays one conversation zip with the operator file name', async () => {
		mocks.fetch.mockResolvedValue(zip('c-conversation-abc.zip'));
		const res = await get('namespace=pilot&crew=c&id=abc');
		expect(res.status).toBe(200);
		expect(res.headers.get('Content-Type')).toBe('application/zip');
		expect(res.headers.get('Content-Disposition')).toContain('c-conversation-abc.zip');
		expect(res.headers.get('Cache-Control')).toBe('no-store');
		expect(await res.text()).toBe('PK-zip');
		expect(mocks.fetch).toHaveBeenCalledWith('http://operator.test:8082/conversation-export/pilot/c?id=abc', AUTH);
	});

	it('repeats id for several conversations and relays the bundle name', async () => {
		mocks.fetch.mockResolvedValue(zip('c-conversations.zip'));
		const res = await get('namespace=pilot&crew=c&id=a&id=b&id=c');
		expect(res.headers.get('Content-Disposition')).toContain('c-conversations.zip');
		expect(mocks.fetch.mock.calls[0][0]).toBe('http://operator.test:8082/conversation-export/pilot/c?id=a&id=b&id=c');
	});

	it('forwards the report flag only when exactly true, and a clean time zone, and nothing else', async () => {
		mocks.fetch.mockImplementation(async () => new Response('{}'));
		await get('namespace=pilot&crew=c&id=a&report=true&timeZone=America/New_York&evil=1');
		expect(mocks.fetch.mock.calls[0][0]).toBe(
			'http://operator.test:8082/conversation-export/pilot/c?id=a&timeZone=America%2FNew_York&report=true'
		);
		mocks.fetch.mockClear();
		await get('namespace=pilot&crew=c&id=a&report=yes&timeZone=a%20b');
		expect(mocks.fetch.mock.calls[0][0]).toBe('http://operator.test:8082/conversation-export/pilot/c?id=a');
	});

	it('sends the dashboard service account token as a bearer, and none when the pod has none', async () => {
		mocks.fetch.mockImplementation(async () => new Response('PK'));
		await get('namespace=pilot&crew=c&id=a');
		expect(mocks.fetch.mock.calls[0][1]).toEqual(AUTH);
		mocks.token.mockReturnValue('');
		await get('namespace=pilot&crew=c&id=a');
		expect(mocks.fetch.mock.calls[1][1]).toEqual({ headers: {} });
	});

	it.each(['allowUnscreened=true', 'allowUnscreened='])('refuses %s without asking the operator', async (flag) => {
		const res = await get(`namespace=pilot&crew=c&id=a&${flag}`);
		expect(res.status).toBe(400);
		expect((await res.json()).error).toContain('kmctl');
		expect(mocks.fetch).not.toHaveBeenCalled();
	});

	it.each(['', 'namespace=pilot&id=a', 'crew=c&id=a', 'namespace=Bad_NS&crew=c&id=a', 'namespace=pilot&crew=../x&id=a'])(
		'rejects an invalid scope (%s) without asking the operator',
		async (query) => {
			expect((await get(query)).status).toBe(400);
			expect(mocks.fetch).not.toHaveBeenCalled();
		}
	);

	it.each(['', 'id=', 'id=a%20b', 'id=a&id=../b', `id=${'a'.repeat(129)}`])(
		'rejects missing or invalid ids (%s) without asking the operator',
		async (ids) => {
			const res = await get(`namespace=pilot&crew=c&${ids}`);
			expect(res.status).toBe(400);
			expect(mocks.fetch).not.toHaveBeenCalled();
		}
	);

	it('rejects more than 50 ids', async () => {
		const ids = Array.from({ length: 51 }, (_, i) => `id=c${i}`).join('&');
		expect((await get(`namespace=pilot&crew=c&${ids}`)).status).toBe(400);
		expect(mocks.fetch).not.toHaveBeenCalled();
	});

	it.each([400, 403, 404, 503])('passes on the operator %i with its message', async (status) => {
		mocks.fetch.mockResolvedValue(new Response(`operator says ${status}`, { status }));
		const res = await get('namespace=pilot&crew=c&id=a');
		expect(res.status).toBe(status);
		expect((await res.json()).error).toBe(`operator says ${status}`);
	});

	it.each([401, 500, 502])('answers 502 for an operator %i, which is not the viewer\'s doing', async (status) => {
		mocks.fetch.mockResolvedValue(new Response('boom', { status }));
		expect((await get('namespace=pilot&crew=c&id=a')).status).toBe(502);
	});

	it('answers 502 when the operator cannot be reached', async () => {
		mocks.fetch.mockRejectedValue(new Error('ECONNREFUSED'));
		const res = await get('namespace=pilot&crew=c&id=a');
		expect(res.status).toBe(502);
		expect((await res.json()).error).toContain('ECONNREFUSED');
	});

	describe('with a namespace selector', () => {
		beforeEach(() => {
			vi.stubEnv('KUBEMOOT_DASHBOARD_NAMESPACE_SELECTOR', 'kubemoot.ai/workshop-team=true');
			mocks.listNamespace.mockResolvedValue({ items: [{ metadata: { name: 'team-a' } }] });
		});

		it('answers 404 outside the scope and never asks the operator', async () => {
			expect((await get('namespace=kubemoot&crew=c&id=a')).status).toBe(404);
			expect(mocks.fetch).not.toHaveBeenCalled();
		});

		it('serves a crew inside the scope', async () => {
			mocks.fetch.mockResolvedValue(zip('c-conversation-a.zip'));
			expect((await get('namespace=team-a&crew=c&id=a')).status).toBe(200);
		});

		it('answers 503 when the scope cannot be resolved', async () => {
			mocks.listNamespace.mockRejectedValue(new Error('apiserver down'));
			expect((await get('namespace=team-a&crew=c&id=a')).status).toBe(503);
		});
	});
});
