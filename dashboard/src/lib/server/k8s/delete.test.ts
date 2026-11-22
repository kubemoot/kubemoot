// @vitest-environment node
import { beforeEach, describe, expect, it, vi } from 'vitest';

const deleteNamespacedCustomObject = vi.fn();
vi.mock('./client.js', () => ({ getCustomObjectsApi: () => ({ deleteNamespacedCustomObject }) }));

const { deleteAs, deleteResource } = await import('./delete.js');
const { BearerToken } = await import('../auth/bearer-token.js');
const { runWithIdentity } = await import('../auth/identity.js');

const asCaller = <T>(fn: () => T) =>
	runWithIdentity({ kind: 'caller', username: 'jonathan', token: new BearerToken('callers-own-token'), ended: new AbortController().signal }, fn);

const refusal = (code: number, message = 'api said no') => Object.assign(new Error(message), { code });

beforeEach(() => {
	deleteNamespacedCustomObject.mockReset().mockResolvedValue({});
});

describe('deleteResource', () => {
	it('deletes the Crew by its plural, as the caller, and does nothing else', async () => {
		await deleteResource('crews', 'crew-a', 'homelab-pilot');
		expect(deleteNamespacedCustomObject).toHaveBeenCalledOnce();
		expect(deleteNamespacedCustomObject).toHaveBeenCalledWith({ group: 'kubemoot.ai', version: 'v1alpha1', namespace: 'crew-a', plural: 'crews', name: 'homelab-pilot' });
	});

	it('deletes a CrewFitnessSuite by its plural', async () => {
		await deleteResource('crewfitnesssuites', 'crew-a', 'gate-run');
		expect(deleteNamespacedCustomObject).toHaveBeenCalledWith(expect.objectContaining({ plural: 'crewfitnesssuites', name: 'gate-run' }));
	});
});

describe('deleteAs', () => {
	it('sends nothing and asks to sign in when nobody is signed in, so the service account never deletes', async () => {
		const res = await runWithIdentity({ kind: 'anonymous' }, () => deleteAs('crews', 'crew-a', 'homelab-pilot'));
		expect(res.status).toBe(401);
		expect((await res.json()).signIn).toBe(true);
		expect(deleteNamespacedCustomObject).not.toHaveBeenCalled();
	});

	it('sends nothing outside a request', async () => {
		expect((await deleteAs('crews', 'crew-a', 'x')).status).toBe(401);
		expect(deleteNamespacedCustomObject).not.toHaveBeenCalled();
	});

	it('answers what was deleted', async () => {
		const res = await asCaller(() => deleteAs('crewfitnesssuites', 'crew-a', 'gate-run'));
		expect(res.status).toBe(200);
		expect(await res.json()).toEqual({ deleted: true, kind: 'crewfitnesssuites', namespace: 'crew-a', name: 'gate-run' });
	});

	it('answers 403 in plain words, naming the thing, when the person\'s RBAC refuses', async () => {
		deleteNamespacedCustomObject.mockRejectedValue(refusal(403));
		const res = await asCaller(() => deleteAs('crews', 'crew-a', 'homelab-pilot'));
		expect(res.status).toBe(403);
		const body = await res.json();
		expect(body.denied).toBe(true);
		expect(body.error).toBe('You do not have permission to delete this crew. Your Kubernetes account is not allowed to; ask the cluster administrator for access.');
	});

	it('says fitness run, not suite, for a refused suite delete', async () => {
		deleteNamespacedCustomObject.mockRejectedValue(refusal(403));
		expect((await (await asCaller(() => deleteAs('crewfitnesssuites', 'n', 'x'))).json()).error).toContain('delete this fitness run');
	});

	it('answers 404 when it is already gone', async () => {
		deleteNamespacedCustomObject.mockRejectedValue(refusal(404));
		const res = await asCaller(() => deleteAs('crewfitnesssuites', 'crew-a', 'gone'));
		expect(res.status).toBe(404);
		expect((await res.json()).error).toBe('This fitness run no longer exists.');
	});

	it('answers 401 asking for a new sign-in when the cluster stopped accepting the token', async () => {
		deleteNamespacedCustomObject.mockRejectedValue(refusal(401));
		const res = await asCaller(() => deleteAs('crews', 'crew-a', 'x'));
		expect(res.status).toBe(401);
		expect((await res.json()).error).toContain('Sign in again');
	});

	it('answers 500 with the message for anything else', async () => {
		deleteNamespacedCustomObject.mockRejectedValue(new Error('etcd timeout'));
		const res = await asCaller(() => deleteAs('crews', 'crew-a', 'x'));
		expect(res.status).toBe(500);
		expect((await res.json()).error).toBe('etcd timeout');
	});
});
