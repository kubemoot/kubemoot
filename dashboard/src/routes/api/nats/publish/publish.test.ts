import { beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({ getNatsConnection: vi.fn() }));

vi.mock('#lib/server/nats-client.js', () => ({
	getNatsConnection: mocks.getNatsConnection,
	sc: { encode: (s: string) => new TextEncoder().encode(s) }
}));
vi.mock('#lib/server/scope.js', () => ({
	subjectTargetAllowed: async () => true,
	forbidden: () => new Response('forbidden', { status: 403 })
}));

import { POST } from './+server';

const post = (body: unknown) =>
	(POST as unknown as (e: unknown) => Promise<Response>)({
		request: new Request('https://dashboard/x', { method: 'POST', body: JSON.stringify(body) })
	});

describe('POST /api/nats/publish', () => {
	beforeEach(() => mocks.getNatsConnection.mockReset());

	it('answers success only after the server has the message', async () => {
		const order: string[] = [];
		mocks.getNatsConnection.mockResolvedValue({
			publish: () => order.push('publish'),
			flush: async () => {
				order.push('flush');
			}
		});
		const res = await post({ subject: 's', data: 'x' });
		expect(res.status).toBe(200);
		expect(order).toEqual(['publish', 'flush']);
	});

	it('answers 500 with the reason when the flush fails', async () => {
		mocks.getNatsConnection.mockResolvedValue({
			publish: () => {},
			flush: async () => {
				throw new Error('connection closed');
			}
		});
		const res = await post({ subject: 's', data: 'x' });
		expect(res.status).toBe(500);
		expect((await res.json()).error).toBe('connection closed');
	});

	it('answers 400 without a subject or data', async () => {
		expect((await post({ subject: 's' })).status).toBe(400);
		expect(mocks.getNatsConnection).not.toHaveBeenCalled();
	});
});
