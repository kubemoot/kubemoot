import { describe, it, expect } from 'vitest';
import { load } from './+page.server';

function redirectOf(search: string): { status: number; location: string } {
	try {
		load({ url: new URL('http://dash.test/discussions' + search) } as Parameters<typeof load>[0]);
	} catch (e) {
		return e as { status: number; location: string };
	}
	throw new Error('no redirect thrown');
}

describe('/discussions redirect', () => {
	it('permanently redirects to /moot', () => {
		const r = redirectOf('');
		expect(r.status).toBe(308);
		expect(r.location).toBe('/moot');
	});

	it('keeps the query string', () => {
		expect(redirectOf('?thread=abc').location).toBe('/moot?thread=abc');
	});
});
