import { beforeEach, describe, expect, it, vi } from 'vitest';

const deleteAs = vi.fn();
const guardNamespace = vi.fn();
vi.mock('#lib/server/k8s/index.js', () => ({ deleteAs, getCrewFitnessSuite: vi.fn() }));
vi.mock('#lib/server/scope.js', () => ({ guardNamespace }));

const { DELETE } = await import('./+server.js');

const call = (namespace: string, name: string) => DELETE({ params: { namespace, name } } as Parameters<typeof DELETE>[0]);

beforeEach(() => {
	deleteAs.mockReset().mockResolvedValue(Response.json({ deleted: true }));
	guardNamespace.mockReset().mockResolvedValue(null);
});

describe('DELETE /api/kubemoot/crewfitnesssuites/{namespace}/{name}', () => {
	it('deletes the suite as the caller and answers what the delete answered', async () => {
		const res = await call('crew-a', 'gate-run');
		expect(deleteAs).toHaveBeenCalledWith('crewfitnesssuites', 'crew-a', 'gate-run');
		expect(await res.json()).toEqual({ deleted: true });
	});

	it('passes a 403 through unchanged', async () => {
		deleteAs.mockResolvedValue(Response.json({ error: 'You do not have permission to delete this fitness run.', denied: true }, { status: 403 }));
		expect((await call('crew-a', 'gate-run')).status).toBe(403);
	});

	it('passes a 404 through unchanged', async () => {
		deleteAs.mockResolvedValue(Response.json({ error: 'This fitness run no longer exists.' }, { status: 404 }));
		expect((await call('crew-a', 'gone')).status).toBe(404);
	});

	it('deletes nothing in a namespace outside the dashboard scope', async () => {
		guardNamespace.mockResolvedValue(Response.json({ error: 'not found' }, { status: 404 }));
		expect((await call('other', 'gate-run')).status).toBe(404);
		expect(deleteAs).not.toHaveBeenCalled();
	});
});
