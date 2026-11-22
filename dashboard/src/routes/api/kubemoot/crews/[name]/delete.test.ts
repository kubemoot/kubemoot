import { beforeEach, describe, expect, it, vi } from 'vitest';

const deleteAs = vi.fn();
const guardNamespace = vi.fn();
vi.mock('#lib/server/k8s/index.js', () => ({ deleteAs, getCrew: vi.fn(), kubeFailure: vi.fn() }));
vi.mock('#lib/server/scope.js', () => ({ guardNamespace }));

const { DELETE } = await import('./+server.js');

const call = (name: string, query = '?namespace=crew-a') =>
	DELETE({ params: { name }, url: new URL(`https://dash.test/api/kubemoot/crews/${name}${query}`) } as Parameters<typeof DELETE>[0]);

beforeEach(() => {
	deleteAs.mockReset().mockResolvedValue(Response.json({ deleted: true }));
	guardNamespace.mockReset().mockResolvedValue(null);
});

describe('DELETE /api/kubemoot/crews/{name}', () => {
	it('deletes the crew of the named namespace as the caller and answers what the delete answered', async () => {
		const res = await call('homelab-pilot');
		expect(deleteAs).toHaveBeenCalledWith('crews', 'crew-a', 'homelab-pilot');
		expect(await res.json()).toEqual({ deleted: true });
	});

	it('passes a 403 the cluster gave through unchanged', async () => {
		deleteAs.mockResolvedValue(Response.json({ error: 'You do not have permission to delete this crew.', denied: true }, { status: 403 }));
		const res = await call('homelab-pilot');
		expect(res.status).toBe(403);
		expect((await res.json()).denied).toBe(true);
	});

	it('passes a 404 through unchanged', async () => {
		deleteAs.mockResolvedValue(Response.json({ error: 'This crew no longer exists.' }, { status: 404 }));
		expect((await call('gone')).status).toBe(404);
	});

	it('answers 400 and deletes nothing when no namespace is given', async () => {
		const res = await call('homelab-pilot', '');
		expect(res.status).toBe(400);
		expect(deleteAs).not.toHaveBeenCalled();
	});

	it('deletes nothing in a namespace outside the dashboard scope', async () => {
		guardNamespace.mockResolvedValue(Response.json({ error: 'not found' }, { status: 404 }));
		expect((await call('homelab-pilot', '?namespace=other')).status).toBe(404);
		expect(deleteAs).not.toHaveBeenCalled();
	});
});
