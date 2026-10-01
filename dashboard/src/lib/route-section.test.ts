import { describe, expect, it } from 'vitest';
import { routeSection } from './route-section';

describe('routeSection', () => {
	it('is the first segment of a route id', () => {
		expect(routeSection('/agents/[name]')).toBe('agents');
		expect(routeSection('/nodes')).toBe('nodes');
	});

	it('is empty for the root route', () => {
		expect(routeSection('/')).toBe('');
	});

	it('skips route groups', () => {
		expect(routeSection('/(app)/config')).toBe('config');
	});

	it('is null when no route matched', () => {
		expect(routeSection(null)).toBeNull();
	});
});
