// @vitest-environment node
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { resetSignInAttempts } from '#lib/server/auth/rate-limit.js';
import { SignInError } from '#lib/server/auth/token-review.js';

// A plain function, not vi.fn: a rejected vi.fn result is reported as an unhandled rejection.
const mocks = vi.hoisted(() => ({ impl: async (): Promise<unknown> => ({}) }));
vi.mock('#lib/server/auth/sign-in.js', () => ({ signIn: () => mocks.impl() }));

import { actions } from './+page.server';

function run(token: string | null) {
	const form = new FormData();
	if (token !== null) form.set('token', token);
	const set = vi.fn();
	const call = (actions.default as unknown as (e: unknown) => Promise<unknown>)({
		request: new Request('https://dash.example.test/signin', { method: 'POST', body: form }),
		cookies: { set, get: () => undefined },
		url: new URL('https://dash.example.test/signin'),
		getClientAddress: () => 'client-1'
	});
	return { call, set };
}


beforeEach(() => resetSignInAttempts());

describe('the sign-in form action', () => {
	it('sets the session cookie HttpOnly, Secure and SameSite=Strict, then redirects', async () => {
		mocks.impl = async () => ({ sessionId: 'opaque-id', username: 'u', expiresAt: Date.now() + 60_000 });
		const { call, set } = run('some-token');
		await expect(call).rejects.toMatchObject({ status: 303 });
		const [name, value, options] = set.mock.calls[0];
		expect(name).toBe('kubemoot_session');
		expect(value).toBe('opaque-id');
		expect(options).toMatchObject({ httpOnly: true, secure: true, sameSite: 'strict' });
		expect(JSON.stringify(set.mock.calls)).not.toContain('some-token');
	});

	it('refuses a token the cluster rejects with a message, no cookie, and never echoes the token', async () => {
		mocks.impl = async () => {
			throw new SignInError('invalid', 'The cluster does not accept this token.');
		};
		const { call, set } = run('bad-token');
		const result = (await call) as { status: number; data: Record<string, unknown> };
		expect(result.status).toBe(401);
		expect(result.data.message).toContain('does not accept');
		expect(JSON.stringify(result)).not.toContain('bad-token');
		expect(set).not.toHaveBeenCalled();
	});

	it('answers 429 after too many attempts from one address', async () => {
		mocks.impl = async () => ({ sessionId: 'x', username: 'u', expiresAt: Date.now() + 60_000 });
		for (let i = 0; i < 10; i++) await run('t').call.catch(() => undefined);
		expect(((await run('t').call) as { status: number }).status).toBe(429);
	});

	it('answers 503 when the review could not be made, and 400 for no token', async () => {
		mocks.impl = async () => {
			throw new SignInError('unavailable', 'down');
		};
		expect(((await run('t').call) as { status: number }).status).toBe(503);
		mocks.impl = async () => {
			throw new SignInError('empty', 'paste');
		};
		expect(((await run(null).call) as { status: number }).status).toBe(400);
	});
});
