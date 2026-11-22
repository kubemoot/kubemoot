import { render, screen, waitFor } from '@testing-library/svelte';
import { createRawSnippet } from 'svelte';
import { get } from 'svelte/store';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { crewDirectory } from '#lib/stores/crews.js';
import { namespace } from '#lib/stores/namespace.js';
import { page, setPageData } from '../../vitest-mocks/app-state.svelte';
import Layout from './+layout.svelte';

const children = createRawSnippet(() => ({ render: () => '<p>page</p>' }));
const pilot = { namespace: 'crew-pilot', crew: 'homelab-pilot', displayName: 'Homelab Pilot' };

// The dashboard server answers the crew and version calls only for a signed-in person.
function server(signedIn: () => boolean) {
	return vi.fn(async (url: string) => {
		if (!signedIn()) return new Response('{}', { status: 401 });
		return new Response(JSON.stringify(url.endsWith('/api/version') ? { version: '1.2.3' } : { crews: [pilot] }));
	});
}

const data = (user: { username: string } | null) => ({ scoped: false, anonymousReadOnly: false, user });

beforeEach(() => {
	crewDirectory.set([]);
	namespace.set('');
	page.route.id = '/signin';
	setPageData({});
});
afterEach(() => vi.unstubAllGlobals());

describe('the root layout', () => {
	it('reads the crews and the version again after sign-in, without a reload', async () => {
		let signedIn = false;
		const fetchFn = server(() => signedIn);
		vi.stubGlobal('fetch', fetchFn);
		// The layout was loaded on the sign-in page, before any session existed.
		const view = render(Layout, { props: { children, data: data(null) } });
		expect(fetchFn).not.toHaveBeenCalled();

		// Signing in moves the page off /signin and gives the layout the signed-in user.
		signedIn = true;
		page.route.id = '/';
		await view.rerender({ children, data: data({ username: 'alice' }) });

		await waitFor(() => expect(get(crewDirectory)).toEqual([pilot]));
		expect(await screen.findByText(/v1\.2\.3/)).toBeTruthy();
		expect(screen.queryByText('Loading...')).toBeNull();
		expect(screen.queryByText('v...')).toBeNull();
	});

	it('forgets the signed-in person crews on sign-out and reads them again for the next one', async () => {
		let signedIn = true;
		vi.stubGlobal('fetch', server(() => signedIn));
		page.route.id = '/';
		const view = render(Layout, { props: { children, data: data({ username: 'alice' }) } });
		await waitFor(() => expect(get(crewDirectory)).toEqual([pilot]));

		signedIn = false;
		page.route.id = '/signin';
		await view.rerender({ children, data: data(null) });
		await waitFor(() => expect(get(crewDirectory)).toEqual([]));

		signedIn = true;
		page.route.id = '/';
		await view.rerender({ children, data: data({ username: 'bob' }) });
		await waitFor(() => expect(get(crewDirectory)).toEqual([pilot]));
	});

	it('ignores a late answer for a person who has since signed out', async () => {
		let release: (r: Response) => void = () => undefined;
		const late = new Promise<Response>((r) => (release = r));
		vi.stubGlobal('fetch', vi.fn(() => late));
		page.route.id = '/';
		const view = render(Layout, { props: { children, data: data({ username: 'alice' }) } });
		page.route.id = '/signin';
		await view.rerender({ children, data: data(null) });
		release(new Response(JSON.stringify({ crews: [pilot], version: '9' })));
		await new Promise((r) => setTimeout(r, 20));
		expect(get(crewDirectory)).toEqual([]);
	});
});
