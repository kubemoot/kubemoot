import { describe, it, expect } from 'vitest';
import { load } from './+page.server';

function redirectOf(search: string): { status: number; location: string } {
	try {
		load({ url: new URL('http://dash.test/messages' + search) } as Parameters<typeof load>[0]);
	} catch (e) {
		return e as { status: number; location: string };
	}
	throw new Error('no redirect thrown');
}

describe('/messages redirect', () => {
	it('permanently redirects to /conversations', () => {
		const r = redirectOf('');
		expect(r.status).toBe(308);
		expect(r.location).toBe('/conversations');
	});

	it('keeps the query string', () => {
		expect(redirectOf('?thread=abc').location).toBe('/conversations?thread=abc');
	});
});
