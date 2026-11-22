import { describe, expect, it, vi } from 'vitest';

const detail = { deployment: { available: false, reason: 'r' }, startedAt: { available: false, reason: 'r' }, leader: { available: false, reason: 'r' }, crds: { available: false, reason: 'r' } };
vi.mock('#lib/server/k8s/operator.js', () => ({ readOperatorDetail: vi.fn(async () => detail) }));

describe('GET /api/kubemoot/operator', () => {
	it('answers 200, uncached, with each reading and the reason it is missing', async () => {
		const { GET } = await import('./+server');
		const res = await (GET as unknown as () => Promise<Response>)();
		expect(res.status).toBe(200);
		expect(res.headers.get('Cache-Control')).toBe('no-store');
		expect(await res.json()).toEqual(detail);
	});
});
