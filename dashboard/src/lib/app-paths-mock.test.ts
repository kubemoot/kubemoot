import { describe, expect, it } from 'vitest';
import { resolve } from '$app/paths';

// The $app/paths stand-in that store and client modules resolve URLs through in tests.
describe('$app/paths test stand-in', () => {
	it('returns a plain pathname unchanged', () => {
		expect(resolve('/api/nats/purge')).toBe('/api/nats/purge');
	});

	it('fills route-id parameters and leaves unknown ones as they are', () => {
		const resolveRoute = resolve as unknown as (r: string, p?: Record<string, string>) => string;
		expect(resolveRoute('/api/kubemoot/watch/[plural]', { plural: 'agents' })).toBe(
			'/api/kubemoot/watch/agents'
		);
		expect(resolveRoute('/api/kubemoot/watch/[plural]')).toBe('/api/kubemoot/watch/[plural]');
	});
});
