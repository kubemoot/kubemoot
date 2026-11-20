import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const QUESTION = 'why does the invoice for ACME-7731 fail';
const SECRET = 'sk-test0123456789abcdefABCDEF';

const { getNatsConnection, subjectTargetAllowed } = vi.hoisted(() => ({
	getNatsConnection: vi.fn(),
	subjectTargetAllowed: vi.fn()
}));
vi.mock('#lib/server/nats-client.js', () => ({
	getNatsConnection,
	sc: { encode: (text: string) => new TextEncoder().encode(text) }
}));
vi.mock('#lib/server/scope.js', () => ({
	subjectTargetAllowed,
	forbidden: (what: string) => Response.json({ error: what }, { status: 403 })
}));

import { POST } from './+server';

const levels = ['log', 'info', 'warn', 'error', 'debug'] as const;
let written: string[];

beforeEach(() => {
	written = [];
	for (const level of levels) {
		vi.spyOn(console, level).mockImplementation((...args: unknown[]) => {
			written.push(args.map((a) => (a instanceof Error ? `${a.message}\n${a.stack}` : String(a))).join(' '));
		});
	}
	subjectTargetAllowed.mockResolvedValue(true);
	getNatsConnection.mockReset();
});

afterEach(() => vi.restoreAllMocks());

function post(data: string) {
	return POST({
		request: new Request('https://dashboard/api/nats/publish', {
			method: 'POST',
			body: JSON.stringify({ subject: 'kubemoot.chat.demo.admin', data })
		})
	} as Parameters<typeof POST>[0]);
}

describe('POST /api/nats/publish logs no question or secret', () => {
	it('publishes a question without logging it', async () => {
		const publish = vi.fn();
		getNatsConnection.mockResolvedValue({ publish, flush: vi.fn() });

		const response = await post(`${QUESTION} ${SECRET}`);

		expect(response.status).toBe(200);
		expect(publish).toHaveBeenCalledOnce();
		expect(written.join('\n')).not.toContain(QUESTION);
		expect(written.join('\n')).not.toContain(SECRET);
	});

	it('logs nothing of the question when the publish fails', async () => {
		getNatsConnection.mockResolvedValue({
			publish: vi.fn(),
			flush: vi.fn().mockRejectedValue(new Error('NATS is down'))
		});

		const response = await post(`${QUESTION} ${SECRET}`);

		expect(response.status).toBe(500);
		expect(written.join('\n')).not.toContain(QUESTION);
		expect(written.join('\n')).not.toContain(SECRET);
	});
});
