import { describe, expect, it, vi } from 'vitest';
import { AskRefused } from './ask-client';
import { validReason as cleanReason } from './access-reason';
import {
	ErasureIncomplete,
	controlConversation,
	deleteConversation,
	isStillRunning,
	needsReason
} from './conversation-actions-client';

const scope = { namespace: 'team-a', crew: 'pilot' };
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
const calls = (f: ReturnType<typeof vi.fn>) => f.mock.calls as unknown as [string, RequestInit][];
const report = { conversationId: 'c-1', threads: ['t1'], deleted: { history: 1 }, kept: ['logs and backups'] };

describe('controlConversation', () => {
	it.each(['stop', 'pause', 'resume'] as const)('posts %s to the dashboard route', async (control) => {
		const fetchFn = vi.fn(async () => json({ action: control }));
		await controlConversation(fetchFn, scope, 'c-1', control);
		const [url, init] = calls(fetchFn)[0];
		expect(url).toBe(`/api/gateway/team-a/pilot/c-1/${control}`);
		expect(init.method).toBe('POST');
		expect(JSON.stringify(init)).not.toMatch(/authorization|bearer/i);
	});

	it('sends the reason in the access reason header, trimmed, only when it is valid', async () => {
		const fetchFn = vi.fn(async () => json({}));
		await controlConversation(fetchFn, scope, 'c-1', 'stop', '  checking a stuck run ');
		await controlConversation(fetchFn, scope, 'c-1', 'stop', '   ');
		expect(calls(fetchFn)[0][1].headers).toEqual({ 'Kubemoot-Access-Reason': 'checking a stuck run' });
		expect(calls(fetchFn)[1][1].headers).toEqual({});
	});

	it('throws the refusal with its status and words', async () => {
		const fetchFn = async () => json({ error: 'the conversation has no running discussion' }, 409);
		await expect(controlConversation(fetchFn as never, scope, 'c-1', 'pause')).rejects.toMatchObject({ status: 409, message: expect.stringContaining('no running') });
	});

	it('says plainly when the server cannot be reached', async () => {
		await expect(controlConversation(async () => Promise.reject(new Error('net')), scope, 'c-1', 'stop')).rejects.toMatchObject({ status: 0 });
	});
});

describe('deleteConversation', () => {
	it('deletes and returns the erasure report', async () => {
		const fetchFn = vi.fn(async () => json(report));
		expect(await deleteConversation(fetchFn, scope, 'c-1')).toEqual(report);
		const [url, init] = calls(fetchFn)[0];
		expect(url).toBe('/api/gateway/team-a/pilot/c-1');
		expect(init.method).toBe('DELETE');
	});

	it('asks to stop first only with force', async () => {
		const fetchFn = vi.fn(async () => json(report));
		await deleteConversation(fetchFn, scope, 'c-1', { force: true });
		expect(calls(fetchFn)[0][0]).toBe('/api/gateway/team-a/pilot/c-1?force=true');
	});

	it('reports a running conversation as a 409 to offer Stop and delete', async () => {
		const fetchFn = async () => json({ error: 'the conversation has a running discussion' }, 409);
		const err = await deleteConversation(fetchFn as never, scope, 'c-1').catch((e: unknown) => e);
		expect(isStillRunning(err)).toBe(true);
		expect(isStillRunning(new AskRefused(404, 'x'))).toBe(false);
	});

	it('throws the report when a store could not be cleared, so the delete can be repeated', async () => {
		const fetchFn = async () => json({ ...report, failed: ['workspace'] }, 500);
		const err = await deleteConversation(fetchFn as never, scope, 'c-1').catch((e: unknown) => e);
		expect(err).toBeInstanceOf(ErasureIncomplete);
		expect((err as ErasureIncomplete).report.failed).toEqual(['workspace']);
	});

	it('refuses a success that carries no report, and a 500 that carries none', async () => {
		await expect(deleteConversation((async () => json({ ok: true })), scope, 'c-1')).rejects.toMatchObject({ status: 502 });
		await expect(deleteConversation((async () => json({ error: 'boom' }, 500)), scope, 'c-1')).rejects.toMatchObject({ status: 500 });
	});
});

describe('the reason', () => {
	it('is 1 to 200 characters once trimmed', () => {
		expect(cleanReason('  ticket 42 ')).toBe('ticket 42');
		expect(cleanReason('')).toBeUndefined();
		expect(cleanReason(undefined)).toBeUndefined();
		expect(cleanReason('x'.repeat(201))).toBeUndefined();
		expect(cleanReason('x'.repeat(200))).toHaveLength(200);
	});

	it('is asked for when the gateway says an operator needs one', () => {
		const gateway = new AskRefused(400, "The crew's gateway answered 400. (acting on another caller's conversations needs a reason of 1 to 200 characters in the Kubemoot-Access-Reason header)");
		expect(needsReason(gateway)).toBe(true);
		expect(needsReason(new AskRefused(400, 'Invalid conversation.'))).toBe(false);
		expect(needsReason(new AskRefused(404, 'reason'))).toBe(false);
		expect(needsReason(new Error('reason'))).toBe(false);
	});
});
