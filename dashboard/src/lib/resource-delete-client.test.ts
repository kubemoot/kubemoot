import { describe, expect, it, vi } from 'vitest';
import { DeleteRefused, deleteResource } from './resource-delete-client';

const answer = (status: number, body: unknown = {}) => vi.fn().mockResolvedValue(new Response(JSON.stringify(body), { status }));

describe('deleteResource', () => {
	it('sends a DELETE for a crew with its namespace in the query', async () => {
		const fetchFn = answer(200, { deleted: true });
		await deleteResource(fetchFn, 'crew', 'crew-a', 'homelab-pilot');
		expect(fetchFn).toHaveBeenCalledWith('/api/kubemoot/crews/homelab-pilot?namespace=crew-a', { method: 'DELETE' });
	});

	it('sends a DELETE for a fitness run with its namespace in the path', async () => {
		const fetchFn = answer(200, { deleted: true });
		await deleteResource(fetchFn, 'suite', 'crew-a', 'gate-run');
		expect(fetchFn).toHaveBeenCalledWith('/api/kubemoot/crewfitnesssuites/crew-a/gate-run', { method: 'DELETE' });
	});

	it('throws the server\'s plain words with the status on a 403', async () => {
		const fetchFn = answer(403, { error: 'You do not have permission to delete this crew.' });
		const err = await deleteResource(fetchFn, 'crew', 'n', 'x').catch((e: unknown) => e);
		expect(err).toBeInstanceOf(DeleteRefused);
		expect(err).toMatchObject({ status: 403, message: 'You do not have permission to delete this crew.' });
	});

	it('throws on a 404 with the words that it is gone', async () => {
		const err = await deleteResource(answer(404, { error: 'This fitness run no longer exists.' }), 'suite', 'n', 'x').catch((e: unknown) => e);
		expect(err).toMatchObject({ status: 404, message: 'This fitness run no longer exists.' });
	});

	it('falls back to a plain sentence when the answer has no error text', async () => {
		const err = await deleteResource(vi.fn().mockResolvedValue(new Response('<html>', { status: 502 })), 'crew', 'n', 'x').catch((e: unknown) => e);
		expect(err).toMatchObject({ status: 502, message: 'The dashboard answered 502.' });
	});

	it('says the server could not be reached when the request itself fails', async () => {
		const err = await deleteResource(vi.fn().mockRejectedValue(new TypeError('failed')), 'crew', 'n', 'x').catch((e: unknown) => e);
		expect(err).toMatchObject({ status: 0 });
		expect((err as Error).message).toContain('could not be reached');
	});
});
