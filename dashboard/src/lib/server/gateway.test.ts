// @vitest-environment node
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { BearerToken } from './auth/bearer-token';
import { runWithIdentity } from './auth/identity';

const mocks = vi.hoisted(() => ({ getCrew: vi.fn() }));
vi.mock('./k8s/kubemoot-crds.js', () => ({ getCrew: mocks.getCrew }));
vi.mock('./scope.js', () => ({ guardNamespace: async () => null }));

import { callGateway, describeRefusal, gateRequest, gatewayUrl, parseAsk, relayRefusal, requireCrewAccess } from './gateway';

const asCaller = <T>(fn: () => T) =>
	runWithIdentity({ kind: 'caller', username: 'u', token: new BearerToken('callers-token'), ended: new AbortController().signal }, fn);

let fetchMock: ReturnType<typeof vi.fn>;
beforeEach(() => {
	fetchMock = vi.fn(async () => new Response('{}', { status: 200 }));
	vi.stubGlobal('fetch', fetchMock);
	mocks.getCrew.mockReset().mockResolvedValue({});
});
afterEach(() => vi.unstubAllGlobals());

describe('gatewayUrl', () => {
	it('addresses the crew Service in its namespace', () => {
		expect(gatewayUrl('team-a', 'pilot', '/api/v1/discussions/pilot', '')).toBe(
			'http://pilot-discussion.team-a.svc.cluster.local/api/v1/discussions/pilot'
		);
	});

	it('fills a template', () => {
		expect(gatewayUrl('ns', 'c', '/x', 'http://localhost:8080/{namespace}/{crew}/')).toBe('http://localhost:8080/ns/c/x');
	});

	it.each(['', 'A', 'a.b', 'a/b', '-a', 'a b', 'x@evil.test', 'a'.repeat(64)])('refuses the name %j', (name) => {
		expect(() => gatewayUrl(name, 'c', '/x', '')).toThrow();
		expect(() => gatewayUrl('ns', name, '/x', '')).toThrow();
	});
});

describe('callGateway', () => {
	it('sends the signed-in caller bearer token, exactly as kmctl does', async () => {
		await asCaller(() => callGateway('ns', 'pilot', '/api/v1/discussions/pilot', { method: 'POST', body: { message: 'hi' } }));
		const [url, init] = fetchMock.mock.calls[0];
		expect(url).toContain('pilot-discussion.ns.svc');
		expect(init.method).toBe('POST');
		expect(init.headers.Authorization).toBe('Bearer callers-token');
		expect(init.redirect).toBe('manual');
		expect(JSON.parse(init.body)).toEqual({ message: 'hi' });
	});

	it('sends nothing when nobody is signed in', async () => {
		expect(await callGateway('ns', 'pilot', '/x')).toBeNull();
		expect(await runWithIdentity({ kind: 'anonymous' }, () => callGateway('ns', 'pilot', '/x'))).toBeNull();
		expect(fetchMock).not.toHaveBeenCalled();
	});
});

describe('the gateway refusals', () => {
	const refusal = (status: number, error?: string) => new Response(JSON.stringify(error ? { error } : {}), { status });

	it.each([
		[401, /did not accept your token/],
		[403, /do not have permission/],
		[404, /not yours/],
		[503, /right now/]
	])('say %i plainly and keep the status', async (status, words) => {
		const res = await relayRefusal(refusal(status));
		expect(res.status).toBe(status);
		expect((await res.json()).error).toMatch(words);
	});

	it('adds the gateway reason', async () => {
		expect(await describeRefusal(refusal(403, 'caller may not get crews'))).toContain('caller may not get crews');
	});

	it('copes with a body that is not JSON', async () => {
		expect(await describeRefusal(new Response('<html>', { status: 502 }))).toContain('502');
	});
});

describe('requireCrewAccess', () => {
	it('passes a caller who may get the crew, using their own token', async () => {
		expect(await asCaller(() => requireCrewAccess('ns', 'c'))).toBeNull();
		expect(mocks.getCrew).toHaveBeenCalledWith('ns', 'c');
	});

	it('answers 403 when RBAC refuses, and 404 when the crew is missing', async () => {
		mocks.getCrew.mockRejectedValueOnce({ code: 403 });
		expect((await requireCrewAccess('ns', 'c'))?.status).toBe(403);
		mocks.getCrew.mockRejectedValueOnce({ code: 404 });
		expect((await requireCrewAccess('ns', 'c'))?.status).toBe(404);
	});

	it('says 503, not 404, when the cluster cannot be asked', async () => {
		mocks.getCrew.mockRejectedValueOnce(new Error('connection refused'));
		expect((await requireCrewAccess('ns', 'c'))?.status).toBe(503);
	});

	it('gateRequest refuses bad names before any call', async () => {
		expect((await gateRequest('Bad_Name', 'c'))?.status).toBe(400);
		expect((await gateRequest('ns', 'c', '../x'))?.status).toBe(400);
		expect(mocks.getCrew).not.toHaveBeenCalled();
	});
});

describe('parseAsk', () => {
	it('keeps only the fields the gateway takes', () => {
		const parsed = parseAsk({ message: 'hi', conversationId: 'abc-1', timeZone: 'UTC', principal: { id: 'forged' }, extra: 1 });
		expect(parsed).toEqual({ ok: true, body: { message: 'hi', conversationId: 'abc-1', timeZone: 'UTC' } });
	});

	it.each([null, {}, { message: '   ' }, { message: 5 }, { message: 'x'.repeat(20_001) }, { message: 'a', conversationId: '../x' }])(
		'refuses %j',
		(input) => {
			expect(parseAsk(input).ok).toBe(false);
		}
	);
});
