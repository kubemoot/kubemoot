import { beforeEach, describe, expect, it, vi } from 'vitest';

const getCrewFitnessSuite = vi.fn();
const guardNamespace = vi.fn();
vi.mock('#lib/server/k8s/index.js', () => ({ getCrewFitnessSuite }));
vi.mock('#lib/server/scope.js', () => ({ guardNamespace }));

const { GET } = await import('./+server.js');

const call = (namespace: string, name: string) => GET({ params: { namespace, name } } as Parameters<typeof GET>[0]);

beforeEach(() => {
	getCrewFitnessSuite.mockReset();
	guardNamespace.mockReset().mockResolvedValue(null);
});

describe('GET /api/kubemoot/crewfitnesssuites/{namespace}/{name}', () => {
	it('returns the suite as Kubernetes returned it', async () => {
		const suite = { kind: 'CrewFitnessSuite', metadata: { name: 's', managedFields: [{}] } };
		getCrewFitnessSuite.mockResolvedValue(suite);
		const res = await call('ns', 's');
		expect(getCrewFitnessSuite).toHaveBeenCalledWith('ns', 's');
		expect(await res.json()).toEqual(suite);
	});

	it('answers with the guard refusal and reads nothing for a namespace outside the scope', async () => {
		guardNamespace.mockResolvedValue(new Response('{"error":"no"}', { status: 403 }));
		const res = await call('other', 's');
		expect(res.status).toBe(403);
		expect(getCrewFitnessSuite).not.toHaveBeenCalled();
	});

	it('reports a failed read as a 500 with the message', async () => {
		getCrewFitnessSuite.mockRejectedValue(new Error('not found'));
		const res = await call('ns', 'gone');
		expect(res.status).toBe(500);
		expect(await res.json()).toEqual({ error: 'not found' });
	});
});
