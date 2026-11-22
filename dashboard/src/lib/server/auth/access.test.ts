import { describe, expect, it } from 'vitest';
import { decideAccess, pathInApp, type AccessInput } from './access';

const base = { signedIn: false, anonymousReadOnly: false };
const decide = (o: Partial<AccessInput> & Pick<AccessInput, 'method' | 'path'>) => decideAccess({ ...base, ...o });

describe('without a session', () => {
	it('sends every page to the sign-in page', () => {
		for (const path of ['/', '/crews', '/conversations', '/nats']) {
			expect(decide({ method: 'GET', path })).toEqual({ action: 'redirect', to: '/signin' });
		}
	});

	it('answers the API with 401', () => {
		expect(decide({ method: 'GET', path: '/api/kubemoot/crews' })).toMatchObject({ action: 'deny', status: 401 });
		expect(decide({ method: 'GET', path: '/api/gateway/ns/c/conversations' })).toMatchObject({ status: 401 });
	});

	it('serves the sign-in page and the health probe', () => {
		expect(decide({ method: 'GET', path: '/signin' })).toEqual({ action: 'allow' });
		expect(decide({ method: 'POST', path: '/signin' })).toEqual({ action: 'allow' });
		expect(decide({ method: 'GET', path: '/api/health' })).toEqual({ action: 'allow' });
	});

	it('refuses asking', () => {
		expect(decide({ method: 'POST', path: '/api/gateway/ns/crew/ask' })).toMatchObject({ action: 'deny', status: 401 });
	});
});

describe('anonymous read-only mode', () => {
	const anon = { anonymousReadOnly: true };

	it('allows the read views', () => {
		expect(decide({ method: 'GET', path: '/crews', ...anon })).toEqual({ action: 'allow' });
		expect(decide({ method: 'GET', path: '/api/kubemoot/crews', ...anon })).toEqual({ action: 'allow' });
	});

	it('reaches no write route and no gateway route', () => {
		for (const method of ['POST', 'PUT', 'PATCH', 'DELETE']) {
			for (const path of ['/api/gateway/ns/crew/ask', '/signout', '/api/nats/publish', '/api/kubemoot/crews/x', '/crews']) {
				const d = decide({ method, path, ...anon });
				expect(d.action, `${method} ${path}`).not.toBe('allow');
			}
		}
		expect(decide({ method: 'GET', path: '/api/gateway/ns/crew/conversations', ...anon })).toMatchObject({ status: 401 });
		expect(decide({ method: 'GET', path: '/api/gateway/ns/crew/abc/stream', ...anon })).toMatchObject({ status: 401 });
	});
});

describe('with a session', () => {
	const on = { signedIn: true };

	it('allows reads and asking', () => {
		expect(decide({ method: 'GET', path: '/crews', ...on })).toEqual({ action: 'allow' });
		expect(decide({ method: 'POST', path: '/api/gateway/ns/crew/ask', ...on })).toEqual({ action: 'allow' });
		expect(decide({ method: 'POST', path: '/signout', ...on })).toEqual({ action: 'allow' });
	});

	it('allows acting on a conversation: stop, pause, resume and delete', () => {
		for (const action of ['stop', 'pause', 'resume']) {
			expect(decide({ method: 'POST', path: `/api/gateway/ns/crew/abc/${action}`, ...on })).toEqual({ action: 'allow' });
		}
		expect(decide({ method: 'DELETE', path: '/api/gateway/ns/crew/abc', ...on })).toEqual({ action: 'allow' });
	});

	it('allows only the method each write route is for', () => {
		expect(decide({ method: 'POST', path: '/api/gateway/ns/crew/abc', ...on })).toMatchObject({ status: 405 });
		expect(decide({ method: 'PUT', path: '/api/gateway/ns/crew/abc', ...on })).toMatchObject({ status: 405 });
		expect(decide({ method: 'DELETE', path: '/api/gateway/ns/crew/abc/stop', ...on })).toMatchObject({ status: 405 });
		expect(decide({ method: 'POST', path: '/api/gateway/ns/crew/conversations', ...on })).toMatchObject({ status: 405 });
	});

	it('asks a visitor who is not signed in to sign in before acting on a conversation', () => {
		expect(decide({ method: 'DELETE', path: '/api/gateway/ns/crew/abc' })).toMatchObject({ status: 401 });
		expect(decide({ method: 'POST', path: '/api/gateway/ns/crew/abc/stop', anonymousReadOnly: true })).toMatchObject({ status: 401 });
	});

	it('still refuses every other write with 405', () => {
		for (const path of ['/api/nats/publish', '/api/kubemoot/crews/x', '/crews', '/api/gateway/ns/crew/abc/restart', '/api/gateway/ns/crew/abc/stop/now']) {
			expect(decide({ method: 'DELETE', path, ...on })).toMatchObject({ action: 'deny', status: 405 });
			expect(decide({ method: 'POST', path, ...on })).toMatchObject({ action: 'deny', status: 405 });
		}
	});

	it('moves a signed-in visitor away from the sign-in page', () => {
		expect(decide({ method: 'GET', path: '/signin', ...on })).toEqual({ action: 'redirect', to: '/' });
	});
});

describe('pathInApp', () => {
	it('removes the base path', () => {
		expect(pathInApp('/dashboard/crews', '/dashboard')).toBe('/crews');
		expect(pathInApp('/dashboard', '/dashboard')).toBe('/');
		expect(pathInApp('/crews', '')).toBe('/crews');
		expect(pathInApp('/dashboardx/crews', '/dashboard')).toBe('/dashboardx/crews');
	});
});
