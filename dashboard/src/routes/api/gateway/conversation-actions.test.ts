// @vitest-environment node
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { BearerToken } from '#lib/server/auth/bearer-token.js';
import { runWithIdentity } from '#lib/server/auth/identity.js';

const mocks = vi.hoisted(() => ({ getCrew: vi.fn() }));
vi.mock('#lib/server/k8s/kubemoot-crds.js', () => ({ getCrew: mocks.getCrew }));
vi.mock('#lib/server/scope.js', () => ({ guardNamespace: async () => null }));

import { POST as control } from './[namespace]/[crew]/[conversationId]/[control]/+server';
import { DELETE as remove } from './[namespace]/[crew]/[conversationId]/+server';

const asCaller = <T>(fn: () => T) =>
	runWithIdentity({ kind: 'caller', username: 'u', token: new BearerToken('callers-token'), ended: new AbortController().signal }, fn);

const params = { namespace: 'ns', crew: 'pilot', conversationId: 'c-1' };
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });

let fetchMock: ReturnType<typeof vi.fn>;
beforeEach(() => {
	fetchMock = vi.fn(async () => json({}));
	vi.stubGlobal('fetch', fetchMock);
	mocks.getCrew.mockReset().mockResolvedValue({});
});
afterEach(() => vi.unstubAllGlobals());

// The handlers read only params, request and url; the rest of SvelteKit's event is not needed.
const callControl = (name: string, headers: Record<string, string> = {}) =>
	asCaller(() =>
		control({ params: { ...params, control: name }, request: new Request('https://d/', { method: 'POST', headers }) } as never)
	) as Promise<Response>;
const callDelete = (query = '', headers: Record<string, string> = {}) =>
	asCaller(() =>
		remove({
			params,
			request: new Request('https://d/', { method: 'DELETE', headers }),
			url: new URL(`https://d/x${query}`)
		} as never)
	) as Promise<Response>;

describe.each(['stop', 'pause', 'resume'])('%s a conversation', (action) => {
	it('posts to the crew gateway with the caller token and returns its answer', async () => {
		fetchMock.mockResolvedValue(json({ conversationId: 'c-1', action, threads: ['t1'] }));
		const res = await callControl(action);
		expect(res.status).toBe(200);
		expect(await res.json()).toMatchObject({ action });
		const [url, init] = fetchMock.mock.calls[0];
		expect(url).toContain(`/api/v1/discussions/pilot/c-1/${action}`);
		expect(init.method).toBe('POST');
		expect(init.headers.Authorization).toBe('Bearer callers-token');
	});

	it('passes the access reason on and relays a 409 for nothing running', async () => {
		fetchMock.mockResolvedValue(json({ error: 'the conversation has no running discussion' }, 409));
		const res = await callControl(action, { 'Kubemoot-Access-Reason': 'checking a stuck run' });
		expect(res.status).toBe(409);
		expect(fetchMock.mock.calls[0][1].headers['Kubemoot-Access-Reason']).toBe('checking a stuck run');
	});
});

describe('a control that is not one', () => {
	it('is not found and calls nothing', async () => {
		expect((await callControl('restart')).status).toBe(404);
		expect(fetchMock).not.toHaveBeenCalled();
	});

	it('refuses a bad reason before calling the gateway', async () => {
		expect((await callControl('stop', { 'Kubemoot-Access-Reason': ' ' })).status).toBe(400);
		expect(fetchMock).not.toHaveBeenCalled();
	});
});

describe('deleting a conversation', () => {
	const report = { conversationId: 'c-1', threads: ['t1'], deleted: { history: 1, owner: 1 }, kept: ['logs and backups'] };

	it('returns the erasure report', async () => {
		fetchMock.mockResolvedValue(json(report));
		const res = await callDelete();
		expect(res.status).toBe(200);
		expect(await res.json()).toEqual(report);
		const [url, init] = fetchMock.mock.calls[0];
		expect(url).toMatch(/\/api\/v1\/discussions\/pilot\/c-1$/);
		expect(init.method).toBe('DELETE');
	});

	it('passes force on, and only when it is true', async () => {
		fetchMock.mockResolvedValue(json(report));
		await callDelete('?force=true');
		await callDelete('?force=yes');
		expect(fetchMock.mock.calls[0][0]).toMatch(/c-1\?force=true$/);
		expect(fetchMock.mock.calls[1][0]).toMatch(/c-1$/);
	});

	it('relays a 409 for a running conversation in plain words', async () => {
		fetchMock.mockResolvedValue(json({ error: 'the conversation has a running discussion' }, 409));
		const res = await callDelete();
		expect(res.status).toBe(409);
		expect((await res.json()).error).toContain('running discussion');
	});

	it('returns the report with its 500 when a store could not be cleared', async () => {
		fetchMock.mockResolvedValue(json({ ...report, failed: ['workspace'] }, 500));
		const res = await callDelete();
		expect(res.status).toBe(500);
		expect((await res.json()).failed).toEqual(['workspace']);
	});

	it('answers 404 for a conversation that is not theirs and 401 when nobody is signed in', async () => {
		fetchMock.mockResolvedValue(json({ error: 'conversation not found' }, 404));
		expect((await callDelete()).status).toBe(404);
		const signedOut = await runWithIdentity({ kind: 'anonymous' }, () =>
			remove({ params, request: new Request('https://d/', { method: 'DELETE' }), url: new URL('https://d/') } as never)
		);
		expect(signedOut.status).toBe(401);
	});

	it('says 502 when the gateway cannot be reached', async () => {
		fetchMock.mockRejectedValue(new Error('refused'));
		expect((await callDelete()).status).toBe(502);
	});
});
