import { describe, expect, it } from 'vitest';
import { asset, resolve } from '$app/paths';

// The $app/paths stand-in that store and client modules resolve URLs through in tests.
// It follows SvelteKit 3: a leading slash marks a route id, asset() takes a file name.
describe('$app/paths test stand-in', () => {
	it('returns a route id without parameters unchanged', () => {
		expect(resolve('/api/nats/purge')).toBe('/api/nats/purge');
	});

	it('fills route-id parameters and leaves unknown ones as they are', () => {
		const resolveRoute = resolve as unknown as (r: string, p?: Record<string, string>) => string;
		expect(resolveRoute('/api/kubemoot/watch/[plural]', { plural: 'agents' })).toBe(
			'/api/kubemoot/watch/agents'
		);
		expect(resolveRoute('/api/kubemoot/watch/[plural]')).toBe('/api/kubemoot/watch/[plural]');
	});

	it('places a pathname without a leading slash under the base', () => {
		const resolvePath = resolve as unknown as (p: string) => string;
		expect(resolvePath('api/health')).toBe('/api/health');
	});

	it('serves a static file by name from the assets root', () => {
		expect(asset('coordinator.webp')).toBe('/coordinator.webp');
	});

	it('drops a legacy leading slash on an asset the way SvelteKit does', () => {
		const assetPath = asset as unknown as (p: string) => string;
		expect(assetPath('/specialist.png')).toBe('/specialist.png');
	});
});
