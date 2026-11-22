import { describe, expect, it } from 'vitest';
import { routeSection, showsCrewSelector } from './route-section';

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

describe('showsCrewSelector', () => {
	it('shows on the Overview and on crew-scoped pages', () => {
		expect(showsCrewSelector('/')).toBe(true);
		expect(showsCrewSelector('/moot')).toBe(true);
		expect(showsCrewSelector('/conversations')).toBe(true);
		expect(showsCrewSelector('/messages')).toBe(true);
		expect(showsCrewSelector('/mcpservers/[name]')).toBe(true);
	});

	it('hides on the pages that are not crew-scoped', () => {
		for (const id of ['/nodes', '/verify', '/config']) {
			expect(showsCrewSelector(id)).toBe(false);
		}
	});

	it('shows on an error page', () => {
		expect(showsCrewSelector(null)).toBe(true);
	});
});
